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
	TypeSessionReady    MessageType = "session_ready"
	TypeRequestFile     MessageType = "request_file"
	TypeRequestComments MessageType = "request_comments"
	TypeRequestTours    MessageType = "request_tours"
	TypeWriteFile   MessageType = "write_file"
	TypeDeleteFile  MessageType = "delete_file"
	TypeCreateFile  MessageType = "create_file"

	// Browser → Server
	TypeIdentify       MessageType = "identify"
	TypeOpenFile       MessageType = "open_file"
	TypeCloseFile      MessageType = "close_file"
	TypeSaveFile       MessageType = "save_file"
	TypeCursorUpdate   MessageType = "cursor_update"
	TypeCommentAdd     MessageType = "comment_add"
	TypeCommentReply   MessageType = "comment_reply"
	TypeCommentResolve MessageType = "comment_resolve"
	TypeTourSave       MessageType = "tour_save"
	TypeTourDelete     MessageType = "tour_delete"
	TypeGuideStart     MessageType = "guide_start"
	TypeGuideStop      MessageType = "guide_stop"
	TypeGuideState     MessageType = "guide_state"
	TypeFollowStatus   MessageType = "follow_status"

	// Server → Browser (relayed from daemon)
	TypeParticipantList MessageType = "participant_list"
	TypeCursorState     MessageType = "cursor_state"
	TypeCommentList     MessageType = "comment_list"
	TypeTourList        MessageType = "tour_list"

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
	File          string `json:"file"`
	Line          int    `json:"line"`
	SelectionFrom int    `json:"selection_from,omitempty"`
	SelectionTo   int    `json:"selection_to,omitempty"`
}

// CursorInfo describes one participant's cursor position.
type CursorInfo struct {
	Name          string `json:"name"`
	Color         string `json:"color"`
	File          string `json:"file"`
	Line          int    `json:"line"`
	SelectionFrom int    `json:"selection_from,omitempty"`
	SelectionTo   int    `json:"selection_to,omitempty"`
}

// CursorState is broadcast to all browsers with everyone's cursor positions.
type CursorState struct {
	Cursors []CursorInfo `json:"cursors"`
}

// CommentAdd is sent by a browser to create a new comment thread.
// Author and Color are populated by the server before relaying to the daemon.
type CommentAdd struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Body   string `json:"body"`
	Author string `json:"author,omitempty"`
	Color  string `json:"color,omitempty"`
}

// CommentReply is sent by a browser to reply to an existing comment.
// Author and Color are populated by the server before relaying to the daemon.
type CommentReply struct {
	ParentID string `json:"parent_id"`
	Body     string `json:"body"`
	Author   string `json:"author,omitempty"`
	Color    string `json:"color,omitempty"`
}

// CommentResolve is sent by a browser to resolve/unresolve a comment thread.
type CommentResolve struct {
	CommentID string `json:"comment_id"`
}

// Comment represents a single comment in a thread.
type Comment struct {
	ID            string   `json:"id"`
	ParentID      string   `json:"parent_id,omitempty"`
	Author        string   `json:"author"`
	Color         string   `json:"color"`
	File          string   `json:"file"`
	Line          int      `json:"line"`
	Body          string   `json:"body"`
	Timestamp     int64    `json:"timestamp"`
	Resolved      bool     `json:"resolved"`
	AnchorText    string   `json:"anchor_text,omitempty"`
	AnchorContext []string `json:"anchor_context,omitempty"`
	Orphaned      bool     `json:"orphaned,omitempty"`
}

// CommentList is broadcast to all browsers with the full comment state.
type CommentList struct {
	Comments []Comment `json:"comments"`
}

// GuideStart is broadcast when a user begins guiding.
type GuideStart struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// GuideStop is broadcast when the guide stops guiding.
type GuideStop struct{}

// GuideState is broadcast by the guide with their current viewport.
type GuideState struct {
	File          string `json:"file"`
	TopLine       int    `json:"top_line"`
	CursorLine    int    `json:"cursor_line"`
	SelectionFrom int    `json:"selection_from,omitempty"`
	SelectionTo   int    `json:"selection_to,omitempty"`
	TourID        string `json:"tour_id,omitempty"`
	TourStep      int    `json:"tour_step,omitempty"` // -1 or omitted = no tour
}

// TourStep is a single step in a guided tour.
type TourStep struct {
	File        string `json:"file"`
	Line        int    `json:"line"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// Tour is a named, ordered walkthrough of the codebase.
type Tour struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	Steps       []TourStep `json:"steps"`
}

// TourList is sent from daemon to browsers with all available tours.
type TourList struct {
	Tours []Tour `json:"tours"`
}

// TourDelete requests deletion of a tour by ID.
type TourDelete struct {
	ID string `json:"id"`
}

// FollowStatus is broadcast when a user follows/unfollows the guide.
type FollowStatus struct {
	Name      string `json:"name"`
	Following bool   `json:"following"`
}

// Error carries an error message.
type Error struct {
	Message string `json:"message"`
}
