# Project Traps — docker-pgupgrade-go

Reusable, repo-specific hard constraints. Every future agent must honor these.
New entries are appended only; never rewrite or drop existing lines.

## Go toolchain pin is coupled to go.mod

- Constraint: Whenever `go.mod`'s `go` directive is raised (directly or by a
  Renovate dependency update), raise `GO_VERSION` in
  `.github/workflows/go.yaml` to the same Go minor in the same change set.
- Why: setup-go pins `GOTOOLCHAIN=local`; a newer `go` directive fails the CI
  build instantly ("go.mod requires go >= 1.26.0, running go 1.24.13") instead
  of downloading a toolchain. This kept master CI red from May 2026 to
  Nov 2026 and made every Renovate PR look "failing".
- Affected: `.github/workflows/go.yaml`, `go.mod`

## pg client tools must be >= newest involved server version

- Constraint: The host that runs the non-interactive (job) mode must have
  `pg_dump`/`pg_restore` at least as new as the newest PostgreSQL server it
  touches. Ship job runs only from images like the official `postgres:NN`
  base (see `Dockerfile`). Do not suppress or ignore the resulting
  "server version mismatch" abort from pg_dump/pg_restore.
- Why: `pg_dump`/`pg_restore` refuse servers newer than the client binary.
  There is no version check in this repository's code; enforcement is the
  client tools' own abort. Observed: local pg_dump 17.12 against a
  PostgreSQL 18.7 server.
- Affected: `Dockerfile`, `README.md` (documented), runtime job deployments

## Globals import rewrites destination role passwords

- Constraint: `pg_dumpall --globals-only` piped into the destination replaces
  its roles, including password hashes. After a `-globals` run the
  destination superuser password equals the SOURCE password. Sequenced runs
  that reuse pre-captured destination credentials will fail authentication.
  This is documented in README; behavior is intentionally unchanged from the
  pre-k8s implementation.
- Why: Roles are global objects; their password hashes belong to them.
- Affected: `migrate.go` (streamGlobals), `README.md`

## Interactive mode must not open a second buffered stdin reader

- Constraint: All interactive prompts must read through ONE shared
  `*bufio.Reader`; password masking must only call `term.ReadPassword` when
  `term.IsTerminal(os.Stdin.Fd())` is true.
- Why: The original code opened a fresh `bufio.Reader` inside
  `readPasswordWithDefault`, and `term.ReadPassword` reads raw bytes from the
  fd. With piped (non-tty) stdin the prompt sequence misaligned by one or
  more lines and selections landed on the wrong prompt, making the wizard
  unscriptable.
- Affected: `interactive.go` (readPasswordWithDefault, runInteractive)

## io.Pipe producer/consumer pipelines: close the write end when EITHER side exits

- Constraint: Never `Wait()` the two sides of an `io.Pipe` bridge
  sequentially. Wait both `exec.Cmd`s in goroutines and close the pipe's
  write end when EITHER side exits; treat `broken pipe`/`closed pipe`
  producer errors as artifacts of a dead consumer.
- Why: `io.Pipe` writes block until consumed. A consumer that exits early
  (pg_restore auth failure) leaves a producer with >32 KB of output blocked
  forever in `Wait()` — in k8s the Job hangs instead of failing. Found by
  review round 1; a 8 MB regression test now guards this.
- Affected: `migrate.go` (runPipeline), `main_test.go`

## Credential flags with env defaults must hide their display default

- Constraint: Any flag registered with a secret from the environment must
  have its `DefValue` blanked before any usage printing (see
  `hideSecretDefaults`). Command strings in errors must pass `redactCmd`.
- Why: Go's `flag.PrintDefaults` appends `(default "…")` for non-empty
  string defaults. With `PGSRC_PASSWORD` set, `-h` or one malformed argument
  wrote the Secret value into pod logs. Found by review round 1.
- Affected: `config.go` (hideSecretDefaults, parseNonInteractive),
  `migrate.go` (redactCmd)


## postgres:18+ official images use a new PGDATA layout

- Constraint: The interactive wizard's auto-create mounts the destination
  volume at `/var/lib/postgresql/data` (inherited from master). Official
  `postgres:18`+ images keep the cluster at
  `/var/lib/postgresql/<major>/docker` instead. Before relying on the
  auto-create flow with an 18+ destination image, adapt the mount path to
  the image's actual layout.
- Why: PGDATA layout changed in the official image line; a volume at the
  legacy path would not hold the cluster files. Pre-existing gap found by
  review round 5; the k8s job mode is unaffected (TCP only, no volumes).
- Affected: `interactive.go` (interactiveAutoCreateDest runArgs)
