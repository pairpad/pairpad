package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/pairpad/pairpad/internal/protocol"
)

const toursFile = "tours.json"

// tourStore manages persistent tour storage in .pairpad/tours.json.
// It maintains both the on-disk tours (with authored line numbers) and
// a runtime copy with re-anchored line numbers based on current file content.
type tourStore struct {
	mu         sync.RWMutex
	projectDir string
	tours      []protocol.Tour // on-disk state (written to tours.json)
	runtime    []protocol.Tour // runtime state (re-anchored, never written)
}

func newTourStore(projectDir string) (*tourStore, error) {
	ts := &tourStore{projectDir: projectDir}

	if err := ts.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	// Initialize runtime copy and anchor all steps
	ts.initRuntime()

	return ts, nil
}

func (ts *tourStore) load() error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	path := filepath.Join(ts.projectDir, pairpadDir, toursFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var wrapper struct {
		Tours []protocol.Tour `json:"tours"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return err
	}
	ts.tours = wrapper.Tours
	return nil
}

func (ts *tourStore) save() error {
	path := filepath.Join(ts.projectDir, pairpadDir, toursFile)
	wrapper := struct {
		Tours []protocol.Tour `json:"tours"`
	}{Tours: ts.tours}
	data, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (ts *tourStore) saveTour(tour protocol.Tour) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	// Populate anchor text for each step
	for i := range tour.Steps {
		ts.populateStepAnchor(&tour.Steps[i])
	}

	// Update existing tour or append new one
	found := false
	for i, t := range ts.tours {
		if t.ID == tour.ID {
			ts.tours[i] = tour
			found = true
			break
		}
	}
	if !found {
		ts.tours = append(ts.tours, tour)
	}

	err := ts.save()
	if err == nil {
		ts.rebuildRuntime()
	}
	return err
}

func (ts *tourStore) deleteTour(id string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for i, t := range ts.tours {
		if t.ID == id {
			ts.tours = append(ts.tours[:i], ts.tours[i+1:]...)
			ts.rebuildRuntime()
			return ts.save()
		}
	}
	return nil
}

// getAll returns the runtime (re-anchored) tours for serving to browsers.
func (ts *tourStore) getAll() []protocol.Tour {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	result := make([]protocol.Tour, len(ts.runtime))
	copy(result, ts.runtime)
	return result
}

// reanchorFile re-anchors all tour steps for a given file in the runtime copy.
// Returns true if any step was modified.
func (ts *tourStore) reanchorFile(relPath string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	lines := readFileLines(ts.projectDir, relPath)
	changed := false

	for ti := range ts.runtime {
		for si := range ts.runtime[ti].Steps {
			step := &ts.runtime[ti].Steps[si]
			if step.File != relPath || step.AnchorText == "" {
				continue
			}

			newIdx, orphaned := findAnchorLine(lines, step.Line-1, step.AnchorText, step.AnchorContext)
			newLine := newIdx + 1

			newEndIdx := -1
			endOrphaned := false
			if step.LineEnd > 0 && step.AnchorTextEnd != "" {
				newEndIdx, endOrphaned = findAnchorLine(lines, step.LineEnd-1, step.AnchorTextEnd, step.AnchorContextEnd)
			}

			if (orphaned || endOrphaned) && !step.Orphaned {
				step.Orphaned = true
				changed = true
			} else if !orphaned && !endOrphaned {
				if step.Orphaned {
					step.Orphaned = false
					changed = true
				}
				if step.Line != newLine {
					step.Line = newLine
					step.AnchorContext = getAnchorContext(lines, newIdx)
					changed = true
				}
				if newEndIdx >= 0 {
					newLineEnd := newEndIdx + 1
					if step.LineEnd != newLineEnd {
						step.LineEnd = newLineEnd
						step.AnchorContextEnd = getAnchorContext(lines, newEndIdx)
						changed = true
					}
				}
			}
		}
	}

	return changed
}

// populateStepAnchor fills in anchor text and context for a tour step.
func (ts *tourStore) populateStepAnchor(step *protocol.TourStep) {
	lines := readFileLines(ts.projectDir, step.File)
	if lines == nil || step.Line < 1 || step.Line > len(lines) {
		return
	}
	step.AnchorText = lines[step.Line-1]
	step.AnchorContext = getAnchorContext(lines, step.Line-1)

	if step.LineEnd > step.Line && step.LineEnd <= len(lines) {
		step.AnchorTextEnd = lines[step.LineEnd-1]
		step.AnchorContextEnd = getAnchorContext(lines, step.LineEnd-1)
	}
}

// initRuntime creates the runtime copy and populates anchors for steps
// that don't already have them (e.g. hand-edited tours.json).
func (ts *tourStore) initRuntime() {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	for ti := range ts.tours {
		for si := range ts.tours[ti].Steps {
			step := &ts.tours[ti].Steps[si]
			if step.AnchorText == "" {
				ts.populateStepAnchor(step)
			}
		}
	}

	ts.rebuildRuntime()
}

// rebuildRuntime deep-copies tours to runtime. Must be called with ts.mu held.
func (ts *tourStore) rebuildRuntime() {
	ts.runtime = make([]protocol.Tour, len(ts.tours))
	for i, tour := range ts.tours {
		ts.runtime[i] = protocol.Tour{
			ID:          tour.ID,
			Title:       tour.Title,
			Description: tour.Description,
			Steps:       make([]protocol.TourStep, len(tour.Steps)),
		}
		copy(ts.runtime[i].Steps, tour.Steps)
	}
}
