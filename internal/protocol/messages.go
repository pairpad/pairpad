// Package protocol defines the WebSocket message types shared between the
// daemon and server. All messages are JSON-encoded with a "type" field for
// routing.
package protocol

import "time"

// MessageType identifies the kind of WebSocket message.
type MessageType string


const (
	// Daemon → Server
	TypeProjectConnect MessageType = "project_connect"
	TypeFileTree       MessageType = "file_tree"
	TypeFileContent MessageType = "file_content"
	TypeFileChanged MessageType = "file_changed"
	TypeFileCreated MessageType = "file_created"
	TypeFileDeleted MessageType = "file_deleted"

	// Server → Daemon
	TypeSessionReady    MessageType = "session_ready"
	TypeRequestFile MessageType = "request_file"
	TypeWriteFile   MessageType = "write_file"
	TypeDeleteFile  MessageType = "delete_file"

	// Server → Browser (session auth)
	TypePasswordRequired MessageType = "password_required"

	// Browser → Server
	TypeSessionAuth    MessageType = "session_auth"
	TypeIdentify       MessageType = "identify"
	TypeOpenFile       MessageType = "open_file"
	TypeCloseFile      MessageType = "close_file"
	TypeSaveFile       MessageType = "save_file"
	TypeCursorUpdate   MessageType = "cursor_update"
	TypeCommentAdd     MessageType = "comment_add"
	TypeCommentReply   MessageType = "comment_reply"
	TypeCommentResolve MessageType = "comment_resolve"
	TypeCommentDelete  MessageType = "comment_delete"
	TypeTourSave       MessageType = "tour_save"
	TypeTourDelete     MessageType = "tour_delete"
	TypeGuideStart     MessageType = "guide_start"
	TypeGuideStop      MessageType = "guide_stop"
	TypeGuideState     MessageType = "guide_state"
	TypeFollowStatus   MessageType = "follow_status"
	TypeSetRole        MessageType = "set_role"
	TypeRequestRole    MessageType = "request_role"
	TypeReanchor       MessageType = "reanchor"

	// Browser → Server → Daemon
	TypeSearchRequest MessageType = "search_request"

	// Daemon → Server → Browser
	TypeSearchResults MessageType = "search_results"

	// Server → Daemon
	TypeActivity MessageType = "activity"

	// Server → Browser
	TypeSaveRejected    MessageType = "save_rejected"
	TypeDaemonStatus    MessageType = "daemon_status"
	TypeYourColor       MessageType = "your_color"
	TypeParticipantList MessageType = "participant_list"
	TypeCursorState     MessageType = "cursor_state"
	TypeCommentList     MessageType = "comment_list"
	TypeTourList        MessageType = "tour_list"

	// Both directions
	TypePing  MessageType = "ping"
	TypePong  MessageType = "pong"
	TypeError MessageType = "error"
)

// ProjectConnect is sent by the daemon to identify which project it's serving.
type ProjectConnect struct {
	ProjectID    string `json:"project_id"`
	SessionID    string `json:"session_id"`
	HostToken    string `json:"host_token"`
	PasswordHash string `json:"password_hash,omitempty"`
	Name         string `json:"name"`
	RemoteURL    string `json:"remote_url,omitempty"`
}

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
	Path     string `json:"path"`
	Content  []byte `json:"content"`
	BaseHash string `json:"base_hash,omitempty"`
}

// SaveRejected is sent to a browser when a save is rejected due to a
// concurrent modification. Contains the current file content.
type SaveRejected struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}

// SessionReady is sent by the server to the daemon after the session is created.
type SessionReady struct {
	SessionID string `json:"session_id"`
	JoinURL   string `json:"join_url"`
	HostToken string `json:"host_token"`
}

// PasswordRequired is sent by the relay when a session has a password.
type PasswordRequired struct{}

// SessionAuth is sent by the browser with the session password or host token.
type SessionAuth struct {
	Password  string `json:"password,omitempty"`
	HostToken string `json:"host_token,omitempty"`
}

// Identify is sent by the browser immediately after connecting.
type Identify struct {
	Name      string `json:"name"`
	HostToken string `json:"host_token,omitempty"`
}

// Role is a session participant's permission level.
type Role string

const (
	RoleHost      Role = "host"
	RoleEditor    Role = "editor"
	RoleCommenter Role = "commenter"
)

// Participant describes a connected user in a session.
type Participant struct {
	Name  string `json:"name"`
	Color string `json:"color"`
	Role  Role   `json:"role"`
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
	File         string `json:"file"`
	Line         int    `json:"line"`
	LineEnd      int    `json:"line_end,omitempty"`
	Body         string `json:"body"`
	Author       string `json:"author,omitempty"`
	Color        string `json:"color,omitempty"`
	SymbolPath   string `json:"symbol_path,omitempty"`
	SymbolOffset int    `json:"symbol_offset,omitempty"`
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

// CommentDelete is sent by a browser to delete a comment and its replies.
type CommentDelete struct {
	CommentID string `json:"comment_id"`
}

// Comment represents a single comment in a thread.
type Comment struct {
	ID               string   `json:"id"`
	ParentID         string   `json:"parent_id,omitempty"`
	Author           string   `json:"author"`
	Color            string   `json:"color"`
	File             string   `json:"file"`
	Line             int      `json:"line"`
	LineEnd          int      `json:"line_end,omitempty"`
	Body             string   `json:"body"`
	Timestamp        int64    `json:"timestamp"`
	Resolved         bool     `json:"resolved"`
	SymbolPath       string   `json:"symbol_path,omitempty"`
	SymbolOffset     int      `json:"symbol_offset,omitempty"`
	AnchorText       string   `json:"anchor_text,omitempty"`
	AnchorContext    []string `json:"anchor_context,omitempty"`
	AnchorTextEnd    string   `json:"anchor_text_end,omitempty"`
	AnchorContextEnd []string `json:"anchor_context_end,omitempty"`
	Orphaned         bool     `json:"orphaned,omitempty"`
	Stale            bool     `json:"stale,omitempty"`
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
	File             string   `json:"file"`
	Line             int      `json:"line"`
	LineEnd          int      `json:"line_end,omitempty"`
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	SymbolPath       string   `json:"symbol_path,omitempty"`
	SymbolOffset     int      `json:"symbol_offset,omitempty"`
	AnchorText       string   `json:"anchor_text,omitempty"`
	AnchorContext    []string `json:"anchor_context,omitempty"`
	AnchorTextEnd    string   `json:"anchor_text_end,omitempty"`
	AnchorContextEnd []string `json:"anchor_context_end,omitempty"`
	Orphaned         bool     `json:"orphaned,omitempty"`
	Stale            bool     `json:"stale,omitempty"`
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

// SearchRequest is sent by the browser to search project files.
type SearchRequest struct {
	Query string `json:"query"`
}

// SearchMatch is a single search result.
type SearchMatch struct {
	File       string `json:"file"`
	LineNumber int    `json:"line_number"`
	Content    string `json:"content"`
}

// SearchResults is sent back to the browser with matching lines.
type SearchResults struct {
	Matches   []SearchMatch `json:"matches"`
	Truncated bool          `json:"truncated"`
}

// Activity is sent by the relay to the daemon for host-facing logging.
type Activity struct {
	Message string `json:"message"`
}

// DaemonStatus is sent by the relay to browsers when the daemon connects/disconnects.
type DaemonStatus struct {
	Connected bool `json:"connected"`
	Loading   bool `json:"loading,omitempty"`
}

// Reanchor is sent by the browser with corrected annotation positions
// after re-parsing a changed file with the AST.
type Reanchor struct {
	Comments []Comment `json:"comments,omitempty"`
	Tours    []Tour    `json:"tours,omitempty"`
}

// RequestRole is sent by a participant to request a role upgrade.
type RequestRole struct {
	Name string `json:"name"`
	Role Role   `json:"role"`
}

// SetRole is sent by the host to change a participant's role.
type SetRole struct {
	TargetName string `json:"target_name"`
	Role       Role   `json:"role"`
}

// Error carries an error message.
type Error struct {
	Message string `json:"message"`
}
