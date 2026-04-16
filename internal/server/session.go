package server

import (
	"context"
	"sync"

	"github.com/pairpad/pairpad/internal/protocol"
	"github.com/coder/websocket"
)

// Colors assigned to participants in join order (Catppuccin Mocha palette).
var participantColors = []string{
	"#89b4fa", // blue
	"#a6e3a1", // green
	"#f9e2af", // yellow
	"#cba6f7", // mauve
	"#fab387", // peach
	"#94e2d5", // teal
	"#f38ba8", // red
	"#74c7ec", // sapphire
	"#f5c2e7", // pink
	"#b4befe", // lavender
}

// participant tracks a connected browser user.
type participant struct {
	conn          *websocket.Conn
	name          string
	color         string
	role          protocol.Role
	cursorFile    string
	cursorLine    int
	selectionFrom int
	selectionTo   int
}

// session represents an active pairing session with a connected daemon
// and zero or more browser clients.
type session struct {
	id           string
	mu           sync.RWMutex
	daemon       *websocket.Conn
	participants map[*websocket.Conn]*participant
	fileTree     []protocol.FileEntry
	colorIndex   int
	hostToken    string
	guideActive  bool
	guideName    string
	guideColor   string
	guideState   []byte // last guide_state message, raw
	// pendingFiles tracks which browser requested which file, so
	// file_content responses are routed only to the requester.
	pendingFiles map[string]*websocket.Conn // path -> requesting conn
}

func newSession(id string, daemon *websocket.Conn, hostToken string) *session {
	return &session{
		id:           id,
		daemon:       daemon,
		hostToken:    hostToken,
		participants:  make(map[*websocket.Conn]*participant),
		pendingFiles: make(map[string]*websocket.Conn),
	}
}

func (s *session) getParticipantByConn(conn *websocket.Conn) *participant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.participants[conn]
}

func (s *session) addBrowser(conn *websocket.Conn) *participant {
	s.mu.Lock()
	defer s.mu.Unlock()
	color := participantColors[s.colorIndex%len(participantColors)]
	s.colorIndex++
	p := &participant{conn: conn, name: "", color: color}
	s.participants[conn] = p
	return p
}

func (s *session) removeBrowser(conn *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.participants, conn)
}

func (s *session) identifyBrowser(conn *websocket.Conn, name string, hostToken string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.participants[conn]; ok {
		p.name = name
		if hostToken != "" && hostToken == s.hostToken {
			p.role = protocol.RoleHost
		} else {
			p.role = protocol.RoleCommenter // default for new joiners
		}
	}
}

func (s *session) setRole(targetName string, role protocol.Role) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.participants {
		if p.name == targetName && p.role != protocol.RoleHost {
			p.role = role
			return
		}
	}
}

func (s *session) isHost(conn *websocket.Conn) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.participants[conn]
	return ok && p.role == protocol.RoleHost
}

func (s *session) hasRole(conn *websocket.Conn, minRole protocol.Role) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.participants[conn]
	if !ok {
		return false
	}
	switch minRole {
	case protocol.RoleCommenter:
		return true // everyone can do commenter-level actions
	case protocol.RoleEditor:
		return p.role == protocol.RoleEditor || p.role == protocol.RoleHost
	case protocol.RoleHost:
		return p.role == protocol.RoleHost
	}
	return false
}

func (s *session) getParticipantList() []protocol.Participant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]protocol.Participant, 0, len(s.participants))
	for _, p := range s.participants {
		if p.name == "" {
			continue // not yet identified
		}
		list = append(list, protocol.Participant{
			Name:  p.name,
			Color: p.color,
			Role:  p.role,
		})
	}
	return list
}

func (s *session) updateCursor(conn *websocket.Conn, file string, line, selFrom, selTo int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.participants[conn]; ok {
		p.cursorFile = file
		p.cursorLine = line
		p.selectionFrom = selFrom
		p.selectionTo = selTo
	}
}

func (s *session) getCursorState() []protocol.CursorInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cursors := make([]protocol.CursorInfo, 0, len(s.participants))
	for _, p := range s.participants {
		if p.name == "" || p.cursorFile == "" {
			continue
		}
		cursors = append(cursors, protocol.CursorInfo{
			Name:          p.name,
			Color:         p.color,
			File:          p.cursorFile,
			Line:          p.cursorLine,
			SelectionFrom: p.selectionFrom,
			SelectionTo:   p.selectionTo,
		})
	}
	return cursors
}

func (s *session) trackFileRequest(path string, conn *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingFiles[path] = conn
}

// resolveFileRequest returns and removes the connection that requested
// a file. Returns nil if no one requested it (e.g. daemon-initiated change).
func (s *session) resolveFileRequest(path string) *websocket.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	conn, ok := s.pendingFiles[path]
	if ok {
		delete(s.pendingFiles, path)
	}
	return conn
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
	conns := make([]*websocket.Conn, 0, len(s.participants))
	for conn := range s.participants {
		conns = append(conns, conn)
	}
	s.mu.RUnlock()
	for _, conn := range conns {
		conn.Write(ctx, websocket.MessageText, data)
	}
}

// writeToDaemon serializes writes to the daemon WebSocket.
func (s *session) writeToDaemon(ctx context.Context, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.daemon != nil {
		s.daemon.Write(ctx, websocket.MessageText, data)
	}
}
