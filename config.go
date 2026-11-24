package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// nonInteractiveConfig holds everything the non-interactive mode needs.
type nonInteractiveConfig struct {
	Src            pgConn
	Dst            pgConn
	MigrateGlobals bool
	Verify         string
	DstWait        time.Duration
}

// parseNonInteractive builds the configuration for the non-interactive
// mode from command line arguments. Flag values win; environment
// variables (for example injected from a Kubernetes Secret) provide the
// defaults so a Job can configure the whole run through env only.
func parseNonInteractive(args []string) (*nonInteractiveConfig, error) {
	cfg := &nonInteractiveConfig{}

	fs := flag.NewFlagSet(appname, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	srcHost := fs.String("src-host", os.Getenv("PGSRC_HOST"), "source PostgreSQL host (env PGSRC_HOST)")
	srcPort := fs.String("src-port", firstNonEmpty(os.Getenv("PGSRC_PORT"), defaultPort), "source PostgreSQL port (env PGSRC_PORT)")
	srcUser := fs.String("src-user", firstNonEmpty(os.Getenv("PGSRC_USER"), "postgres"), "source database user (env PGSRC_USER)")
	srcPass := fs.String("src-password", os.Getenv("PGSRC_PASSWORD"), "source database password; prefer the env variable so it stays out of process lists (env PGSRC_PASSWORD)")
	srcDB := fs.String("src-db", os.Getenv("PGSRC_DB"), "source database name (env PGSRC_DB)")

	dstHost := fs.String("dst-host", os.Getenv("PGDST_HOST"), "destination PostgreSQL host (env PGDST_HOST)")
	dstPort := fs.String("dst-port", firstNonEmpty(os.Getenv("PGDST_PORT"), defaultPort), "destination PostgreSQL port (env PGDST_PORT)")
	dstUser := fs.String("dst-user", firstNonEmpty(os.Getenv("PGDST_USER"), "postgres"), "destination database user (env PGDST_USER)")
	dstPass := fs.String("dst-password", os.Getenv("PGDST_PASSWORD"), "destination database password; prefer the env variable so it stays out of process lists (env PGDST_PASSWORD)")
	dstDB := fs.String("dst-db", firstNonEmpty(os.Getenv("PGDST_DB"), os.Getenv("PGSRC_DB")), "destination database name (env PGDST_DB, falls back to PGSRC_DB)")

	fs.BoolVar(&cfg.MigrateGlobals, "globals", envBool(os.Getenv("PGUPGRADE_GLOBALS")), "also migrate global objects such as roles (env PGUPGRADE_GLOBALS=true)")
	fs.StringVar(&cfg.Verify, "verify", strings.TrimSpace(os.Getenv("PGUPGRADE_VERIFY")), "post-migration verification: none, quick, full (env PGUPGRADE_VERIFY)")
	dstWait := fs.String("dst-wait", os.Getenv("PGUPGRADE_DST_WAIT"), "seconds to wait for the destination to accept connections before the first hard check (env PGUPGRADE_DST_WAIT)")

	// The usage output (printed for -h and on parse errors) renders flag
	// defaults. Values injected from the environment would print the
	// password; blank the display defaults for the credential flags only.
	// The functional defaults stay untouched.
	hideSecretDefaults(fs)

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	// Normalize the verification mode case-insensitively (env value and flag
	// value alike). An unknown value is a configuration error, not a silent
	// downgrade: a typo must never quietly turn verification off in an
	// unattended Job run.
	switch mode := strings.ToLower(cfg.Verify); mode {
	case "", "none":
		cfg.Verify = "none"
	case "quick", "full":
		cfg.Verify = mode
	default:
		return nil, fmt.Errorf("unknown verification mode %q (-verify: none, quick, full; env PGUPGRADE_VERIFY)", cfg.Verify)
	}

	cfg.Src = tcpConn(*srcHost, *srcPort, *srcUser, *srcPass, *srcDB)
	cfg.Dst = tcpConn(*dstHost, *dstPort, *dstUser, *dstPass, *dstDB)

	if wait := strings.TrimSpace(*dstWait); wait != "" {
		secs, err := strconv.Atoi(wait)
		if err != nil || secs < 0 {
			return nil, fmt.Errorf("invalid -dst-wait value %q: use a non-negative number of seconds", wait)
		}
		if secs > maxDstWaitSeconds {
			return nil, fmt.Errorf("invalid -dst-wait value %q: maximum is %d seconds", wait, maxDstWaitSeconds)
		}
		cfg.DstWait = time.Duration(secs) * time.Second
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// maxDstWaitSeconds caps -dst-wait. Without a cap, very large values would
// overflow time.Duration into a negative number, silently disabling the
// wait (fail-safe but surprising).
const maxDstWaitSeconds = 86400 // one day

// validate returns an error when the configuration cannot run. TCP mode
// is the only supported target for the non-interactive flow.
func (cfg *nonInteractiveConfig) validate() error {
	var problems []string
	if cfg.Src.Host == "" {
		problems = append(problems, "source host is missing (-src-host or PGSRC_HOST)")
	}
	if cfg.Src.DB == "" {
		problems = append(problems, "source database name is missing (-src-db or PGSRC_DB)")
	}
	if cfg.Dst.Host == "" {
		problems = append(problems, "destination host is missing (-dst-host or PGDST_HOST)")
	}
	if cfg.Dst.DB == "" {
		problems = append(problems, "destination database name is missing (-dst-db or PGDST_DB)")
	}
	// Restoring with --clean --if-exists into the SAME endpoint the dump
	// reads from would drop source objects mid-stream. Reject it instead.
	if cfg.Src.Host == cfg.Dst.Host && cfg.Src.Port == cfg.Dst.Port && cfg.Src.DB == cfg.Dst.DB {
		problems = append(problems, "source and destination are the same endpoint (-src-*/-dst-*): restoring would drop the source objects while dumping")
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// hideSecretDefaults blanks the DISPLAY defaults of the password flags so
// the rendered usage never contains environment-injected passwords. The
// functional default of each flag stays untouched.
func hideSecretDefaults(fs *flag.FlagSet) {
	for _, secretFlag := range []string{"src-password", "dst-password"} {
		if f := fs.Lookup(secretFlag); f != nil {
			f.DefValue = ""
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func envBool(value string) bool {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "1", "true", "yes", "y":
		return true
	default:
		return false
	}
}

// runNonInteractive performs the whole migration without prompts and
// returns an error on any failure. It is the path a Kubernetes Job uses.
func runNonInteractive(cfg *nonInteractiveConfig) error {
	required := []string{"pg_dump", "pg_restore", "pg_isready", "psql"}
	if cfg.MigrateGlobals {
		required = append(required, "pg_dumpall")
	}
	if err := cfg.Src.checkToolsRequired(required...); err != nil {
		return err
	}
	if err := cfg.Dst.checkToolsRequired(required...); err != nil {
		return err
	}

	if cfg.DstWait > 0 {
		fmt.Printf("Waiting up to %s for the destination PostgreSQL to accept connections...\n", cfg.DstWait)
		if !cfg.Dst.waitForReady(cfg.DstWait) {
			return fmt.Errorf("destination %s did not accept connections within %s", cfg.Dst.label(), cfg.DstWait)
		}
	}

	if !cfg.Src.checkConnection() {
		return fmt.Errorf("cannot reach source database on %s", cfg.Src.label())
	}
	if !cfg.Dst.checkConnection() {
		return fmt.Errorf("cannot reach destination database on %s", cfg.Dst.label())
	}

	if cfg.MigrateGlobals {
		if err := streamGlobals(cfg.Src, cfg.Dst); err != nil {
			return fmt.Errorf("migrating global objects failed: %w", err)
		}
	}

	if err := streamDumpRestore(cfg.Src, cfg.Dst, cfg.Src.DB); err != nil {
		return fmt.Errorf("migrating database failed: %w", err)
	}
	fmt.Println("Database migration completed successfully.")

	if err := verifyMigration(cfg.Src, cfg.Dst, cfg.Verify); err != nil {
		return fmt.Errorf("verification failed: %w", err)
	}
	if parseVerifyMode(cfg.Verify) != "none" {
		fmt.Println("Verification passed.")
	}
	return nil
}
