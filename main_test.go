package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestNormalizeSchemaDropsCommentsBlanksAndOwner(t *testing.T) {
	in := `-- comment line
\restrict rndtok
SET transaction_timeout = 0;

CREATE TABLE public.foo (id integer);
ALTER TABLE public.foo OWNER TO pguser;
\unrestrict rndtok
`
	want := "CREATE TABLE public.foo (id integer);"
	if got := normalizeSchema(in); got != want {
		t.Fatalf("normalizeSchema = %q, want %q", got, want)
	}
}

func TestParseVerifyMode(t *testing.T) {
	cases := map[string]string{
		"quick":  "quick",
		" FULL ": "full",
		"":       "none",
		"none":   "none",
		"bogus":  "none",
	}
	for in, want := range cases {
		if got := parseVerifyMode(in); got != want {
			t.Fatalf("parseVerifyMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnvBoolAndFirstNonEmpty(t *testing.T) {
	for _, v := range []string{"1", "true", "Yes", "Y", " yes "} {
		if !envBool(v) {
			t.Fatalf("envBool(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "no", "false"} {
		if envBool(v) {
			t.Fatalf("envBool(%q) = true, want false", v)
		}
	}
	if got := firstNonEmpty("", "", "x"); got != "x" {
		t.Fatalf("firstNonEmpty = %q, want x", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Fatalf("firstNonEmpty() = %q, want empty", got)
	}
}

func TestPqQuoting(t *testing.T) {
	if got := pqQuoteIdent(`pu"bli"c`); got != `"pu""bli""c"` {
		t.Fatalf("pqQuoteIdent = %q", got)
	}
	if got := pqQuoteLiteral("it's"); got != `'it''s'` {
		t.Fatalf("pqQuoteLiteral = %q", got)
	}
}

func TestParseNonInteractiveFlagWinsOverEnv(t *testing.T) {
	t.Setenv("PGSRC_HOST", "env-src-host")
	t.Setenv("PGDST_HOST", "env-dst-host")
	t.Setenv("PGSRC_DB", "envdb")
	t.Setenv("PGUPGRADE_VERIFY", "full")

	cfg, err := parseNonInteractive([]string{"-src-host", "flag-src-host"})
	if err != nil {
		t.Fatalf("parseNonInteractive: %v", err)
	}
	if cfg.Src.Host != "flag-src-host" {
		t.Fatalf("flag value lost: %q", cfg.Src.Host)
	}
	if cfg.Dst.Host != "env-dst-host" {
		t.Fatalf("env default lost: %q", cfg.Dst.Host)
	}
	if cfg.Src.Port != defaultPort || cfg.Dst.Port != defaultPort {
		t.Fatalf("port default lost: %q / %q", cfg.Src.Port, cfg.Dst.Port)
	}
	if cfg.Src.User != "postgres" || cfg.Dst.User != "postgres" {
		t.Fatalf("user default lost: %q / %q", cfg.Src.User, cfg.Dst.User)
	}
	// -dst-db falls back to PGSRC_DB
	if cfg.Dst.DB != "envdb" {
		t.Fatalf("dst db env fallback lost: %q", cfg.Dst.DB)
	}
	if cfg.Verify != "full" {
		t.Fatalf("verify env default lost: %q", cfg.Verify)
	}
}

func TestParseNonInteractiveValidationErrors(t *testing.T) {
	t.Setenv("PGSRC_HOST", "")
	t.Setenv("PGDST_HOST", "")
	if _, err := parseNonInteractive(nil); err == nil {
		t.Fatal("expected validation error with missing hosts")
	}

	// invalid dst-wait
	_, err := parseNonInteractive([]string{"-src-host", "a", "-src-db", "x", "-dst-host", "b", "-dst-db", "y", "-dst-wait", "nope"})
	if err == nil || !strings.Contains(err.Error(), "dst-wait") {
		t.Fatalf("expected dst-wait error, got %v", err)
	}

	// dst-wait above the cap is rejected instead of overflowing the duration
	_, err = parseNonInteractive([]string{"-src-host", "a", "-src-db", "x", "-dst-host", "b", "-dst-db", "y", "-dst-wait", "10000000000"})
	if err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("expected dst-wait cap error, got %v", err)
	}

	// src == dst endpoint is rejected
	_, err = parseNonInteractive([]string{"-src-host", "a", "-src-db", "x", "-dst-host", "a", "-dst-db", "x"})
	if err == nil || !strings.Contains(err.Error(), "same endpoint") {
		t.Fatalf("expected same-endpoint error, got %v", err)
	}
	// same host, different DB is fine
	cfg, err := parseNonInteractive([]string{"-src-host", "a", "-src-db", "x", "-dst-host", "a", "-dst-db", "y"})
	if err != nil {
		t.Fatalf("same host different db should be accepted, got %v", err)
	}
	if cfg.DstWait != 0 {
		t.Fatalf("dst wait default not zero: %v", cfg.DstWait)
	}

	// unknown verify mode is a config error (never a silent downgrade)
	_, err = parseNonInteractive([]string{"-src-host", "a", "-src-db", "x", "-dst-host", "b", "-dst-db", "y", "-verify", "sometimes"})
	if err == nil || !strings.Contains(err.Error(), "verification mode") {
		t.Fatalf("expected verify error, got %v", err)
	}
	t.Setenv("PGUPGRADE_VERIFY", "sometimes")
	if _, err := parseNonInteractive([]string{"-src-host", "a", "-src-db", "x", "-dst-host", "b", "-dst-db", "y"}); err == nil {
		t.Fatal("expected verify error from env value")
	}

	// uppercase stays accepted and normalizes to the canonical value
	t.Setenv("PGUPGRADE_VERIFY", "")
	cfg, err = parseNonInteractive([]string{"-src-host", "a", "-src-db", "x", "-dst-host", "b", "-dst-db", "y", "-verify", "FULL"})
	if err != nil {
		t.Fatalf("uppercase verify should be accepted, got %v", err)
	}
	if cfg.Verify != "full" {
		t.Fatalf("verify normalization failed: %q", cfg.Verify)
	}
}

func TestHideSecretDefaultsInUsage(t *testing.T) {
	t.Setenv("PGSRC_PASSWORD", "topsecret123")
	t.Setenv("PGDST_PASSWORD", "dstsecret456")

	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.String("src-password", os.Getenv("PGSRC_PASSWORD"), "source password (env PGSRC_PASSWORD)")
	fs.String("dst-password", os.Getenv("PGDST_PASSWORD"), "destination password (env PGDST_PASSWORD)")

	hideSecretDefaults(fs)

	var buf strings.Builder
	fs.SetOutput(&buf)
	fs.PrintDefaults()
	if strings.Contains(buf.String(), "topsecret123") || strings.Contains(buf.String(), "dstsecret456") {
		t.Fatalf("usage leaks passwords: %q", buf.String())
	}
	// the flag's functional default must remain the env value
	if f := fs.Lookup("src-password"); f.Value.String() != "topsecret123" {
		t.Fatalf("functional default lost: %q", f.Value.String())
	}
}

func TestRedactCmdHidesPasswords(t *testing.T) {
	cmd := exec.Command("docker", "exec", "-e", "PGPASSWORD=hunter2", "pg-src", "pg_dump", "-U", "u")
	got := redactCmd(cmd)
	if strings.Contains(got, "hunter2") {
		t.Fatalf("redactCmd leaks password: %q", got)
	}
	if got != "docker exec -e PGPASSWORD=*** pg-src pg_dump -U u" {
		t.Fatalf("redactCmd = %q", got)
	}
}

// TestRunPipelineConsumerEarlyDeathMustNotHang is the regression test for
// the round-1 BLOCKER: a consumer that exits before reading its stdin must
// not deadlock a producer whose output exceeds one pipe chunk.
func TestRunPipelineConsumerEarlyDeathMustNotHang(t *testing.T) {
	// 8 MB producer output, consumer exits immediately without reading.
	producer := exec.Command("dd", "if=/dev/zero", "bs=1M", "count=8")
	consumer := exec.Command("false")

	done := make(chan error, 1)
	go func() { done <- runPipeline(producer, consumer) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error when the consumer fails early")
		}
		if !strings.Contains(err.Error(), "pipeline consumer failed") {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("runPipeline deadlocked: producer still writing after consumer death")
	}
}

// TestRunPipelineProducerEarlyDeath surfaces the root-cause error when the
// producer fails (for example bad source credentials) while the consumer
// is still healthy.
func TestRunPipelineProducerEarlyDeath(t *testing.T) {
	producer := exec.Command("sh", "-c", "echo boom >&2; exit 2")
	// A consumer that keeps the pipe alive briefly; harmless on failure.
	consumer := exec.Command("cat")

	err := runPipeline(producer, consumer)
	if err == nil {
		t.Fatal("expected producer error")
	}
	if !strings.Contains(err.Error(), "pipeline producer failed") {
		t.Fatalf("expected producer error, got %v", err)
	}
}

// TestRunPipelineSuccess flows a large payload through the pipe to prove
// the happy path is unaffected by the close-on-either-exit logic.
func TestRunPipelineSuccess(t *testing.T) {
	producer := exec.Command("sh", "-c", "dd if=/dev/zero bs=1M count=8 2>/dev/null")
	consumer := exec.Command("wc", "-c")
	var out bytes.Buffer
	consumer.Stdout = &out

	if err := runPipeline(producer, consumer); err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	// wc -c sees 8 MiB on its stdin; last line contains the count.
	fields := strings.Fields(strings.TrimSpace(out.String()))
	if len(fields) == 0 {
		t.Fatalf("no wc output: %q", out.String())
	}
	if fields[len(fields)-1] != "8388608" {
		t.Fatalf("byte count = %q, want 8388608", fields[len(fields)-1])
	}
}

func TestToolCmdDockerWrapsExec(t *testing.T) {
	c := dockerConn("pg-src", "admin", "secret", "mydb")
	cmd, err := c.toolCmd("pg_dump", "-U", c.User, "-d", c.DB)
	if err != nil {
		t.Fatalf("toolCmd: %v", err)
	}
	want := []string{"docker", "exec", "-e", "PGPASSWORD=secret", "pg-src", "pg_dump", "-U", "admin", "-d", "mydb"}
	if strings.Join(cmd.Args, " ") != strings.Join(want, " ") {
		t.Fatalf("docker cmd args = %v, want %v", cmd.Args, want)
	}
}

func TestToolCmdTCPSetsPGEnv(t *testing.T) {
	c := tcpConn("10.0.0.5", "6432", "admin", "secret", "mydb")
	cmd, err := c.toolCmd("psql", "-c", "SELECT 1")
	if err != nil {
		t.Fatalf("toolCmd: %v", err)
	}
	if !strings.HasSuffix(cmd.Path, "psql") {
		t.Fatalf("unexpected binary path %q", cmd.Path)
	}
	env := map[string]bool{}
	for _, kv := range cmd.Env {
		parts := strings.SplitN(kv, "=", 2)
		env[parts[0]] = true
		if len(parts) == 2 {
			switch parts[0] {
			case "PGHOST":
				if parts[1] != "10.0.0.5" {
					t.Fatalf("PGHOST = %q", parts[1])
				}
			case "PGPORT":
				if parts[1] != "6432" {
					t.Fatalf("PGPORT = %q", parts[1])
				}
			case "PGPASSWORD":
				if parts[1] != "secret" {
					t.Fatalf("PGPASSWORD mismatch")
				}
			}
		}
	}
	for _, key := range []string{"PGHOST", "PGPORT", "PGPASSWORD"} {
		if !env[key] {
			t.Fatalf("missing %s in tcp cmd env", key)
		}
	}
	if len(cmd.Args) != 3 || cmd.Args[1] != "-c" || cmd.Args[2] != "SELECT 1" {
		t.Fatalf("unexpected psql args: %v", cmd.Args)
	}
}

func TestToolCmdStdinAddsInteractiveFlag(t *testing.T) {
	c := dockerConn("pg-dst", "admin", "secret", "mydb")
	cmd, err := c.toolCmdStdin("pg_restore", "-U", c.User, "-d", c.DB)
	if err != nil {
		t.Fatalf("toolCmdStdin: %v", err)
	}
	want := []string{"docker", "exec", "-i", "-e", "PGPASSWORD=secret", "pg-dst", "pg_restore", "-U", "admin", "-d", "mydb"}
	if strings.Join(cmd.Args, " ") != strings.Join(want, " ") {
		t.Fatalf("docker stdin cmd args = %v, want %v", cmd.Args, want)
	}
	// tcp mode must be identical to toolCmd
	tcpCmd, err := tcpConn("h", "5432", "u", "p", "d").toolCmdStdin("psql", "-c", "SELECT 1")
	if err != nil {
		t.Fatalf("toolCmdStdin tcp: %v", err)
	}
	plain, err := tcpConn("h", "5432", "u", "p", "d").toolCmd("psql", "-c", "SELECT 1")
	if err != nil {
		t.Fatalf("toolCmd tcp: %v", err)
	}
	if strings.Join(tcpCmd.Args, " ") != strings.Join(plain.Args, " ") {
		t.Fatalf("tcp stdin args %v != plain %v", tcpCmd.Args, plain.Args)
	}
}

func TestLabelAndCheckToolsRequired(t *testing.T) {
	if got := dockerConn("c1", "u", "p", "d").label(); got != "container 'c1'" {
		t.Fatalf("docker label = %q", got)
	}
	if got := tcpConn("h", "1234", "u", "p", "d").label(); got != "h:1234" {
		t.Fatalf("tcp label = %q", got)
	}
	// unknown binary on PATH must produce an error in tcp mode
	err := tcpConn("h", "5432", "u", "p", "d").checkToolsRequired("definitely-not-installed-tool-xyz")
	if err == nil {
		t.Fatal("expected tool lookup error")
	}
	// docker mode assumes tools are inside the image
	if err := dockerConn("c1", "u", "p", "d").checkToolsRequired("definitely-not-installed-tool-xyz"); err != nil {
		t.Fatalf("docker mode tool check should pass, got %v", err)
	}
}

func TestErrHelpPropagation(t *testing.T) {
	fs := flag.NewFlagSet(appname, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse([]string{"-h"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected ErrHelp, got %v", err)
	}
}
