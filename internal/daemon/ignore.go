package daemon

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// hardDenyPatterns are never served regardless of .gitignore configuration.
var hardDenyPatterns = []string{
	// Security — secrets and credentials
	".env",
	".ssh",
	".gnupg",
	"*.pem",
	"*.key",
	"*.p12",
	"*.pfx",
	".npmrc",
	".pypirc",
	".netrc",
	".htpasswd",

	// Version control
	".git",

	// Build artifacts and dependencies
	"bin",
	"node_modules",
	"vendor",
	"__pycache__",

	// Editor and OS temp files
	"*.tmp",
	"*.temp",
	"*.swp",
	"*.swo",
	"*~",
	"tags",
	".DS_Store",
}

// ignoreMatcher determines whether a file path should be excluded.
type ignoreMatcher struct {
	patterns   []string
	projectDir string
	useGit     bool
}

// newIgnoreMatcher builds a matcher from .gitignore + .pairpadignore + hard deny list.
// Uses `git check-ignore` for accurate gitignore evaluation (respects global
// gitignore, negation patterns, and all git ignore semantics).
func newIgnoreMatcher(projectDir string) *ignoreMatcher {
	m := &ignoreMatcher{
		patterns:   append([]string{}, hardDenyPatterns...),
		projectDir: projectDir,
		useGit:     isGitRepo(projectDir),
	}
	// Load project .gitignore as a fallback for non-git projects
	if !m.useGit {
		m.loadFile(filepath.Join(projectDir, ".gitignore"))
	}
	m.loadFile(filepath.Join(projectDir, ".pairpadignore"))
	return m
}

func isGitRepo(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = dir
	return cmd.Run() == nil
}

func (m *ignoreMatcher) loadFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip trailing slash (git uses it to mean "directory only",
		// but our matcher checks all path components regardless)
		line = strings.TrimSuffix(line, "/")
		if line == "" {
			continue
		}
		m.patterns = append(m.patterns, line)
	}
}

// shouldIgnore returns true if the given relative path matches any ignore pattern.
func (m *ignoreMatcher) shouldIgnore(relPath string) bool {
	// Reject path traversal
	if strings.Contains(relPath, "..") {
		return true
	}

	// Check hard deny patterns and .pairpadignore
	name := filepath.Base(relPath)
	for _, pattern := range m.patterns {
		if matched, _ := filepath.Match(pattern, relPath); matched {
			return true
		}
		if matched, _ := filepath.Match(pattern, name); matched {
			return true
		}
		parts := strings.Split(relPath, string(filepath.Separator))
		for _, part := range parts {
			if matched, _ := filepath.Match(pattern, part); matched {
				return true
			}
		}
	}

	// Use git check-ignore for accurate gitignore evaluation
	// (handles global gitignore, negation, anchored patterns, etc.)
	if m.useGit {
		cmd := exec.Command("git", "check-ignore", "-q", relPath)
		cmd.Dir = m.projectDir
		if cmd.Run() == nil {
			return true // git says ignore it
		}
	}

	return false
}
