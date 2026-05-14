package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	flag "github.com/spf13/pflag"
	"github.com/pairpad/pairpad/internal/daemon"
	"github.com/pairpad/pairpad/internal/importer"
	"github.com/pairpad/pairpad/internal/server"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	os.Args = append(os.Args[:1], os.Args[2:]...)

	switch cmd {
	case "local":
		cmdLocal()
	case "connect":
		cmdConnect()
	case "relay":
		cmdRelay()
	case "import":
		cmdImport()
	case "login":
		cmdLogin()
	case "version", "--version", "-v":
		fmt.Printf("pairpad %s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Pairpad — Annotate your codebase. Walk your team through it.

Usage: pairpad <command> [flags]

Commands:
  connect     Connect this project to app.pairpad.dev (or a self-hosted relay)
  local       Run everything locally — no server needed
  relay       Run a self-hosted relay server
  import      Import tours from a JSON file into a session
  version     Print version information
  help        Show this help

Quick start:
  pairpad connect              Share the current directory via app.pairpad.dev
  pairpad connect --dir ~/src  Share a specific directory
  pairpad local                Try it locally first (opens browser)

Run 'pairpad <command> --help' for details on each command.
`)
}

func cmdLocal() {
	fs := flag.NewFlagSet("local", flag.ExitOnError)
	addr := fs.StringP("addr", "a", envOrDefault("PAIRPAD_ADDR", ":8080"), "Relay listen address")
	dir := fs.StringP("dir", "d", ".", "Project directory")
	newSession := fs.Bool("new", false, "Start a new session (default: continue previous)")
	sessionID := fs.String("session", "", "Resume a specific session ID")
	password := fs.StringP("password", "p", envOrDefault("PAIRPAD_PASSWORD", ""), "Require password to join session (prefer PAIRPAD_PASSWORD env var)")
	noBrowser := fs.Bool("no-browser", false, "Don't open browser automatically")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Run relay + daemon together in one process. Zero configuration needed.
Continues the previous session by default. Use --new for a fresh session.

Usage: pairpad local [flags]

Flags:
`)
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])

	projectDir, err := filepath.Abs(*dir)
	if err != nil {
		fatal("Invalid directory: %v", err)
	}

	publicURL := fmt.Sprintf("http://localhost%s", *addr)

	srvCfg := server.Config{
		Addr:        *addr,
		DBPath:      envOrDefault("DATABASE_PATH", defaultDBPath()),
		PublicURL:   publicURL,
		MaxSessions: 1,
	}

	srv, err := server.New(srvCfg)
	if err != nil {
		fatal("Failed to start relay: %v", err)
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- srv.Run()
	}()

	time.Sleep(100 * time.Millisecond)

	daemonCfg := daemon.Config{
		ProjectDir: projectDir,
		ServerURL:  fmt.Sprintf("ws://localhost%s", *addr),
		NewSession: *newSession,
		SessionID:  *sessionID,
		Password:   *password,
		OnReady: func(joinURL string) {
			if !*noBrowser {
				openBrowser(joinURL)
			}
		},
	}

	d, err := daemon.New(daemonCfg)
	if err != nil {
		fatal("Failed to start daemon: %v", err)
	}

	fmt.Printf("pairpad: serving %s on %s\n", projectDir, publicURL)

	go func() {
		errCh <- d.Run()
	}()

	if err := <-errCh; err != nil {
		fatal("%v", err)
	}
}

func cmdConnect() {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	serverURL := fs.StringP("server", "s", envOrDefault("PAIRPAD_SERVER", "wss://app.pairpad.dev"), "Relay server URL")
	dir := fs.StringP("dir", "d", ".", "Project directory")
	newSession := fs.Bool("new", false, "Start a new session (default: continue previous)")
	sessionID := fs.String("session", "", "Resume a specific session ID")
	password := fs.StringP("password", "p", envOrDefault("PAIRPAD_PASSWORD", ""), "Require password to join session (prefer PAIRPAD_PASSWORD env var)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Connect this project to a remote relay server.
Continues the previous session by default. Use --new for a fresh session.

Usage: pairpad connect [flags]

Flags:
`)
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])

	projectDir, err := filepath.Abs(*dir)
	if err != nil {
		fatal("Invalid directory: %v", err)
	}

	cfg := daemon.Config{
		ProjectDir: projectDir,
		ServerURL:  *serverURL,
		NewSession: *newSession,
		SessionID:  *sessionID,
		Password:   *password,
	}

	d, err := daemon.New(cfg)
	if err != nil {
		fatal("Failed to start daemon: %v", err)
	}

	fmt.Printf("pairpad: connecting %s to %s\n", projectDir, *serverURL)
	if err := d.Run(); err != nil {
		fatal("%v", err)
	}
}

func cmdRelay() {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	addr := fs.StringP("addr", "a", envOrDefault("PAIRPAD_ADDR", ":8080"), "Listen address")
	dbPath := fs.String("db", envOrDefault("DATABASE_PATH", defaultDBPath()), "SQLite database path")
	publicURL := fs.String("public-url", "", "Public URL for session links (default: http://localhost:<port>)")
	maxSessions := fs.Int("max-sessions", 0, "Maximum concurrent sessions (0 = unlimited)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Run the relay server. Browsers and daemons connect to this.

Usage: pairpad relay [flags]

Flags:
`)
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])

	if *publicURL == "" {
		*publicURL = envOrDefault("PAIRPAD_PUBLIC_URL", "http://localhost"+*addr)
	}

	cfg := server.Config{
		Addr:        *addr,
		DBPath:      *dbPath,
		PublicURL:   *publicURL,
		MaxSessions: *maxSessions,
	}

	srv, err := server.New(cfg)
	if err != nil {
		fatal("Failed to start relay: %v", err)
	}

	fmt.Printf("pairpad relay: listening on %s (db: %s)\n", *addr, *dbPath)
	if err := srv.Run(); err != nil {
		fatal("%v", err)
	}
}

func cmdImport() {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	serverURL := fs.StringP("server", "s", envOrDefault("PAIRPAD_SERVER", "wss://app.pairpad.dev"), "Relay server URL")
	dir := fs.StringP("dir", "d", ".", "Project directory")
	sessionID := fs.String("session", "", "Target a specific session ID (default: use saved session)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Import tours from a JSON file into an active session.
Requires a running daemon for the same project.

Usage: pairpad import [flags] <file>

Flags:
`)
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])

	args := fs.Args()
	if len(args) == 0 {
		fs.Usage()
		os.Exit(1)
	}

	cfg := importer.Config{
		ProjectDir: *dir,
		ServerURL:  *serverURL,
		SessionID:  *sessionID,
		FilePath:   args[0],
	}

	fmt.Printf("pairpad: importing tours from %s\n", cfg.FilePath)
	if err := importer.Import(cfg); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("pairpad: import complete\n")
}

func cmdLogin() {
	fmt.Println("pairpad login: not yet implemented (coming soon for hosted service)")
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "pairpad: "+format+"\n", args...)
	os.Exit(1)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func defaultDBPath() string {
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
