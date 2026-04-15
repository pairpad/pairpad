package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/pairpad/pairpad/internal/protocol"
)

const toursFile = "tours.json"

// tourStore manages persistent tour storage in .pairpad/tours.json
type tourStore struct {
	mu         sync.RWMutex
	projectDir string
	tours      []protocol.Tour
}

func newTourStore(projectDir string) (*tourStore, error) {
	ts := &tourStore{projectDir: projectDir}

	if err := ts.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}

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

func (ts *tourStore) getAll() []protocol.Tour {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	result := make([]protocol.Tour, len(ts.tours))
	copy(result, ts.tours)
	return result
}
