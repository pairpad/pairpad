package daemon

import (
	"io/fs"
	"log"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
	"github.com/pairpad/pairpad/internal/protocol"
)

// watcherEvent represents a filesystem change detected by the watcher.
type watcherEvent struct {
	Type    protocol.MessageType
	RelPath string
}

// startWatcher begins watching the project directory for changes and sends
// events on the returned channel. It respects ignore patterns.
func startWatcher(projectDir string, ignore *ignoreMatcher) (<-chan watcherEvent, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	events := make(chan watcherEvent, 64)

	go func() {
		defer watcher.Close()
		defer close(events)

		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}

				rel, err := filepath.Rel(projectDir, event.Name)
				if err != nil || ignore.shouldIgnore(rel) {
					continue
				}

				switch {
				case event.Has(fsnotify.Create):
					events <- watcherEvent{Type: protocol.TypeFileCreated, RelPath: rel}
					// If it's a directory, add it to the watcher
					watcher.Add(event.Name)
				case event.Has(fsnotify.Write):
					events <- watcherEvent{Type: protocol.TypeFileChanged, RelPath: rel}
				case event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename):
					events <- watcherEvent{Type: protocol.TypeFileDeleted, RelPath: rel}
				}

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("watcher error: %v", err)
			}
		}
	}()

	// Add the project root and all non-ignored subdirectories
	if err := addDirs(watcher, projectDir, ignore); err != nil {
		return nil, err
	}

	return events, nil
}

// addDirs recursively adds directories to the watcher, skipping ignored ones.
func addDirs(watcher *fsnotify.Watcher, root string, ignore *ignoreMatcher) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if rel != "." && ignore.shouldIgnore(rel) {
			return filepath.SkipDir
		}
		return watcher.Add(path)
	})
}
