package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/pairpad/pairpad/internal/daemon"
	"github.com/pairpad/pairpad/internal/server"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "local":
		cmdLocal()
	case "connect":
		cmdConnect()
	case "relay":
		cmdRelay()
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
  local     Run everything locally (zero-config, opens browser)
  connect   Connect this project to a remote relay server
  relay     Run the relay server (for self-hosting or pairpad.dev)
  login     Authenticate with the Pairpad server
  version   Print version information
`)
}

// cmdConnect runs the daemon only, connecting to a remote relay server.
func cmdConnect() {
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

// cmdRelay runs the relay server only.
func cmdRelay() {
	addr := envOrDefault("PAIRPAD_ADDR", ":8080")
	cfg := server.Config{
		Addr:      addr,
		DBPath:    envOrDefault("DATABASE_PATH", defaultDBPath()),
		PublicURL: envOrDefault("PAIRPAD_PUBLIC_URL", "http://localhost"+addr),
	}

	srv, err := server.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("pairpad server: listening on %s\n", cfg.Addr)
	if err := srv.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// cmdLocal runs both server and daemon in one process. Zero config.
func cmdLocal() {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	addr := envOrDefault("PAIRPAD_ADDR", ":8080")
	publicURL := fmt.Sprintf("http://localhost%s", addr)

	// Start the server in background
	srvCfg := server.Config{
		Addr:      addr,
		DBPath:    envOrDefault("DATABASE_PATH", defaultDBPath()),
		PublicURL: publicURL,
	}

	srv, err := server.New(srvCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- srv.Run()
	}()

	// Give the server a moment to start listening
	time.Sleep(100 * time.Millisecond)

	// Start the daemon connecting to the local server
	daemonCfg := daemon.Config{
		ProjectDir: dir,
		ServerURL:  fmt.Sprintf("ws://localhost%s", addr),
		OnReady: func(joinURL string) {
			openBrowser(joinURL)
		},
	}

	d, err := daemon.New(daemonCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("pairpad local: serving %s on %s\n", dir, publicURL)

	go func() {
		errCh <- d.Run()
	}()

	// Wait for either to fail
	if err := <-errCh; err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func cmdLogin() {
	fmt.Println("pairpad: login not yet implemented")
}

func defaultDBPath() string {
	// Follow XDG on Linux, standard paths on other platforms
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "pairpad.db"
		}
		switch runtime.GOOS {
		case "darwin":
			dataDir = filepath.Join(home, "Library", "Application Support")
		default:
			dataDir = filepath.Join(home, ".local", "share")
		}
	}
	dir := filepath.Join(dataDir, "pairpad")
	os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "pairpad.db")
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	cmd.Start()
}
