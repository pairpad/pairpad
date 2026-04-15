package main

import (
	"fmt"
	"os"

	"github.com/pairpad/pairpad/internal/server"
)

func main() {
	addr := envOrDefault("PAIRPAD_ADDR", ":8080")
	cfg := server.Config{
		Addr:      addr,
		DBPath:    envOrDefault("DATABASE_PATH", "pairpad.db"),
		PublicURL: envOrDefault("PAIRPAD_PUBLIC_URL", "http://localhost"+addr),
	}

	srv, err := server.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("pairpad-server: listening on %s\n", cfg.Addr)
	if err := srv.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
