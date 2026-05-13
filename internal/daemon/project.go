package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"strings"
)

// ProjectInfo holds the identity of the project being served.
type ProjectInfo struct {
	ID        string
	Name      string
	RemoteURL string
}

// detectProject determines the project identity from the given directory.
// Uses git remote origin URL if available, falls back to a hash of the
// absolute path.
func DetectProject(dir string) ProjectInfo {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}

	name := filepath.Base(absDir)

	// Try to read git remote origin URL
	remoteURL := gitRemoteURL(absDir)
	if remoteURL != "" {
		return ProjectInfo{
			ID:        hashString(normalizeRemoteURL(remoteURL)),
			Name:      name,
			RemoteURL: remoteURL,
		}
	}

	// Fallback: hash of absolute path
	return ProjectInfo{
		ID:   hashString(absDir),
		Name: name,
	}
}

// gitRemoteURL runs `git remote get-url origin` in the given directory.
func gitRemoteURL(dir string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// normalizeRemoteURL strips .git suffix and protocol differences so that
// https://github.com/foo/bar.git and git@github.com:foo/bar produce the
// same project ID.
func normalizeRemoteURL(url string) string {
	url = strings.TrimSuffix(url, ".git")
	// Convert SSH to HTTPS-like form for consistent hashing
	if strings.HasPrefix(url, "git@") {
		// git@github.com:foo/bar → github.com/foo/bar
		url = strings.TrimPrefix(url, "git@")
		url = strings.Replace(url, ":", "/", 1)
	}
	// Strip protocol
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")
	return strings.ToLower(url)
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:12]) // 24 hex chars, plenty unique
}
