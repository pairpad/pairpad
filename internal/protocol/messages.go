// Package protocol defines the WebSocket message types shared between the
// daemon and server. All messages are JSON-encoded with a "type" field for
// routing.
package protocol

import "time"

// MessageType identifies the kind of WebSocket message.
type MessageType string

const (
	// Daemon → Server
	TypeFileTree    MessageType = "file_tree"
	TypeFileContent MessageType = "file_content"
	TypeFileChanged MessageType = "file_changed"
	TypeFileCreated MessageType = "file_created"
	TypeFileDeleted MessageType = "file_deleted"

	// Server → Daemon
	TypeSessionReady MessageType = "session_ready"
	TypeRequestFile  MessageType = "request_file"
	TypeWriteFile   MessageType = "write_file"
	TypeDeleteFile  MessageType = "delete_file"
	TypeCreateFile  MessageType = "create_file"

	// Browser → Server
	TypeIdentify     MessageType = "identify"
	TypeOpenFile     MessageType = "open_file"
	TypeCloseFile    MessageType = "close_file"
	TypeSaveFile     MessageType = "save_file"
	TypeCursorUpdate MessageType = "cursor_update"

	// Server → Browser
	TypeParticipantList MessageType = "participant_list"
	TypeCursorState     MessageType = "cursor_state"

	// Both directions
	TypePing  MessageType = "ping"
	TypePong  MessageType = "pong"
	TypeError MessageType = "error"
)

// Envelope wraps every WebSocket message with a type discriminator.
type Envelope struct {
	Type    MessageType `json:"type"`
	Payload []byte      `json:"payload"`
}

// FileEntry represents a single file in the project tree.
type FileEntry struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	IsDir   bool      `json:"is_dir"`
}

// FileTree is sent by the daemon on connect and when the tree changes.
type FileTree struct {
	Files []FileEntry `json:"files"`
}

// FileContent carries a file's contents, used for initial load and updates.
type FileContent struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}

// FileChanged is sent by the daemon when a local file is modified.
type FileChanged struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}

// FileCreated is sent by the daemon when a new file appears locally.
type FileCreated struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}

// FileDeleted is sent by the daemon when a local file is removed.
type FileDeleted struct {
	Path string `json:"path"`
}

// RequestFile is sent by the server to ask the daemon for a file's contents.
type RequestFile struct {
	Path string `json:"path"`
}

// WriteFile is sent by the server to tell the daemon to write content to disk.
type WriteFile struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}

// DeleteFile is sent by the server to tell the daemon to delete a file.
type DeleteFile struct {
	Path string `json:"path"`
}

// CreateFile is sent by the server to tell the daemon to create a new file.
type CreateFile struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}

// OpenFile is sent by a browser client when it opens a file in the editor.
type OpenFile struct {
	Path string `json:"path"`
}

// CloseFile is sent by a browser client when it closes a file tab.
type CloseFile struct {
	Path string `json:"path"`
}

// SaveFile is sent by a browser client to persist the current editor contents.
type SaveFile struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}

// SessionReady is sent by the server to the daemon after the session is created.
type SessionReady struct {
	SessionID string `json:"session_id"`
	JoinURL   string `json:"join_url"`
}

// Identify is sent by the browser immediately after connecting.
type Identify struct {
	Name string `json:"name"`
}

// Participant describes a connected user in a session.
type Participant struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// ParticipantList is broadcast to all browsers when someone joins or leaves.
type ParticipantList struct {
	Participants []Participant `json:"participants"`
}

// CursorUpdate is sent by a browser when its cursor position changes.
type CursorUpdate struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// CursorInfo describes one participant's cursor position.
type CursorInfo struct {
	Name  string `json:"name"`
	Color string `json:"color"`
	File  string `json:"file"`
	Line  int    `json:"line"`
}

// CursorState is broadcast to all browsers with everyone's cursor positions.
type CursorState struct {
	Cursors []CursorInfo `json:"cursors"`
}

// Error carries an error message.
type Error struct {
	Message string `json:"message"`
}
