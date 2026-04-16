package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/pairpad/pairpad/internal/protocol"
)

const (
	pairpadDir   = ".pairpad"
	commentsFile = "comments.json"
	// Number of context lines above and below the anchor
	contextRadius = 2
)

// commentStore manages persistent comment storage in .pairpad/comments.json
type commentStore struct {
	mu         sync.RWMutex
	projectDir string
	comments   []protocol.Comment
}

func newCommentStore(projectDir string) (*commentStore, error) {
	cs := &commentStore{projectDir: projectDir}

	// Ensure .pairpad directory exists
	dir := filepath.Join(projectDir, pairpadDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	// Load existing comments
	if err := cs.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	return cs, nil
}

func (cs *commentStore) load() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	path := filepath.Join(cs.projectDir, pairpadDir, commentsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	return json.Unmarshal(data, &cs.comments)
}

func (cs *commentStore) save() error {
	path := filepath.Join(cs.projectDir, pairpadDir, commentsFile)
	data, err := json.MarshalIndent(cs.comments, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (cs *commentStore) getAll() []protocol.Comment {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	result := make([]protocol.Comment, len(cs.comments))
	copy(result, cs.comments)
	return result
}

func (cs *commentStore) add(c protocol.Comment) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	// Populate anchor text and context from the actual file
	cs.populateAnchor(&c)
	cs.comments = append(cs.comments, c)
	return cs.save()
}

func (cs *commentStore) resolve(id string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	for i := range cs.comments {
		if cs.comments[i].ID == id && cs.comments[i].ParentID == "" {
			cs.comments[i].Resolved = !cs.comments[i].Resolved
			return cs.save()
		}
	}
	return nil
}

func (cs *commentStore) findComment(id string) (protocol.Comment, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	for i := range cs.comments {
		if cs.comments[i].ID == id {
			return cs.comments[i], true
		}
	}
	return protocol.Comment{}, false
}

// populateAnchor reads the file and fills in anchor_text and anchor_context.
// Must be called with cs.mu held.
func (cs *commentStore) populateAnchor(c *protocol.Comment) {
	if c.ParentID != "" {
		return // replies inherit parent's anchor
	}

	lines := readFileLines(cs.projectDir, c.File)
	if lines == nil || c.Line < 1 || c.Line > len(lines) {
		return
	}

	c.AnchorText = lines[c.Line-1]
	c.AnchorContext = getAnchorContext(lines, c.Line-1)

	if c.LineEnd > c.Line && c.LineEnd <= len(lines) {
		c.AnchorTextEnd = lines[c.LineEnd-1]
		c.AnchorContextEnd = getAnchorContext(lines, c.LineEnd-1)
	}
}

// reanchorFile re-anchors all comments for a given file after it changes.
// Returns true if any comment was modified.
func (cs *commentStore) reanchorFile(relPath string) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	lines := readFileLines(cs.projectDir, relPath)
	changed := false

	for i := range cs.comments {
		c := &cs.comments[i]
		if c.File != relPath || c.ParentID != "" || c.AnchorText == "" {
			continue
		}

		// Use 0-based index for the shared anchor functions
		newIdx, orphaned := findAnchorLine(lines, c.Line-1, c.AnchorText, c.AnchorContext)
		newLine := newIdx + 1

		// Re-anchor end line if this is a range comment
		newEndIdx := -1
		endOrphaned := false
		if c.LineEnd > 0 && c.AnchorTextEnd != "" {
			newEndIdx, endOrphaned = findAnchorLine(lines, c.LineEnd-1, c.AnchorTextEnd, c.AnchorContextEnd)
		}

		if (orphaned || endOrphaned) && !c.Orphaned {
			c.Orphaned = true
			changed = true
		} else if !orphaned && !endOrphaned {
			if c.Orphaned {
				c.Orphaned = false
				changed = true
			}
			if c.Line != newLine {
				c.Line = newLine
				c.AnchorContext = getAnchorContext(lines, newIdx)
				changed = true
			}
			if newEndIdx >= 0 {
				newLineEnd := newEndIdx + 1
				if c.LineEnd != newLineEnd {
					c.LineEnd = newLineEnd
					c.AnchorContextEnd = getAnchorContext(lines, newEndIdx)
					changed = true
				}
			}
			// Update replies' line numbers to match parent
			for j := range cs.comments {
				if cs.comments[j].ParentID == c.ID {
					cs.comments[j].Line = c.Line
					cs.comments[j].LineEnd = c.LineEnd
				}
			}
		}
	}

	if changed {
		cs.save()
	}
	return changed
}
