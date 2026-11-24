package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// pgConn describes one PostgreSQL endpoint that this tool operates on.
//
// Mode "docker" runs client tools inside a Docker container via
// `docker exec`. Mode "tcp" runs locally installed client tools and
// connects to a host:port over the network (used for Kubernetes Jobs).
type pgConn struct {
	Mode      string // "docker" or "tcp"
	Container string // docker mode only
	Host      string // tcp mode only
	Port      string // tcp mode only
	User      string
	Password  string
	DB        string
}

const (
	modeDocker  = "docker"
	modeTCP     = "tcp"
	defaultPort = "5432"
)

func dockerConn(container, user, pass, db string) pgConn {
	return pgConn{Mode: modeDocker, Container: container, User: user, Password: pass, DB: db}
}

func tcpConn(host, port, user, pass, db string) pgConn {
	return pgConn{Mode: modeTCP, Host: host, Port: port, User: user, Password: pass, DB: db}
}

// label returns a human readable description of the endpoint for log output.
func (c pgConn) label() string {
	switch c.Mode {
	case modeDocker:
		return fmt.Sprintf("container '%s'", c.Container)
	case modeTCP:
		return fmt.Sprintf("%s:%s", c.Host, c.Port)
	default:
		return fmt.Sprintf("<unknown mode '%s'>", c.Mode)
	}
}

// toolCmd prepares an exec.Cmd that runs the named PostgreSQL client tool
// against this endpoint. The caller must start the command.
func (c pgConn) toolCmd(tool string, args ...string) (*exec.Cmd, error) {
	switch c.Mode {
	case modeDocker:
		full := append([]string{"exec", "-e", "PGPASSWORD=" + c.Password, c.Container, tool}, args...)
		return exec.Command("docker", full...), nil
	case modeTCP:
		path, err := exec.LookPath(tool)
		if err != nil {
			return nil, fmt.Errorf("required client tool '%s' not found in PATH (install postgresql-client): %w", tool, err)
		}
		cmd := exec.Command(path, args...)
		// connect_timeout bounds the initial TCP handshake; without it a
		// blackholed host stalls until the OS-level TCP timeout. pg_isready
		// needs no extra flag: it applies its own connection timeout
		// (libpq default 3 seconds) to every readiness probe.
		cmd.Env = append(os.Environ(),
			"PGCONNECT_TIMEOUT=10",
			"PGPASSWORD="+c.Password,
			"PGHOST="+c.Host,
			"PGPORT="+c.Port,
		)
		return cmd, nil
	default:
		return nil, fmt.Errorf("unknown connection mode '%s'", c.Mode)
	}
}

// toolCmdStdin prepares a client tool command that reads its input from
// stdin (pg_restore, psql). Docker mode adds `exec -i` so the container
// process sees the piped stdin.
func (c pgConn) toolCmdStdin(tool string, args ...string) (*exec.Cmd, error) {
	switch c.Mode {
	case modeDocker:
		full := append([]string{"exec", "-i", "-e", "PGPASSWORD=" + c.Password, c.Container, tool}, args...)
		return exec.Command("docker", full...), nil
	default:
		return c.toolCmd(tool, args...)
	}
}

// checkConnection verifies with pg_isready that the endpoint accepts
// connections. pg_isready does not verify credentials; callers that need
// authenticated access rely on the subsequent dump/restore commands.
func (c pgConn) checkConnection() bool {
	fmt.Printf("Checking PostgreSQL connection for %s...\n", c.label())
	cmd, err := c.toolCmd("pg_isready", "-U", c.User, "-d", c.DB)
	if err != nil {
		fmt.Printf("Failed to prepare connection check for %s: %v\n", c.label(), err)
		return false
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("Failed to connect to PostgreSQL on %s: %v - %s\n", c.label(), err, stderr.String())
		return false
	}
	fmt.Printf("PostgreSQL on %s is ready.\n", c.label())
	return true
}

// waitForReady polls checkConnection until it succeeds or the timeout
// elapses.
func (c pgConn) waitForReady(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if c.checkConnection() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Second)
	}
}

// checkToolsRequired verifies that every client tool the selected mode
// needs is available, so the tool fails fast with a clear message instead
// of halfway through a migration.
func (c pgConn) checkToolsRequired(tools ...string) error {
	switch c.Mode {
	case modeDocker:
		// Tools run inside the official postgres image; assumed present.
		return nil
	case modeTCP:
		for _, t := range tools {
			if _, err := exec.LookPath(t); err != nil {
				return fmt.Errorf("required client tool '%s' not found in PATH (install postgresql-client): %w", t, err)
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown connection mode '%s'", c.Mode)
	}
}
