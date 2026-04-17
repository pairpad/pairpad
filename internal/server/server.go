package server

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os/signal"
	"sync"
	"syscall"

	"time"

	"github.com/pairpad/pairpad/internal/anchor"
	"github.com/pairpad/pairpad/internal/protocol"
	"github.com/pairpad/pairpad/internal/storage"
	"github.com/coder/websocket"
)

//go:embed static
var staticFiles embed.FS

// Config holds the server configuration.
type Config struct {
	Addr      string
	DBPath    string
	PublicURL string // e.g. "http://localhost:8080" — used in session_ready join URL
}

// Server is the Pairpad backend that relays messages between daemons and
// browser clients.
type Server struct {
	cfg      Config
	store    *storage.DB
	mu       sync.RWMutex
	sessions map[string]*session
}

// New creates a new Server.
func New(cfg Config) (*Server, error) {
	store, err := storage.Open(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	return &Server{
		cfg:      cfg,
		store:    store,
		sessions: make(map[string]*session),
	}, nil
}

// Run starts the HTTP server.
func (s *Server) Run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", s.handleDaemon)
	mux.HandleFunc("/ws/browser", s.handleBrowser)
	mux.HandleFunc("/api/sessions", s.handleListSessions)

	// Serve embedded frontend static files
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return fmt.Errorf("failed to load static files: %w", err)
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	srv := &http.Server{
		Addr:    s.cfg.Addr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	return srv.ListenAndServe()
}

func (s *Server) handleDaemon(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		log.Printf("daemon websocket accept: %v", err)
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(10 * 1024 * 1024) // 10MB

	// Wait for project_connect from daemon to get session ID
	var msg protocol.ProjectConnect
	for {
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		env, err := protocol.Decode(data)
		if err != nil {
			continue
		}
		if env.Type == protocol.TypeProjectConnect {
			if err := protocol.DecodePayload(env, &msg); err == nil {
				break
			}
		}
	}

	// Use the daemon-provided session ID
	sessionID := msg.SessionID

	// Create or load the project
	if _, err := s.store.GetOrCreateProject(msg.ProjectID, msg.Name, msg.RemoteURL); err != nil {
		log.Printf("failed to load/create project: %v", err)
	}

	// Check if this session already exists (daemon reconnecting)
	s.mu.Lock()
	sess, reconnecting := s.sessions[sessionID]
	if reconnecting {
		// Reconnection: update daemon connection, keep existing state
		sess.mu.Lock()
		sess.daemon = conn
		sess.mu.Unlock()
		log.Printf("daemon reconnected, session %s (project %s)", sessionID, msg.Name)
	} else {
		// New session — use daemon-provided host token
		sess = newSession(sessionID, conn, msg.HostToken)
		sess.projectID = msg.ProjectID
		s.sessions[sessionID] = sess
		log.Printf("daemon connected, session %s (project %s)", sessionID, msg.Name)
	}
	s.mu.Unlock()

	defer func() {
		// Don't delete the session immediately — keep it for 60 seconds
		// in case the daemon reconnects
		go func() {
			time.Sleep(60 * time.Second)
			s.mu.Lock()
			if sess, ok := s.sessions[sessionID]; ok {
				sess.mu.RLock()
				daemonGone := sess.daemon == conn // still our old conn
				sess.mu.RUnlock()
				if daemonGone {
					delete(s.sessions, sessionID)
					log.Printf("session %s expired (daemon did not reconnect)", sessionID)
				}
			}
			s.mu.Unlock()
		}()
		log.Printf("daemon disconnected, session %s (keeping for 60s)", sessionID)
		// Notify browsers that daemon is gone
		if statusData, err := protocol.Encode(protocol.TypeDaemonStatus, protocol.DaemonStatus{Connected: false}); err == nil {
			sess.broadcastToBrowsers(r.Context(), statusData)
		}
	}()

	// Send session_ready to daemon with the join URL and host token
	joinURL := fmt.Sprintf("%s/#%s", s.cfg.PublicURL, sessionID)
	sess.mu.RLock()
	hostToken := sess.hostToken
	sess.mu.RUnlock()
	readyData, err := protocol.Encode(protocol.TypeSessionReady, protocol.SessionReady{
		SessionID: sessionID,
		JoinURL:   joinURL,
		HostToken: hostToken,
	})
	if err == nil {
		conn.Write(r.Context(), websocket.MessageText, readyData)
	}

	// Notify browsers that daemon is reconnecting (file tree coming soon)
	if statusData, err := protocol.Encode(protocol.TypeDaemonStatus, protocol.DaemonStatus{Connected: true, Loading: true}); err == nil {
		sess.broadcastToBrowsers(r.Context(), statusData)
	}

	// Read messages from daemon and relay to browsers
	for {
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}

		env, err := protocol.Decode(data)
		if err != nil {
			log.Printf("invalid daemon message: %v", err)
			continue
		}

		switch env.Type {
		case protocol.TypeFileTree:
			var msg protocol.FileTree
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			sess.setFileTree(msg.Files)
			// Relay to browsers
			sess.broadcastToBrowsers(r.Context(), data)
			// Signal that daemon is fully loaded
			if readyData, err := protocol.Encode(protocol.TypeDaemonStatus, protocol.DaemonStatus{Connected: true}); err == nil {
				sess.broadcastToBrowsers(r.Context(), readyData)
			}

		case protocol.TypeFileContent:
			// Cache file content for anchor operations
			var msg protocol.FileContent
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			sess.cacheFileContent(msg.Path, msg.Content)
			// Route to the browser that requested it, not everyone
			if requester := sess.resolveFileRequest(msg.Path); requester != nil {
				requester.Write(r.Context(), websocket.MessageText, data)
			}

		case protocol.TypeFileChanged, protocol.TypeFileCreated:
			// Cache updated content, re-anchor, and broadcast to browsers
			var msg protocol.FileContent
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			sess.cacheFileContent(msg.Path, msg.Content)
			s.reanchorFile(r.Context(), sess, msg.Path)
			sess.broadcastToBrowsers(r.Context(), data)

		case protocol.TypeFileDeleted,
			protocol.TypeCommentList, protocol.TypeTourList:
			// Broadcast daemon-initiated changes to all browsers
			sess.broadcastToBrowsers(r.Context(), data)

		case protocol.TypePong:
			// Keepalive response, nothing to do

		case protocol.TypeError:
			// Log and ignore — daemon is reporting an error (e.g. can't read a file)
			var msg protocol.Error
			if err := protocol.DecodePayload(env, &msg); err == nil {
				log.Printf("daemon error: %s", msg.Message)
			}

		default:
			log.Printf("unhandled daemon message: %s payload=%s", env.Type, string(env.Payload))
		}
	}
}

func (s *Server) handleBrowser(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		http.Error(w, "missing session parameter", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	sess, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		log.Printf("browser websocket accept: %v", err)
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(10 * 1024 * 1024) // 10MB

	p := sess.addBrowser(conn)
	defer func() {
		sess.removeBrowser(conn)
		s.broadcastParticipants(r.Context(), sess)
		s.broadcastCursorState(r.Context(), sess)
		log.Printf("browser left session %s", sessionID)
	}()

	log.Printf("browser connected to session %s, waiting for identify", sessionID)

	// Wait for the identify message before doing anything else
	for {
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		env, err := protocol.Decode(data)
		if err != nil {
			continue
		}
		if env.Type == protocol.TypeIdentify {
			var msg protocol.Identify
			if err := protocol.DecodePayload(env, &msg); err == nil && msg.Name != "" {
				sess.identifyBrowser(conn, msg.Name, msg.HostToken)
				log.Printf("%s joined session %s (color %s, role %s)", msg.Name, sessionID, p.color, p.role)
				break
			}
		}
	}

	// Send participant's assigned color back
	// Get project name for browser title
	sess.mu.RLock()
	projectName := sess.projectID
	if len(projectName) > 12 {
		projectName = projectName[:12]
	}
	sess.mu.RUnlock()
	if proj, err := s.store.GetProject(sess.projectID); err == nil {
		projectName = proj.Name
	}

	colorData, err := protocol.Encode(protocol.TypeYourColor, struct {
		Color       string `json:"color"`
		ProjectName string `json:"project_name"`
	}{Color: p.color, ProjectName: projectName})
	if err == nil {
		conn.Write(r.Context(), websocket.MessageText, colorData)
	}

	// Broadcast updated participant list
	s.broadcastParticipants(r.Context(), sess)

	// Send current file tree
	if tree := sess.getFileTree(); tree != nil {
		data, err := protocol.Encode(protocol.TypeFileTree, protocol.FileTree{Files: tree})
		if err == nil {
			conn.Write(r.Context(), websocket.MessageText, data)
		}
	}

	// Send stored comments and tours from SQLite
	s.sendAnnotations(r.Context(), conn, sess)

	// If guide mode is active, send guide_start and latest guide_state to the new joiner
	sess.mu.RLock()
	if sess.guideActive {
		startData, err := protocol.Encode(protocol.TypeGuideStart, protocol.GuideStart{
			Name:  sess.guideName,
			Color: sess.guideColor,
		})
		if err == nil {
			conn.Write(r.Context(), websocket.MessageText, startData)
		}
		if sess.guideState != nil {
			conn.Write(r.Context(), websocket.MessageText, sess.guideState)
		}
	}
	sess.mu.RUnlock()

	// Read messages from browser and relay to daemon
	for {
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}

		env, err := protocol.Decode(data)
		if err != nil {
			log.Printf("invalid browser message: %v", err)
			continue
		}

		switch env.Type {
		case protocol.TypeOpenFile:
			var msg protocol.OpenFile
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			sess.trackFileRequest(msg.Path, conn)
			reqData, err := protocol.Encode(protocol.TypeRequestFile, protocol.RequestFile{Path: msg.Path})
			if err == nil {
				sess.writeToDaemon(r.Context(), reqData)
			}

		case protocol.TypeCloseFile:
			// Acknowledged, no action needed — daemon doesn't track open files

		case protocol.TypeSaveFile:
			if !sess.hasRole(conn, protocol.RoleEditor) {
				continue
			}
			var msg protocol.SaveFile
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			writeData, err := protocol.Encode(protocol.TypeWriteFile, protocol.WriteFile{
				Path:    msg.Path,
				Content: msg.Content,
			})
			if err == nil {
				sess.writeToDaemon(r.Context(), writeData)
			}

		case protocol.TypeCursorUpdate:
			var msg protocol.CursorUpdate
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			sess.updateCursor(conn, msg.File, msg.Line, msg.SelectionFrom, msg.SelectionTo)
			s.broadcastCursorState(r.Context(), sess)

		case protocol.TypeCommentAdd:
			if !sess.hasRole(conn, protocol.RoleCommenter) {
				continue
			}
			var msg protocol.CommentAdd
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			p := sess.getParticipantByConn(conn)
			if p == nil {
				continue
			}
			comment := protocol.Comment{
				ID:           generateID(),
				Author:       p.name,
				Color:        p.color,
				File:         msg.File,
				Line:         msg.Line,
				LineEnd:      msg.LineEnd,
				Body:         msg.Body,
				Timestamp:    time.Now().UnixMilli(),
				SymbolPath:   msg.SymbolPath,
				SymbolOffset: msg.SymbolOffset,
			}
			// Populate anchor from file cache
			if lines := sess.getFileLines(msg.File); lines != nil {
				anchor.PopulateComment(&comment, lines)
			}
			if err := s.store.SaveComment(sess.projectID, comment); err != nil {
				log.Printf("failed to save comment: %v", err)
				continue
			}
			s.broadcastComments(r.Context(), sess)

		case protocol.TypeCommentReply:
			if !sess.hasRole(conn, protocol.RoleCommenter) {
				continue
			}
			var msg protocol.CommentReply
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			p := sess.getParticipantByConn(conn)
			if p == nil {
				continue
			}
			// Find parent comment to inherit file/line
			comments, _ := s.store.GetComments(sess.projectID)
			var parent *protocol.Comment
			for i := range comments {
				if comments[i].ID == msg.ParentID {
					parent = &comments[i]
					break
				}
			}
			if parent == nil {
				continue
			}
			reply := protocol.Comment{
				ID:        generateID(),
				ParentID:  msg.ParentID,
				Author:    p.name,
				Color:     p.color,
				File:      parent.File,
				Line:      parent.Line,
				LineEnd:   parent.LineEnd,
				Body:      msg.Body,
				Timestamp: time.Now().UnixMilli(),
			}
			if err := s.store.SaveComment(sess.projectID, reply); err != nil {
				log.Printf("failed to save reply: %v", err)
				continue
			}
			s.broadcastComments(r.Context(), sess)

		case protocol.TypeCommentResolve:
			if !sess.hasRole(conn, protocol.RoleCommenter) {
				continue
			}
			var msg protocol.CommentResolve
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			if err := s.store.ResolveComment(sess.projectID, msg.CommentID); err != nil {
				log.Printf("failed to resolve comment: %v", err)
			}
			s.broadcastComments(r.Context(), sess)

		case protocol.TypeCommentDelete:
			if !sess.hasRole(conn, protocol.RoleCommenter) {
				continue
			}
			var msg protocol.CommentDelete
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			if err := s.store.DeleteComment(sess.projectID, msg.CommentID); err != nil {
				log.Printf("failed to delete comment: %v", err)
			}
			s.broadcastComments(r.Context(), sess)

		case protocol.TypeTourSave:
			if !sess.hasRole(conn, protocol.RoleEditor) {
				continue
			}
			var tour protocol.Tour
			if err := protocol.DecodePayload(env, &tour); err != nil {
				continue
			}
			// Populate anchors from file cache
			for i := range tour.Steps {
				if lines := sess.getFileLines(tour.Steps[i].File); lines != nil {
					anchor.PopulateTourStep(&tour.Steps[i], lines)
				}
			}
			if err := s.store.SaveTour(sess.projectID, tour); err != nil {
				log.Printf("failed to save tour: %v", err)
				continue
			}
			s.broadcastTours(r.Context(), sess)

		case protocol.TypeTourDelete:
			if !sess.hasRole(conn, protocol.RoleEditor) {
				continue
			}
			var msg protocol.TourDelete
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			if err := s.store.DeleteTour(msg.ID); err != nil {
				log.Printf("failed to delete tour: %v", err)
			}
			s.broadcastTours(r.Context(), sess)

		case protocol.TypeGuideStart:
			if !sess.hasRole(conn, protocol.RoleHost) {
				continue
			}
			// Inject guide's name and color, broadcast to all browsers
			var msg protocol.GuideStart
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			p := sess.getParticipantByConn(conn)
			if p == nil {
				continue
			}
			msg.Name = p.name
			msg.Color = p.color
			relayData, err := protocol.Encode(protocol.TypeGuideStart, msg)
			if err == nil {
				sess.mu.Lock()
				sess.guideActive = true
				sess.guideName = p.name
				sess.guideColor = p.color
				sess.mu.Unlock()
				sess.broadcastToBrowsers(r.Context(), relayData)
			}

		case protocol.TypeGuideStop:
			if !sess.hasRole(conn, protocol.RoleHost) {
				continue
			}
			sess.mu.Lock()
			sess.guideActive = false
			sess.guideName = ""
			sess.guideColor = ""
			sess.guideState = nil
			sess.mu.Unlock()
			sess.broadcastToBrowsers(r.Context(), data)

		case protocol.TypeGuideState:
			if !sess.hasRole(conn, protocol.RoleHost) {
				continue
			}
			sess.mu.Lock()
			sess.guideState = data
			sess.mu.Unlock()
			sess.broadcastToBrowsers(r.Context(), data)

		case protocol.TypeReanchor:
			// Browser has re-parsed files and computed corrected positions
			var msg protocol.Reanchor
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			if len(msg.Comments) > 0 {
				if err := s.store.UpdateComments(sess.projectID, msg.Comments); err != nil {
					log.Printf("failed to update reanchored comments: %v", err)
				}
				s.broadcastComments(r.Context(), sess)
			}
			if len(msg.Tours) > 0 {
				if err := s.store.UpdateTours(sess.projectID, msg.Tours); err != nil {
					log.Printf("failed to update reanchored tours: %v", err)
				}
				s.broadcastTours(r.Context(), sess)
			}

		case protocol.TypeSetRole:
			if !sess.isHost(conn) {
				continue
			}
			var msg protocol.SetRole
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			sess.setRole(msg.TargetName, msg.Role)
			s.broadcastParticipants(r.Context(), sess)

		case protocol.TypeFollowStatus:
			// Inject name and broadcast
			var msg protocol.FollowStatus
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			p := sess.getParticipantByConn(conn)
			if p == nil {
				continue
			}
			msg.Name = p.name
			relayData, err := protocol.Encode(protocol.TypeFollowStatus, msg)
			if err == nil {
				sess.broadcastToBrowsers(r.Context(), relayData)
			}

		default:
			log.Printf("unhandled browser message: %s payload=%s", env.Type, string(env.Payload))
		}
	}
}

func (s *Server) sendAnnotations(ctx context.Context, conn *websocket.Conn, sess *session) {
	comments, err := s.store.GetComments(sess.projectID)
	if err == nil {
		data, err := protocol.Encode(protocol.TypeCommentList, protocol.CommentList{Comments: comments})
		if err == nil {
			conn.Write(ctx, websocket.MessageText, data)
		}
	}
	tours, err := s.store.GetTours(sess.projectID)
	if err == nil {
		data, err := protocol.Encode(protocol.TypeTourList, protocol.TourList{Tours: tours})
		if err == nil {
			conn.Write(ctx, websocket.MessageText, data)
		}
	}
}

func (s *Server) broadcastComments(ctx context.Context, sess *session) {
	comments, err := s.store.GetComments(sess.projectID)
	if err != nil {
		return
	}
	data, err := protocol.Encode(protocol.TypeCommentList, protocol.CommentList{Comments: comments})
	if err == nil {
		sess.broadcastToBrowsers(ctx, data)
	}
}

func (s *Server) broadcastTours(ctx context.Context, sess *session) {
	tours, err := s.store.GetTours(sess.projectID)
	if err != nil {
		return
	}
	data, err := protocol.Encode(protocol.TypeTourList, protocol.TourList{Tours: tours})
	if err == nil {
		sess.broadcastToBrowsers(ctx, data)
	}
}

func (s *Server) reanchorFile(ctx context.Context, sess *session, file string) {
	lines := sess.getFileLines(file)
	if lines == nil {
		return
	}

	// Re-anchor comments
	comments, err := s.store.GetComments(sess.projectID)
	if err == nil && anchor.ReanchorComments(comments, file, lines) {
		s.store.UpdateComments(sess.projectID, comments)
		s.broadcastComments(ctx, sess)
	}

	// Re-anchor tours
	tours, err := s.store.GetTours(sess.projectID)
	if err == nil && anchor.ReanchorTourSteps(tours, file, lines) {
		s.store.UpdateTours(sess.projectID, tours)
		s.broadcastTours(ctx, sess)
	}
}

func (s *Server) broadcastCursorState(ctx context.Context, sess *session) {
	cursors := sess.getCursorState()
	data, err := protocol.Encode(protocol.TypeCursorState, protocol.CursorState{
		Cursors: cursors,
	})
	if err == nil {
		sess.broadcastToBrowsers(ctx, data)
	}
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"sessions":[`)
	i := 0
	for id, sess := range s.sessions {
		if i > 0 {
			fmt.Fprint(w, ",")
		}
		sess.mu.RLock()
		fmt.Fprintf(w, `{"id":%q,"participants":%d,"files":%d}`, id, len(sess.participants), len(sess.fileTree))
		sess.mu.RUnlock()
		i++
	}
	fmt.Fprint(w, "]}")
}

func (s *Server) broadcastParticipants(ctx context.Context, sess *session) {
	list := sess.getParticipantList()
	data, err := protocol.Encode(protocol.TypeParticipantList, protocol.ParticipantList{
		Participants: list,
	})
	if err == nil {
		sess.broadcastToBrowsers(ctx, data)
	}
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
