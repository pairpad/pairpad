package daemon

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// hardDenyPatterns are never served regardless of configuration.
var hardDenyPatterns = []string{
	".env",
	".ssh",
	".gnupg",
	".git",
	".pairpad",
	"*.pem",
	"*.key",
	"*.tmp",
	"*.temp",
	"*.swp",
	"*.swo",
	"*~",
	"tags",
	"node_modules",
}

// ignoreMatcher determines whether a file path should be excluded.
type ignoreMatcher struct {
	patterns []string
}

// newIgnoreMatcher builds a matcher from .gitignore + .pairpadignore + hard deny list.
func newIgnoreMatcher(projectDir string) *ignoreMatcher {
	m := &ignoreMatcher{
		patterns: append([]string{}, hardDenyPatterns...),
	}
	m.loadFile(filepath.Join(projectDir, ".gitignore"))
	m.loadFile(filepath.Join(projectDir, ".pairpadignore"))
	return m
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
		m.patterns = append(m.patterns, line)
	}
}

// shouldIgnore returns true if the given relative path matches any ignore pattern.
func (m *ignoreMatcher) shouldIgnore(relPath string) bool {
	// Reject path traversal
	if strings.Contains(relPath, "..") {
		return true
	}

	name := filepath.Base(relPath)
	for _, pattern := range m.patterns {
		// Match against the full relative path
		if matched, _ := filepath.Match(pattern, relPath); matched {
			return true
		}
		// Match against just the filename
		if matched, _ := filepath.Match(pattern, name); matched {
			return true
		}
		// Match against each path component (for directory patterns)
		parts := strings.Split(relPath, string(filepath.Separator))
		for _, part := range parts {
			if matched, _ := filepath.Match(pattern, part); matched {
				return true
			}
		}
	}
	return false
}
