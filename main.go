package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
)

var (
	appname = "docker-pgupgrade-go"
	version = "0.3"
	author  = "Traktuner"
	// Set via -ldflags in build.sh / CI
	Tag       = ""
	Commit    = ""
	BuildTime = ""
)

func printBanner() {
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
}

func main() {
	printBanner()

	// No arguments: run the original interactive Docker wizard.
	if len(os.Args) == 1 {
		reader := bufio.NewReader(os.Stdin)
		if err := runInteractive(reader); err != nil {
			os.Exit(1)
		}
		return
	}

	// Any argument switches to the non-interactive mode (Kubernetes Job),
	// configured by flags and PG*/PGUPGRADE* environment variables.
	cfg, err := parseNonInteractive(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(2)
	}
	if err := runNonInteractive(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
