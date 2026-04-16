package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

	lines := cs.readFileLines(c.File)
	if lines == nil || c.Line < 1 || c.Line > len(lines) {
		return
	}

	c.AnchorText = lines[c.Line-1]
	c.AnchorContext = cs.getContext(lines, c.Line-1)
}

// reanchorFile re-anchors all comments for a given file after it changes.
// Returns true if any comment was modified.
func (cs *commentStore) reanchorFile(relPath string) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	lines := cs.readFileLines(relPath)
	changed := false

	for i := range cs.comments {
		c := &cs.comments[i]
		if c.File != relPath || c.ParentID != "" || c.AnchorText == "" {
			continue
		}

		newLine, orphaned := cs.findAnchor(lines, c)
		if orphaned && !c.Orphaned {
			c.Orphaned = true
			changed = true
		} else if !orphaned {
			if c.Orphaned {
				c.Orphaned = false
				changed = true
			}
			if c.Line != newLine {
				c.Line = newLine
				c.AnchorContext = cs.getContext(lines, newLine-1)
				changed = true
			}
			// Update replies' line numbers to match parent
			for j := range cs.comments {
				if cs.comments[j].ParentID == c.ID {
					cs.comments[j].Line = c.Line
				}
			}
		}
	}

	if changed {
		cs.save()
	}
	return changed
}
// New stuff...


// findAnchor tries to locate the anchor text in the file.
// Returns (new line number 1-based, orphaned bool).
func (cs *commentStore) findAnchor(lines []string, c *protocol.Comment) (int, bool) {
	if lines == nil {
		return c.Line, true // file doesn't exist or can't be read
	}

	// 1. Check if the anchor text is still at the original line
	if c.Line >= 1 && c.Line <= len(lines) && lines[c.Line-1] == c.AnchorText {
		return c.Line, false
	}

	// 2. Search for exact match anywhere in the file
	for i, line := range lines {
		if line == c.AnchorText {
			return i + 1, false
		}
	}

	// 3. Fuzzy match using context — find best-matching window
	if len(c.AnchorContext) > 0 {
		bestScore := 0
		bestLine := -1

		for i := range lines {
			score := cs.contextMatchScore(lines, i, c.AnchorContext)
			if score > bestScore {
				bestScore = score
				bestLine = i
			}
		}

		// Require at least half the context lines to match
		if bestLine >= 0 && bestScore >= len(c.AnchorContext)/2 {
			return bestLine + 1, false
		}
	}

	// 4. No match found — orphaned
	return c.Line, true
}

// contextMatchScore returns how many context lines match around position idx.
func (cs *commentStore) contextMatchScore(lines []string, idx int, context []string) int {
	score := 0
	contextStart := idx - contextRadius
	for i, ctxLine := range context {
		fileIdx := contextStart + i
		if fileIdx >= 0 && fileIdx < len(lines) {
			if strings.TrimSpace(lines[fileIdx]) == strings.TrimSpace(ctxLine) {
				score++
			}
		}
	}
	return score
}

// getContext returns contextRadius lines above and below (inclusive of the anchor line).
func (cs *commentStore) getContext(lines []string, idx int) []string {
	start := idx - contextRadius
	if start < 0 {
		start = 0
	}
	end := idx + contextRadius + 1
	if end > len(lines) {
		end = len(lines)
	}
	context := make([]string, end-start)
	copy(context, lines[start:end])
	return context
}

// readFileLines reads a file and returns its lines.
func (cs *commentStore) readFileLines(relPath string) []string {
	absPath := filepath.Join(cs.projectDir, relPath)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil
	}
	return strings.Split(string(data), "\n")
}
