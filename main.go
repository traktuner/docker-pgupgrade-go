package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

var (
	appname = "docker-pgupgrade-go"
	version = "0.1"
	author = "Traktuner"
)

func main() {
	reader := bufio.NewReader(os.Stdin)

	//Output version tag
	fmt.Println("====================================")
    fmt.Printf(" %s\n", appname)
    fmt.Printf(" Version: %s\n", version)
    fmt.Printf(" Developed by: %s\n", author)
    fmt.Println("====================================")

	
	// Query all running Docker containers
	fmt.Println("Querying all running Docker containers...")
	containers, err := exec.Command("docker", "ps", "--format", "{{.Names}}").Output()
	if err != nil {
		fmt.Printf("Error querying Docker containers: %v\n", err)
		return
	}

	containerNames := strings.Split(strings.TrimSpace(string(containers)), "\n")
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

	// Get original DB credentials and the database name for the dump
	fmt.Print("Enter the username for the original DB: ")
	originalUsername, _ := reader.ReadString('\n')
	originalUsername = strings.TrimSpace(originalUsername)
	fmt.Print("Enter the password for the original DB: ")
	originalPassword, _ := reader.ReadString('\n')
	originalPassword = strings.TrimSpace(originalPassword)
	fmt.Print("Enter the database name for the dump: ")
	databaseName, _ := reader.ReadString('\n')
	databaseName = strings.TrimSpace(databaseName)

	// Check connection to the original DB
	if !checkPgConnection(originalContainer, originalUsername, originalPassword, databaseName) {
		fmt.Println("The credentials for the original database are either wrong or there is some other problem with the database.")
		return
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
		return
	}
	newContainer := containerNames[newIndex]

	// Check if we should use the same credentials for the new DB
	var newUsername, newPassword string
	fmt.Print("Do you want to use the credentials from the original database for the new container? (yes/no): ")
	useSameCredentialsStr, _ := reader.ReadString('\n')
	useSameCredentials := strings.TrimSpace(strings.ToLower(useSameCredentialsStr)) == "yes"

	if useSameCredentials {
		newUsername = originalUsername
		newPassword = originalPassword
	} else {
		// Get new DB credentials
		fmt.Print("Enter the username for the new DB: ")
		newUsername, _ = reader.ReadString('\n')
		newUsername = strings.TrimSpace(newUsername)
		fmt.Print("Enter the password for the new DB: ")
		newPassword, _ = reader.ReadString('\n')
		newPassword = strings.TrimSpace(newPassword)
	}

	// Check connection to the new DB
	if !checkPgConnection(newContainer, newUsername, newPassword, databaseName) {
		fmt.Println("The credentials for the new database are either wrong or there is some other problem with the database.")
		return
	}

	// Dump the original database
	dumpFileName := databaseName + "_dump.sql"
	fmt.Printf("Running pg_dump on the original container '%s'...\n", originalContainer)
	cmd := exec.Command("docker", "exec", originalContainer, "pg_dump", "-U", originalUsername, "-d", databaseName, "-f", "/"+dumpFileName)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+originalPassword)
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
	cmd = exec.Command("docker", "exec", newContainer, "psql", "-U", newUsername, "-d", databaseName, "-f", "/"+dumpFileName)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+newPassword)
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
}

func checkPgConnection(containerName, username, password, database string) bool {
	fmt.Printf("Checking PostgreSQL connection for container '%s'...\n", containerName)
	cmd := exec.Command("docker", "exec", containerName, "pg_isready", "-U", username, "-d", database)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+password)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("Failed to connect to PostgreSQL on container '%s': %v - %s\n", containerName, err, stderr.String())
		return false
	}
	fmt.Printf("PostgreSQL on container '%s' is ready.\n", containerName)
	return true
}
