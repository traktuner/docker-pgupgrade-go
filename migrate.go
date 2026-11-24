package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// streamGlobals migrates global objects (roles, database-level settings)
// from the source to the destination using pg_dumpall --globals-only
// piped into psql. Works for both docker and tcp endpoints.
func streamGlobals(src, dst pgConn) error {
	fmt.Println("Migrating global objects (roles)...")
	dumpCmd, err := src.toolCmd("pg_dumpall", "-U", src.User, "--globals-only")
	if err != nil {
		return err
	}
	psqlCmd, err := dst.toolCmdStdin("psql", "-U", dst.User, "-d", "postgres")
	if err != nil {
		return err
	}
	return runPipeline(dumpCmd, psqlCmd)
}

// streamDumpRestore streams a custom-format dump (pg_dump -Fc) from the
// source directly into pg_restore on the destination, without temporary
// files. Parallel restore (-j) is not possible while reading from stdin,
// so the restore stays single-threaded.
func streamDumpRestore(src, dst pgConn, dbName string) error {
	fmt.Printf("Streaming dump from %s to %s for database '%s'...\n", src.label(), dst.label(), dbName)
	dumpCmd, err := src.toolCmd("pg_dump", "-U", src.User, "-d", src.DB, "-Fc", "--no-owner", "--no-privileges")
	if err != nil {
		return err
	}
	restoreCmd, err := dst.toolCmdStdin("pg_restore", "-U", dst.User, "-d", dst.DB, "--clean", "--if-exists", "--no-owner", "--no-privileges")
	if err != nil {
		return err
	}
	return runPipeline(dumpCmd, restoreCmd)
}

// runPipeline connects stdout of producer to stdin of consumer and runs
// both, streaming data through an in-process pipe (no shell involved).
//
// Both sides are waited on concurrently, and the pipe's write end is
// closed as soon as EITHER side exits. This is what prevents a deadlock:
// when the consumer dies early (for example pg_restore failing on
// authentication), the producer would otherwise block forever writing
// into a pipe nobody reads.
//
// Error preference: a producer error that is not caused by the consumer's
// death (for example a wrong source password) is reported first, because
// it explains a truncated stream. A pipe-closed artifact is ignored in
// favor of the real consumer error.
func runPipeline(producer, consumer *exec.Cmd) error {
	pipeReader, pipeWriter := io.Pipe()
	producer.Stdout = pipeWriter
	consumer.Stdin = pipeReader

	// Both commands may write progress/diagnostics to stderr; forward them
	// separately so error attribution stays possible.
	var prodErr, consErr bytes.Buffer
	producer.Stderr = &prodErr
	consumer.Stderr = &consErr
	// Consumers may print progress to stdout; forward it to the process
	// stdout unless the caller captured it.
	if consumer.Stdout == nil {
		consumer.Stdout = os.Stdout
	}

	if err := producer.Start(); err != nil {
		return fmt.Errorf("failed to start %s: %w", redactCmd(producer), err)
	}
	if err := consumer.Start(); err != nil {
		// Closing the write end unblocks a reading producer; a writing
		// producer receives a pipe error and exits.
		_ = pipeWriter.Close()
		// A producer that never writes would wait on its connection
		// forever; kill it so this path always returns promptly.
		if producer.Process != nil {
			_ = producer.Process.Kill()
		}
		_ = producer.Wait()
		return fmt.Errorf("failed to start %s: %w", redactCmd(consumer), err)
	}

	prodDone := make(chan error, 1)
	go func() {
		prodDone <- producer.Wait()
		// Producer finished: give the consumer EOF.
		_ = pipeWriter.Close()
	}()
	consDone := make(chan error, 1)
	go func() {
		consDone <- consumer.Wait()
		// Consumer finished: unblock a producer still writing so it can
		// exit and be reaped.
		_ = pipeWriter.Close()
	}()

	prodWaitErr := <-prodDone
	consWaitErr := <-consDone

	prodErrStr := ""
	if prodWaitErr != nil {
		prodErrStr = prodWaitErr.Error()
	}
	// A producer that dies BECAUSE the consumer went away reports a closed
	// pipe, an already-closed pipe, dies from SIGPIPE, or exits with the
	// conventional SIGPIPE exit code 141 when a shell wraps it. These
	// artifacts must not mask the real consumer error.
	pipeArtifact := prodWaitErr != nil &&
		(strings.Contains(prodErrStr, "closed pipe") ||
			strings.Contains(prodErrStr, "broken pipe") ||
			strings.Contains(prodErrStr, "exit status 141"))

	if prodWaitErr != nil && !pipeArtifact {
		return fmt.Errorf("pipeline producer failed: %v - %s", prodWaitErr, prodErr.String())
	}
	if consWaitErr != nil {
		return fmt.Errorf("pipeline consumer failed: %v - %s", consWaitErr, consErr.String())
	}
	if prodWaitErr != nil {
		return fmt.Errorf("pipeline producer failed: %v - %s", prodWaitErr, prodErr.String())
	}
	return nil
}

// redactCmd renders a command for error messages with PGPASSWORD values
// replaced, so credentials never appear in logs.
func redactCmd(cmd *exec.Cmd) string {
	parts := make([]string, 0, len(cmd.Args))
	for _, a := range cmd.Args {
		if _, ok := strings.CutPrefix(a, "PGPASSWORD="); ok {
			parts = append(parts, "PGPASSWORD=***")
			continue
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}
