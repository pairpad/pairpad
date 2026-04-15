package daemon

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pairpad/pairpad/internal/protocol"
)

const maxFileSize = 5 * 1024 * 1024 // 5MB

// scanTree walks the project directory and returns a list of file entries,
// respecting ignore patterns and the file size limit.
func scanTree(projectDir string, ignore *ignoreMatcher) ([]protocol.FileEntry, error) {
	var files []protocol.FileEntry

	err := filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}

		rel, err := filepath.Rel(projectDir, path)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}

		if ignore.shouldIgnore(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		if !d.IsDir() && info.Size() > maxFileSize {
			return nil // skip large files
		}

		files = append(files, protocol.FileEntry{
			Path:    rel,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   d.IsDir(),
		})

		return nil
	})

	return files, err
}

// readFile reads a file's contents, returning nil if it's ignored or too large.
func readFile(projectDir, relPath string, ignore *ignoreMatcher) ([]byte, error) {
	if ignore.shouldIgnore(relPath) {
		return nil, fs.ErrPermission
	}

	absPath := filepath.Join(projectDir, relPath)

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxFileSize {
		return nil, fs.ErrPermission
	}

	return os.ReadFile(absPath)
}

// writeFile writes content to a file on disk, creating parent directories
// as needed.
func writeFile(projectDir, relPath string, content []byte, ignore *ignoreMatcher) error {
	if ignore.shouldIgnore(relPath) {
		return fs.ErrPermission
	}

	absPath := filepath.Join(projectDir, relPath)

	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return err
	}

	return os.WriteFile(absPath, content, 0o644)
}
