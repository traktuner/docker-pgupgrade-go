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

// queryRunningPostgresContainers returns the names of running Docker
// containers whose image name contains "postgres".
func queryRunningPostgresContainers() ([]string, error) {
	out, err := exec.Command("docker", "ps", "--format", "{{.Names}}::{{.Image}}").Output()
	if err != nil {
		return nil, fmt.Errorf("Error querying Docker containers: %v", err)
	}
	rawLines := strings.Split(strings.TrimSpace(string(out)), "\n")
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
	return containerNames, nil
}

// runInteractive is the original interactive wizard operating on local
// Docker containers.
func runInteractive(reader *bufio.Reader) error {
	// Query Postgres Docker containers (filter by image name containing 'postgres')
	fmt.Println("Querying all running Docker containers...")
	containerNames, err := queryRunningPostgresContainers()
	if err != nil {
		fmt.Println(err)
		return fmt.Errorf("docker query failed")
	}

	if len(containerNames) == 0 {
		fmt.Println("No running Docker containers found.")
		return fmt.Errorf("no running postgres containers")
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
		return fmt.Errorf("invalid container selection")
	}
	if originalIndex < 0 || originalIndex >= len(containerNames) {
		fmt.Printf("Number %d is out of range (0-%d).\n", originalIndex, len(containerNames)-1)
		return fmt.Errorf("invalid container selection")
	}
	originalContainer := containerNames[originalIndex]

	// Prefill credentials from container env if possible
	srcEnv := getContainerEnv(originalContainer)
	defaultSrcUser := srcEnv["POSTGRES_USER"]
	if defaultSrcUser == "" {
		defaultSrcUser = "postgres"
	}
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
	originalPassword := readPasswordWithDefault(reader, "Enter the password for the original DB", defaultSrcPass)
	fmt.Printf("Enter the database name for the dump [%s]: ", defaultSrcDB)
	databaseName, _ := reader.ReadString('\n')
	databaseName = strings.TrimSpace(databaseName)
	if databaseName == "" {
		databaseName = defaultSrcDB
	}

	srcConn := dockerConn(originalContainer, originalUsername, originalPassword, databaseName)

	// Check connection to the original DB
	if !srcConn.checkConnection() {
		fmt.Println("The credentials for the original database are either wrong or there is some other problem with the database.")
		return fmt.Errorf("cannot reach source database")
	}

	dstConn, err := interactiveDest(reader, containerNames, srcConn, databaseName)
	if err != nil {
		return err
	}

	// Check connection to the new DB
	if !dstConn.checkConnection() {
		fmt.Println("The credentials for the new database are either wrong or there is some other problem with the database.")
		return fmt.Errorf("cannot reach destination database")
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
			if err := streamGlobals(srcConn, dstConn); err != nil {
				fmt.Printf("Error migrating global objects: %v\n", err)
				return fmt.Errorf("global objects migration failed")
			}
		}

		if err := streamDumpRestore(srcConn, dstConn, databaseName); err != nil {
			fmt.Printf("Error migrating database: %v\n", err)
			return fmt.Errorf("migration failed")
		}
		fmt.Println("Database migration completed successfully.")
		// Optional verification
		runPostMigrationVerification(reader, srcConn, dstConn)
		return nil
	}

	if err := fileBasedMigration(originalContainer, dstConn.Container, originalUsername, originalPassword, dstConn.User, dstConn.Password, databaseName); err != nil {
		fmt.Printf("Error: %v\n", err)
		return fmt.Errorf("migration failed")
	}

	fmt.Println("Database migration completed successfully.")
	// Optional verification
	runPostMigrationVerification(reader, srcConn, dstConn)
	return nil
}

// interactiveDest resolves the destination container, either by
// auto-creating it or by asking the user to pick one.
func interactiveDest(reader *bufio.Reader, containerNames []string, srcConn pgConn, databaseName string) (pgConn, error) {
	// Optionally create a new destination container automatically
	fmt.Print("Do you want to automatically create the destination container? (yes/no): ")
	autoCreateStr, _ := reader.ReadString('\n')
	autoCreate := strings.TrimSpace(strings.ToLower(autoCreateStr)) == "yes"
	if autoCreate {
		return interactiveAutoCreateDest(reader, srcConn, databaseName)
	}

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
		return pgConn{}, fmt.Errorf("invalid container selection")
	}
	if newIndex < 0 || newIndex >= len(containerNames) {
		fmt.Printf("Number %d is out of range (0-%d).\n", newIndex, len(containerNames)-1)
		return pgConn{}, fmt.Errorf("invalid container selection")
	}
	newContainer := containerNames[newIndex]

	// Check if we should use the same credentials for the new DB
	fmt.Print("Do you want to use the credentials from the original database for the new container? (yes/no): ")
	useSameCredentialsStr, _ := reader.ReadString('\n')
	useSameCredentials := strings.TrimSpace(strings.ToLower(useSameCredentialsStr)) == "yes"

	if useSameCredentials {
		return dockerConn(newContainer, srcConn.User, srcConn.Password, srcConn.DB), nil
	}

	// Prefill from destination env
	dstEnv := getContainerEnv(newContainer)
	defUser := dstEnv["POSTGRES_USER"]
	if defUser == "" {
		defUser = "postgres"
	}
	defPass := dstEnv["POSTGRES_PASSWORD"]
	fmt.Printf("Enter the username for the new DB [%s]: ", defUser)
	usr, _ := reader.ReadString('\n')
	newUsername := strings.TrimSpace(usr)
	if newUsername == "" {
		newUsername = defUser
	}
	newPassword := readPasswordWithDefault(reader, "Enter the password for the new DB", defPass)
	return dockerConn(newContainer, newUsername, newPassword, srcConn.DB), nil
}

// interactiveAutoCreateDest creates a fresh destination container with
// user supplied parameters and waits for it to become ready.
func interactiveAutoCreateDest(reader *bufio.Reader, srcConn pgConn, databaseName string) (pgConn, error) {
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
	fmt.Printf("Enter the username for the new DB [%s]: ", srcConn.User)
	usrStr, _ := reader.ReadString('\n')
	newUsername := strings.TrimSpace(usrStr)
	if newUsername == "" {
		newUsername = srcConn.User
	}
	newPassword := readPasswordWithDefault(reader, "Enter the password for the new DB", srcConn.Password)

	fmt.Printf("Creating volume '%s'...\n", volume)
	if err := exec.Command("docker", "volume", "create", volume).Run(); err != nil {
		fmt.Printf("Failed to create volume: %v\n", err)
		return pgConn{}, fmt.Errorf("volume creation failed")
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
		return pgConn{}, fmt.Errorf("container start failed")
	}
	dstConn := dockerConn(contName, newUsername, newPassword, databaseName)

	// Wait until ready
	fmt.Println("Waiting for the new PostgreSQL to be ready...")
	if !dstConn.waitForReady(60 * time.Second) {
		fmt.Println("New PostgreSQL container did not become ready in time.")
		return pgConn{}, fmt.Errorf("destination container not ready")
	}
	return dstConn, nil
}

// fileBasedMigration is the docker-only fallback that dumps plain SQL,
// copies the file through the local filesystem, restores it and cleans up.
func fileBasedMigration(originalContainer, newContainer, originalUsername, originalPassword, newUsername, newPassword, databaseName string) error {
	dumpFileName := databaseName + "_dump.sql"
	fmt.Printf("Running pg_dump on the original container '%s'...\n", originalContainer)
	cmd := exec.Command("docker", "exec", "-e", "PGPASSWORD="+originalPassword, originalContainer, "pg_dump", "-U", originalUsername, "-d", databaseName, "-f", "/"+dumpFileName)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Failed to dump the database: %v - %s", err, stderr.String())
	}

	// Copy the dump file from the original container to the local filesystem
	fmt.Printf("Copying the dump file from the original container '%s' to the local filesystem...\n", originalContainer)
	localDumpPath := "./" + dumpFileName
	err := exec.Command("docker", "cp", fmt.Sprintf("%s:/%s", originalContainer, dumpFileName), localDumpPath).Run()
	if err != nil {
		return fmt.Errorf("Error copying the dump file from the original container: %v", err)
	}

	// Copy the dump file from the local filesystem to the new container
	fmt.Printf("Copying the dump file to the new container '%s'...\n", newContainer)
	err = exec.Command("docker", "cp", localDumpPath, fmt.Sprintf("%s:/%s", newContainer, dumpFileName)).Run()
	if err != nil {
		return fmt.Errorf("Error copying the dump file to the new container: %v", err)
	}

	// Restore the dump into the new database
	fmt.Printf("Restoring the dump file into the new database on container '%s'...\n", newContainer)
	cmd = exec.Command("docker", "exec", "-e", "PGPASSWORD="+newPassword, newContainer, "psql", "-U", newUsername, "-d", databaseName, "-f", "/"+dumpFileName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Error restoring the database: %v", err)
	}

	// Cleanup: delete the dump file from the container and the script directory
	fmt.Println("Cleaning up...")
	_ = exec.Command("docker", "exec", originalContainer, "rm", "/"+dumpFileName).Run()
	_ = exec.Command("docker", "exec", newContainer, "rm", "/"+dumpFileName).Run()
	_ = exec.Command("rm", localDumpPath).Run()
	return nil
}

// getContainerEnv reads the environment variables of a Docker container.
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

// readPasswordWithDefault reads a password using the shared buffered
// reader. On a real terminal the echo is disabled while typing; when
// input is not a terminal (for example a pipe) the plain buffered line is
// read instead, which keeps the prompt order intact.
func readPasswordWithDefault(reader *bufio.Reader, prompt string, def string) string {
	if def != "" {
		fmt.Printf("%s [hidden, press Enter to keep existing]: ", prompt)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		pwdBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println("")
		if err == nil {
			val := strings.TrimSpace(string(pwdBytes))
			if val == "" {
				return def
			}
			return val
		}
	}
	// Not a terminal or ReadPassword failed: visible fallback on the shared
	// reader.
	val, _ := reader.ReadString('\n')
	val = strings.TrimSpace(val)
	if val == "" {
		return def
	}
	return val
}
