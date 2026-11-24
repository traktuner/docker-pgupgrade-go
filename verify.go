package main

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// ===== Verification helpers =====

// parseVerifyMode normalizes a verification mode input.
func parseVerifyMode(s string) string {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "quick":
		return "quick"
	case "full":
		return "full"
	default:
		return "none"
	}
}

// runPostMigrationVerification asks the user for a verification mode and
// runs it in the interactive flow.
func runPostMigrationVerification(reader *bufio.Reader, src, dst pgConn) {
	fmt.Print("Run post-migration verification? (none/quick/full) [none]: ")
	modeStr, _ := reader.ReadString('\n')
	mode := strings.TrimSpace(strings.ToLower(modeStr))
	if mode == "" {
		mode = "none"
	}
	if parseVerifyMode(mode) == "none" && mode != "none" {
		fmt.Println("Unknown verification mode; skipping.")
		return
	}
	switch parseVerifyMode(mode) {
	case "none":
		return
	case "quick":
		if err := verifySchemaEqual(src, dst); err != nil {
			fmt.Printf("Verification (schema) failed: %v\n", err)
		} else {
			fmt.Println("Schema verification passed.")
		}
	case "full":
		if err := verifySchemaEqual(src, dst); err != nil {
			fmt.Printf("Verification (schema) failed: %v\n", err)
			// continue to counts to provide more info
		} else {
			fmt.Println("Schema verification passed.")
		}
		if err := verifyRowCountsEqual(src, dst); err != nil {
			fmt.Printf("Verification (row counts) failed: %v\n", err)
		} else {
			fmt.Println("Row counts verification passed.")
		}
	}
}

// verifyMigration runs the selected verification mode without prompts and
// returns the first failure. Used by the non-interactive flow, where a
// verification failure must fail the whole run. The mode is normalized in
// parseNonInteractive; parseVerifyMode maps anything unknown to "none", so
// the default branch below handles exactly "none".
func verifyMigration(src, dst pgConn, mode string) error {
	switch parseVerifyMode(mode) {
	case "quick":
		return verifySchemaEqual(src, dst)
	case "full":
		if err := verifySchemaEqual(src, dst); err != nil {
			return err
		}
		return verifyRowCountsEqual(src, dst)
	default: // "none"
		return nil
	}
}

func verifySchemaEqual(src, dst pgConn) error {
	srcSchema, err := dumpSchema(src)
	if err != nil {
		return fmt.Errorf("src schema dump failed: %w", err)
	}
	dstSchema, err := dumpSchema(dst)
	if err != nil {
		return fmt.Errorf("dst schema dump failed: %w", err)
	}
	normSrc := normalizeSchema(srcSchema)
	normDst := normalizeSchema(dstSchema)
	if normSrc != normDst {
		return fmt.Errorf("schema differs between source and destination")
	}
	return nil
}

func dumpSchema(c pgConn) (string, error) {
	cmd, err := c.toolCmd("pg_dump", "-U", c.User, "-d", c.DB, "-s", "--no-owner", "--no-privileges")
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%v - %s", err, errBuf.String())
	}
	return out.String(), nil
}

func normalizeSchema(s string) string {
	lines := strings.Split(s, "\n")
	var kept []string
	for _, l := range lines {
		// drop comments and blank lines
		if strings.HasPrefix(strings.TrimSpace(l), "--") || strings.TrimSpace(l) == "" {
			continue
		}
		// normalize OWNER TO differences
		if strings.Contains(l, " OWNER TO ") {
			continue
		}
		// pg_dump >= 17.5 emits \restrict/\unrestrict psql meta commands with
		// a random token; the token differs on every dump, so drop them.
		if strings.HasPrefix(l, "\\restrict ") || strings.HasPrefix(l, "\\unrestrict ") {
			continue
		}
		// Session SET lines can differ between server versions (for example
		// `SET transaction_timeout = 0;` exists only on PostgreSQL 18+).
		// Comparing schemas across versions requires ignoring them; actual
		// schema settings survive as ALTER TABLE/CREATE lines.
		if strings.HasPrefix(l, "SET ") {
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n")
}

func verifyRowCountsEqual(src, dst pgConn) error {
	tables, err := listUserTables(src)
	if err != nil {
		return fmt.Errorf("listing tables failed: %w", err)
	}
	if len(tables) == 0 {
		return nil
	}
	srcCounts, err := fetchRowCounts(src, tables)
	if err != nil {
		return fmt.Errorf("source counts failed: %w", err)
	}
	dstCounts, err := fetchRowCounts(dst, tables)
	if err != nil {
		return fmt.Errorf("destination counts failed: %w", err)
	}
	var diffs []string
	for _, t := range tables {
		key := t[0] + "." + t[1]
		if srcCounts[key] != dstCounts[key] {
			diffs = append(diffs, fmt.Sprintf("%s: src=%d dst=%d", key, srcCounts[key], dstCounts[key]))
		}
	}
	if len(diffs) > 0 {
		return fmt.Errorf("row count differences detected:\n%s", strings.Join(diffs, "\n"))
	}
	return nil
}

func listUserTables(c pgConn) ([][2]string, error) {
	sql := `SELECT table_schema, table_name
FROM information_schema.tables
WHERE table_type='BASE TABLE'
  AND table_schema NOT IN ('pg_catalog','information_schema')
ORDER BY table_schema, table_name;`
	out, err := runPsqlQuery(c, sql)
	if err != nil {
		return nil, err
	}
	var tables [][2]string
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) != 2 {
			continue
		}
		schema := parts[0]
		name := parts[1]
		tables = append(tables, [2]string{schema, name})
	}
	return tables, nil
}

func fetchRowCounts(c pgConn, tables [][2]string) (map[string]int64, error) {
	// Build a single UNION ALL query for performance
	var sb strings.Builder
	for i, t := range tables {
		if i > 0 {
			sb.WriteString(" UNION ALL ")
		}
		// SELECT 'schema.table', COUNT(*) FROM schema.table
		tbl := fmt.Sprintf("%s.%s", pqQuoteIdent(t[0]), pqQuoteIdent(t[1]))
		label := fmt.Sprintf("%s.%s", t[0], t[1])
		sb.WriteString(fmt.Sprintf("SELECT %s, COUNT(*)::bigint FROM %s", pqQuoteLiteral(label), tbl))
	}
	sb.WriteString(" ORDER BY 1;")
	out, err := runPsqlQuery(c, sb.String())
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Split(line, ",")
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		valStr := parts[1]
		n, _ := strconv.ParseInt(strings.TrimSpace(valStr), 10, 64)
		counts[key] = n
	}
	return counts, nil
}

func runPsqlQuery(c pgConn, sql string) (string, error) {
	cmd, err := c.toolCmd("psql", "-U", c.User, "-d", c.DB, "-t", "-A", "-F", ",", "-c", sql)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%v - %s", err, errBuf.String())
	}
	return out.String(), nil
}

func pqQuoteIdent(ident string) string {
	// double quote and escape quotes
	return "\"" + strings.ReplaceAll(ident, "\"", "\"\"") + "\""
}

// pqQuoteLiteral single-quotes a string literal for SQL. Used for labels in
// the generated row-count query.
func pqQuoteLiteral(literal string) string {
	return "'" + strings.ReplaceAll(literal, "'", "''") + "'"
}
