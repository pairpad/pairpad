package main

import (
	"fmt"
	"os"

	"github.com/pairpad/pairpad/internal/daemon"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		cmdServe()
	case "login":
		cmdLogin()
	case "version":
		fmt.Println("pairpad v0.0.1")
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: pairpad <command>

Commands:
  serve     Start the daemon and expose the current directory
  login     Authenticate with the Pairpad server
  version   Print version information
`)
}

func cmdServe() {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	cfg := daemon.Config{
		ProjectDir: dir,
		ServerURL:  envOrDefault("PAIRPAD_SERVER", "wss://localhost:8080"),
	}

	d, err := daemon.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("pairpad: serving %s\n", dir)
	if err := d.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func cmdLogin() {
	fmt.Println("pairpad: login not yet implemented")
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
