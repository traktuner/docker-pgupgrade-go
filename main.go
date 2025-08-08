package main

import (
    "bufio"
    "bytes"
    "fmt"
    "os"
    "os/exec"
    "strconv"
    "strings"
    "time"

    "golang.org/x/term"
)

var (
	appname = "docker-pgupgrade-go"
	version = "0.1"
	author = "Traktuner"
    // Set via -ldflags in build.sh / CI
    Tag       = ""
    Commit    = ""
    BuildTime = ""
)

func main() {
	reader := bufio.NewReader(os.Stdin)

	//Output version tag
    fmt.Println("====================================")
    fmt.Printf(" %s\n", appname)
    fmt.Printf(" Version: %s\n", version)
    if Tag != "" {
        fmt.Printf(" Tag: %s\n", Tag)
    }
    if Commit != "" {
        fmt.Printf(" Commit: %s\n", Commit)
    }
    if BuildTime != "" {
        fmt.Printf(" BuildTime: %s\n", BuildTime)
    }
    fmt.Printf(" Developed by: %s\n", author)
    fmt.Println("====================================")

    // Query Postgres Docker containers (filter by image name containing 'postgres')
    fmt.Println("Querying all running Docker containers...")
    containers, err := exec.Command("docker", "ps", "--format", "{{.Names}}::{{.Image}}").Output()
	if err != nil {
		fmt.Printf("Error querying Docker containers: %v\n", err)
		return
	}

    rawLines := strings.Split(strings.TrimSpace(string(containers)), "\n")
    var containerNames []string
    for _, line := range rawLines {
        if strings.TrimSpace(line) == "" {
            continue
        }
        parts := strings.SplitN(line, "::", 2)
        name := parts[0]
        image := ""
        if len(parts) == 2 {
            image = parts[1]
        }
        if strings.Contains(strings.ToLower(image), "postgres") {
            containerNames = append(containerNames, name)
        }
    }
    if len(containerNames) == 0 {
		fmt.Println("No running Docker containers found.")
		return
	}

	// Choose the original PostgreSQL container
	fmt.Println("Please choose the original PostgreSQL container:")
	for i, name := range containerNames {
		fmt.Printf("[%d] %s\n", i, name)
	}
	fmt.Print("Enter the number of the original container: ")
	originalIndexStr, _ := reader.ReadString('\n')
	originalIndex, err := strconv.Atoi(strings.TrimSpace(originalIndexStr))
	if err != nil {
		fmt.Printf("Invalid input: %v\n", err)
		return
	}
	originalContainer := containerNames[originalIndex]

    // Prefill credentials from container env if possible
    srcEnv := getContainerEnv(originalContainer)
    defaultSrcUser := srcEnv["POSTGRES_USER"]
    defaultSrcPass := srcEnv["POSTGRES_PASSWORD"]
    defaultSrcDB := srcEnv["POSTGRES_DB"]
    if defaultSrcDB == "" {
        defaultSrcDB = "postgres"
    }

    // Get original DB credentials and the database name for the dump
    fmt.Printf("Enter the username for the original DB [%s]: ", defaultSrcUser)
    originalUsername, _ := reader.ReadString('\n')
    originalUsername = strings.TrimSpace(originalUsername)
    if originalUsername == "" {
        originalUsername = defaultSrcUser
    }
    originalPassword := readPasswordWithDefault("Enter the password for the original DB", defaultSrcPass)
    fmt.Printf("Enter the database name for the dump [%s]: ", defaultSrcDB)
    databaseName, _ := reader.ReadString('\n')
    databaseName = strings.TrimSpace(databaseName)
    if databaseName == "" {
        databaseName = defaultSrcDB
    }

	// Check connection to the original DB
	if !checkPgConnection(originalContainer, originalUsername, originalPassword, databaseName) {
		fmt.Println("The credentials for the original database are either wrong or there is some other problem with the database.")
		return
	}

    // Optionally create a new destination container automatically
    fmt.Print("Do you want to automatically create the destination container? (yes/no): ")
    autoCreateStr, _ := reader.ReadString('\n')
    autoCreate := strings.TrimSpace(strings.ToLower(autoCreateStr)) == "yes"
    var newContainer string
    var newUsername, newPassword string
    if autoCreate {
        fmt.Print("Enter the image for the new container [postgres:latest]: ")
        imageStr, _ := reader.ReadString('\n')
        image := strings.TrimSpace(imageStr)
        if image == "" {
            image = "postgres:latest"
        }
        fmt.Print("Enter a name for the new container [pg-new]: ")
        nameStr, _ := reader.ReadString('\n')
        contName := strings.TrimSpace(nameStr)
        if contName == "" {
            contName = "pg-new"
        }
        fmt.Print("Enter a host port to expose [5433]: ")
        portStr, _ := reader.ReadString('\n')
        hostPort := strings.TrimSpace(portStr)
        if hostPort == "" {
            hostPort = "5433"
        }
        fmt.Print("Enter a volume name for data [pgdata_new]: ")
        volStr, _ := reader.ReadString('\n')
        volume := strings.TrimSpace(volStr)
        if volume == "" {
            volume = "pgdata_new"
        }
        // Ask for credentials for the new DB (prefill from src)
        fmt.Printf("Enter the username for the new DB [%s]: ", originalUsername)
        usrStr, _ := reader.ReadString('\n')
        newUsername = strings.TrimSpace(usrStr)
        if newUsername == "" {
            newUsername = originalUsername
        }
        newPassword = readPasswordWithDefault("Enter the password for the new DB", originalPassword)

        fmt.Printf("Creating volume '%s'...\n", volume)
        if err := exec.Command("docker", "volume", "create", volume).Run(); err != nil {
            fmt.Printf("Failed to create volume: %v\n", err)
            return
        }
        fmt.Printf("Starting new container '%s' from image '%s'...\n", contName, image)
        runArgs := []string{
            "run", "-d",
            "--name", contName,
            "-e", fmt.Sprintf("POSTGRES_USER=%s", newUsername),
            "-e", fmt.Sprintf("POSTGRES_PASSWORD=%s", newPassword),
            "-e", fmt.Sprintf("POSTGRES_DB=%s", databaseName),
            "-p", hostPort + ":5432",
            "-v", volume + ":/var/lib/postgresql/data",
            image,
        }
        cmdRun := exec.Command("docker", runArgs...)
        var runStderr bytes.Buffer
        cmdRun.Stderr = &runStderr
        if err := cmdRun.Run(); err != nil {
            fmt.Printf("Failed to start new container: %v - %s\n", err, runStderr.String())
            return
        }
        newContainer = contName
        // Wait until ready
        fmt.Println("Waiting for the new PostgreSQL to be ready...")
        if !waitForPgReady(newContainer, newUsername, newPassword, databaseName, 60*time.Second) {
            fmt.Println("New PostgreSQL container did not become ready in time.")
            return
        }
    } else {
        // Choose the new PostgreSQL container
        fmt.Println("Please choose the new PostgreSQL container:")
        for i, name := range containerNames {
            fmt.Printf("[%d] %s\n", i, name)
        }
        fmt.Print("Enter the number of the new container: ")
        newIndexStr, _ := reader.ReadString('\n')
        newIndex, err := strconv.Atoi(strings.TrimSpace(newIndexStr))
        if err != nil {
            fmt.Printf("Invalid input: %v\n", err)
            return
        }
        newContainer = containerNames[newIndex]

        // Check if we should use the same credentials for the new DB
        fmt.Print("Do you want to use the credentials from the original database for the new container? (yes/no): ")
        useSameCredentialsStr, _ := reader.ReadString('\n')
        useSameCredentials := strings.TrimSpace(strings.ToLower(useSameCredentialsStr)) == "yes"

        if useSameCredentials {
            newUsername = originalUsername
            newPassword = originalPassword
        } else {
            // Prefill from destination env
            dstEnv := getContainerEnv(newContainer)
            defUser := dstEnv["POSTGRES_USER"]
            defPass := dstEnv["POSTGRES_PASSWORD"]
            fmt.Printf("Enter the username for the new DB [%s]: ", defUser)
            usr, _ := reader.ReadString('\n')
            newUsername = strings.TrimSpace(usr)
            if newUsername == "" {
                newUsername = defUser
            }
            newPassword = readPasswordWithDefault("Enter the password for the new DB", defPass)
        }
    }

	// Check connection to the new DB
	if !checkPgConnection(newContainer, newUsername, newPassword, databaseName) {
		fmt.Println("The credentials for the new database are either wrong or there is some other problem with the database.")
		return
	}

    // Migration method: stream (recommended) or file-based
    fmt.Print("Use streaming migration (no temporary file)? (yes/no): ")
    streamStr, _ := reader.ReadString('\n')
    useStream := strings.TrimSpace(strings.ToLower(streamStr)) == "yes"

    if useStream {
        // Optionally migrate global objects (roles, db-level settings)
        fmt.Print("Also migrate global objects (roles)? (yes/no): ")
        globalsStr, _ := reader.ReadString('\n')
        migrateGlobals := strings.TrimSpace(strings.ToLower(globalsStr)) == "yes"
        if migrateGlobals {
            if err := streamGlobals(originalContainer, originalUsername, originalPassword, newContainer, newUsername, newPassword); err != nil {
                fmt.Printf("Error migrating global objects: %v\n", err)
                return
            }
        }

        if err := streamDumpRestore(originalContainer, originalUsername, originalPassword, newContainer, newUsername, newPassword, databaseName); err != nil {
            fmt.Printf("Error migrating database: %v\n", err)
            return
        }
        fmt.Println("Database migration completed successfully.")
        // Optional verification
        runPostMigrationVerification(reader, originalContainer, originalUsername, originalPassword, newContainer, newUsername, newPassword, databaseName)
        return
    }

    // Fallback: file-based dump/restore (plain SQL)
    dumpFileName := databaseName + "_dump.sql"
    fmt.Printf("Running pg_dump on the original container '%s'...\n", originalContainer)
    cmd := exec.Command("docker", "exec", "-e", "PGPASSWORD="+originalPassword, originalContainer, "pg_dump", "-U", originalUsername, "-d", databaseName, "-f", "/"+dumpFileName)
    var stderr bytes.Buffer
    cmd.Stderr = &stderr
    if err := cmd.Run(); err != nil {
        fmt.Printf("Failed to dump the database: %v - %s\n", err, stderr.String())
        return
    }

    // Copy the dump file from the original container to the local filesystem
    fmt.Printf("Copying the dump file from the original container '%s' to the local filesystem...\n", originalContainer)
    localDumpPath := "./" + dumpFileName
    err = exec.Command("docker", "cp", fmt.Sprintf("%s:/%s", originalContainer, dumpFileName), localDumpPath).Run()
    if err != nil {
        fmt.Printf("Error copying the dump file from the original container: %v\n", err)
        return
    }

    // Copy the dump file from the local filesystem to the new container
    fmt.Printf("Copying the dump file to the new container '%s'...\n", newContainer)
    err = exec.Command("docker", "cp", localDumpPath, fmt.Sprintf("%s:/%s", newContainer, dumpFileName)).Run()
    if err != nil {
        fmt.Printf("Error copying the dump file to the new container: %v\n", err)
        return
    }

    // Restore the dump into the new database
    fmt.Printf("Restoring the dump file into the new database on container '%s'...\n", newContainer)
    cmd = exec.Command("docker", "exec", "-e", "PGPASSWORD="+newPassword, newContainer, "psql", "-U", newUsername, "-d", databaseName, "-f", "/"+dumpFileName)
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    if err := cmd.Run(); err != nil {
        fmt.Printf("Error restoring the database: %v\n", err)
        return
    }

    // Cleanup: delete the dump file from the container and the script directory
    fmt.Println("Cleaning up...")
    exec.Command("docker", "exec", originalContainer, "rm", "/"+dumpFileName).Run()
    exec.Command("docker", "exec", newContainer, "rm", "/"+dumpFileName).Run()
    exec.Command("rm", localDumpPath).Run()

    fmt.Println("Database migration completed successfully.")
    // Optional verification
    runPostMigrationVerification(reader, originalContainer, originalUsername, originalPassword, newContainer, newUsername, newPassword, databaseName)
}

func checkPgConnection(containerName, username, password, database string) bool {
	fmt.Printf("Checking PostgreSQL connection for container '%s'...\n", containerName)
    cmd := exec.Command("docker", "exec", "-e", "PGPASSWORD="+password, containerName, "pg_isready", "-U", username, "-d", database)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("Failed to connect to PostgreSQL on container '%s': %v - %s\n", containerName, err, stderr.String())
		return false
	}
	fmt.Printf("PostgreSQL on container '%s' is ready.\n", containerName)
	return true
}

func waitForPgReady(containerName, username, password, database string, timeout time.Duration) bool {
    deadline := time.Now().Add(timeout)
    for {
        if checkPgConnection(containerName, username, password, database) {
            return true
        }
        if time.Now().After(deadline) {
            return false
        }
        time.Sleep(2 * time.Second)
    }
}

func getContainerEnv(containerName string) map[string]string {
    out, err := exec.Command("docker", "inspect", "--format", "{{range .Config.Env}}{{println .}}{{end}}", containerName).Output()
    if err != nil {
        return map[string]string{}
    }
    env := map[string]string{}
    scanner := bufio.NewScanner(bytes.NewReader(out))
    for scanner.Scan() {
        line := scanner.Text()
        if strings.TrimSpace(line) == "" {
            continue
        }
        kv := strings.SplitN(line, "=", 2)
        if len(kv) == 2 {
            env[kv[0]] = kv[1]
        }
    }
    return env
}

func readPasswordWithDefault(prompt string, def string) string {
    if def != "" {
        fmt.Printf("%s [hidden, press Enter to keep existing]: ", prompt)
    } else {
        fmt.Printf("%s: ", prompt)
    }
    // Disable input echo for password
    pwdBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
    fmt.Println("")
    if err != nil {
        // Fallback to visible input
        reader := bufio.NewReader(os.Stdin)
        val, _ := reader.ReadString('\n')
        val = strings.TrimSpace(val)
        if val == "" {
            return def
        }
        return val
    }
    val := strings.TrimSpace(string(pwdBytes))
    if val == "" {
        return def
    }
    return val
}

// shellQuote returns a safely single-quoted string for embedding into shell commands
func shellQuote(s string) string {
    // Replace ' with '\'' pattern
    return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func streamGlobals(srcContainer, srcUser, srcPass, dstContainer, dstUser, dstPass string) error {
    fmt.Println("Migrating global objects (roles)...")
    cmdStr := fmt.Sprintf(
        "docker exec -e PGPASSWORD=%s %s pg_dumpall -U %s --globals-only | docker exec -e PGPASSWORD=%s -i %s psql -U %s -d postgres",
        shellQuote(srcPass), srcContainer, shellQuote(srcUser), shellQuote(dstPass), dstContainer, shellQuote(dstUser),
    )
    return runShellPipeline(cmdStr)
}

func streamDumpRestore(srcContainer, srcUser, srcPass, dstContainer, dstUser, dstPass, dbName string) error {
    fmt.Printf("Streaming dump from '%s' to '%s' for database '%s'...\n", srcContainer, dstContainer, dbName)
    // Use custom format for potential parallelism; pg_restore reads from stdin
    // Note: -j parallelism cannot be used when reading from stdin; keep single-threaded for reliability
    cmdStr := fmt.Sprintf(
        "docker exec -e PGPASSWORD=%s %s pg_dump -U %s -d %s -Fc --no-owner --no-privileges | docker exec -e PGPASSWORD=%s -i %s pg_restore -U %s -d %s --clean --if-exists",
        shellQuote(srcPass), srcContainer, shellQuote(srcUser), shellQuote(dbName), shellQuote(dstPass), dstContainer, shellQuote(dstUser), shellQuote(dbName),
    )
    return runShellPipeline(cmdStr)
}

func runShellPipeline(command string) error {
    fmt.Printf("Running: %s\n", command)
    // Use /bin/sh -c to execute the full pipeline
    cmd := exec.Command("/bin/sh", "-c", command)
    cmd.Stdout = os.Stdout
    var stderr bytes.Buffer
    cmd.Stderr = &stderr
    if err := cmd.Run(); err != nil {
        return fmt.Errorf("pipeline failed: %v - %s", err, stderr.String())
    }
    return nil
}

// ===== Verification helpers =====

func runPostMigrationVerification(reader *bufio.Reader, srcContainer, srcUser, srcPass, dstContainer, dstUser, dstPass, dbName string) {
    fmt.Print("Run post-migration verification? (none/quick/full) [none]: ")
    modeStr, _ := reader.ReadString('\n')
    mode := strings.TrimSpace(strings.ToLower(modeStr))
    if mode == "" {
        mode = "none"
    }
    switch mode {
    case "none":
        return
    case "quick":
        if err := verifySchemaEqual(srcContainer, srcUser, srcPass, dstContainer, dstUser, dstPass, dbName); err != nil {
            fmt.Printf("Verification (schema) failed: %v\n", err)
        } else {
            fmt.Println("Schema verification passed.")
        }
    case "full":
        if err := verifySchemaEqual(srcContainer, srcUser, srcPass, dstContainer, dstUser, dstPass, dbName); err != nil {
            fmt.Printf("Verification (schema) failed: %v\n", err)
            // continue to counts to provide more info
        } else {
            fmt.Println("Schema verification passed.")
        }
        if err := verifyRowCountsEqual(srcContainer, srcUser, srcPass, dstContainer, dstUser, dstPass, dbName); err != nil {
            fmt.Printf("Verification (row counts) failed: %v\n", err)
        } else {
            fmt.Println("Row counts verification passed.")
        }
    default:
        fmt.Println("Unknown verification mode; skipping.")
    }
}

func verifySchemaEqual(srcContainer, srcUser, srcPass, dstContainer, dstUser, dstPass, dbName string) error {
    srcSchema, err := dumpSchema(srcContainer, srcUser, srcPass, dbName)
    if err != nil {
        return fmt.Errorf("src schema dump failed: %w", err)
    }
    dstSchema, err := dumpSchema(dstContainer, dstUser, dstPass, dbName)
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

func dumpSchema(container, user, pass, db string) (string, error) {
    args := []string{"exec", "-e", "PGPASSWORD=" + pass, container, "pg_dump", "-U", user, "-d", db, "-s", "--no-owner", "--no-privileges"}
    cmd := exec.Command("docker", args...)
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
        kept = append(kept, l)
    }
    return strings.Join(kept, "\n")
}

func verifyRowCountsEqual(srcContainer, srcUser, srcPass, dstContainer, dstUser, dstPass, db string) error {
    tables, err := listUserTables(srcContainer, srcUser, srcPass, db)
    if err != nil {
        return fmt.Errorf("listing tables failed: %w", err)
    }
    if len(tables) == 0 {
        return nil
    }
    srcCounts, err := fetchRowCounts(srcContainer, srcUser, srcPass, db, tables)
    if err != nil {
        return fmt.Errorf("source counts failed: %w", err)
    }
    dstCounts, err := fetchRowCounts(dstContainer, dstUser, dstPass, db, tables)
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

func listUserTables(container, user, pass, db string) ([][2]string, error) {
    sql := `SELECT table_schema, table_name
FROM information_schema.tables
WHERE table_type='BASE TABLE'
  AND table_schema NOT IN ('pg_catalog','information_schema')
ORDER BY table_schema, table_name;`
    out, err := runPsql(container, user, pass, db, sql)
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

func fetchRowCounts(container, user, pass, db string, tables [][2]string) (map[string]int64, error) {
    // Build a single UNION ALL query for performance
    var sb strings.Builder
    for i, t := range tables {
        if i > 0 {
            sb.WriteString(" UNION ALL ")
        }
        // SELECT 'schema.table', COUNT(*) FROM schema.table
        tbl := fmt.Sprintf("%s.%s", pqQuoteIdent(t[0]), pqQuoteIdent(t[1]))
        label := fmt.Sprintf("%s.%s", t[0], t[1])
        sb.WriteString(fmt.Sprintf("SELECT '%s', COUNT(*)::bigint FROM %s", label, tbl))
    }
    sb.WriteString(" ORDER BY 1;")
    out, err := runPsql(container, user, pass, db, sb.String())
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

func runPsql(container, user, pass, db, sql string) (string, error) {
    args := []string{"exec", "-e", "PGPASSWORD=" + pass, container, "psql", "-U", user, "-d", db, "-t", "-A", "-F", ",", "-c", sql}
    cmd := exec.Command("docker", args...)
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
