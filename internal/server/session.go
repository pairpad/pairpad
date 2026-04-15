package server

import (
	"context"
	"sync"

	"github.com/pairpad/pairpad/internal/protocol"
	"nhooyr.io/websocket"
)

// session represents an active pairing session with a connected daemon
// and zero or more browser clients.
type session struct {
	id       string
	mu       sync.RWMutex
	daemon   *websocket.Conn
	browsers map[*websocket.Conn]struct{}
	fileTree []protocol.FileEntry
}

func newSession(id string, daemon *websocket.Conn) *session {
	return &session{
		id:       id,
		daemon:   daemon,
		browsers: make(map[*websocket.Conn]struct{}),
	}
}

func (s *session) addBrowser(conn *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.browsers[conn] = struct{}{}
}

func (s *session) removeBrowser(conn *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.browsers, conn)
}

func (s *session) setFileTree(files []protocol.FileEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fileTree = files
}

func (s *session) getFileTree() []protocol.FileEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fileTree
}

// broadcastToBrowsers sends a message to all connected browser clients.
func (s *session) broadcastToBrowsers(ctx context.Context, data []byte) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for conn := range s.browsers {
		conn.Write(ctx, websocket.MessageText, data)
	}
}
