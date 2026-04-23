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
	"net/url"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/pairpad/pairpad/internal/anchor"
	"github.com/pairpad/pairpad/internal/protocol"
	"github.com/pairpad/pairpad/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

//go:embed static
var staticFiles embed.FS

// Config holds the server configuration.
type Config struct {
	Addr        string
	DBPath      string
	PublicURL   string // e.g. "http://localhost:8080" — used in session_ready join URL
	MaxSessions int    // 0 = unlimited
}

// Server is the Pairpad backend that relays messages between daemons and
// browser clients.
type Server struct {
	cfg            Config
	store          *storage.DB
	originPatterns []string
	ipLimit        *ipLimiter
	mu             sync.RWMutex
	sessions       map[string]*session
}

// New creates a new Server.
func New(cfg Config) (*Server, error) {
	store, err := storage.Open(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := store.DeleteStaleSessions(7 * 24 * time.Hour); err != nil {
		log.Printf("failed to clean stale sessions: %v", err)
	}

	return &Server{
		cfg:            cfg,
		store:          store,
		originPatterns: deriveOriginPatterns(cfg.PublicURL),
		ipLimit:        newIPLimiter(10),
		sessions:       make(map[string]*session),
	}, nil
}

// Run starts the HTTP server.
func (s *Server) Run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", s.handleDaemon)
	mux.HandleFunc("/ws/browser", s.handleBrowser)
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
		OriginPatterns: s.originPatterns,
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
		// Verify host token before allowing reconnection
		sess.mu.RLock()
		validToken := sess.hostToken == msg.HostToken
		sess.mu.RUnlock()
		if !validToken {
			s.mu.Unlock()
			conn.Close(websocket.StatusPolicyViolation, "invalid host token")
			return
		}
		sess.mu.Lock()
		sess.daemon = conn
		sess.passwordHash = msg.PasswordHash
		sess.mu.Unlock()
		s.store.TouchSession(sessionID)
		log.Printf("[session=%s] session_resume project=%s", sessionID[:12], msg.Name)
	} else {
		// Not in memory — check max-sessions before creating
		if s.cfg.MaxSessions > 0 && len(s.sessions) >= s.cfg.MaxSessions {
			s.mu.Unlock()
			conn.Close(websocket.StatusPolicyViolation, "relay at capacity — too many active sessions")
			return
		}
		// Check if session exists in SQLite (relay restarted)
		dbSess, _ := s.store.GetSession(sessionID)
		if dbSess != nil && dbSess.HostToken == msg.HostToken {
			sess = newSession(sessionID, conn, dbSess.HostToken)
			sess.projectID = dbSess.ProjectID
			sess.passwordHash = msg.PasswordHash
			s.sessions[sessionID] = sess
			s.store.TouchSession(sessionID)
			log.Printf("[session=%s] session_restore project=%s", sessionID[:12], msg.Name)
		} else {
			sess = newSession(sessionID, conn, msg.HostToken)
			sess.projectID = msg.ProjectID
			sess.passwordHash = msg.PasswordHash
			s.sessions[sessionID] = sess
			s.store.SaveSession(sessionID, msg.ProjectID, msg.HostToken, msg.PasswordHash)
			log.Printf("[session=%s] session_start project=%s", sessionID[:12], msg.Name)
		}
	}
	s.mu.Unlock()

	defer func() {
		// Evict heavy caches immediately on disconnect
		sess.evictCaches()
		log.Printf("[session=%s] daemon_disconnect (grace=60s)", sessionID[:12])
		// Notify browsers that daemon is gone
		if statusData, err := protocol.Encode(protocol.TypeDaemonStatus, protocol.DaemonStatus{Connected: false}); err == nil {
			sess.broadcastToBrowsers(r.Context(), statusData)
		}
		// Delete from memory after 60 seconds, keep in SQLite
		go func() {
			time.Sleep(60 * time.Second)
			s.mu.Lock()
			if sess, ok := s.sessions[sessionID]; ok {
				sess.mu.RLock()
				daemonGone := sess.daemon == conn
				sess.mu.RUnlock()
				if daemonGone {
					sess.closeAllBrowsers()
					delete(s.sessions, sessionID)
					log.Printf("[session=%s] session_expired (persisted in db)", sessionID[:12])
				}
			}
			s.mu.Unlock()
		}()
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
			protocol.TypeCommentList, protocol.TypeTourList,
			protocol.TypeSearchResults:
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
	ip := clientIP(r)
	rateLimited := !s.ipLimit.acquire(ip)

	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		if rateLimited {
			s.ipLimit.release(ip)
		}
		http.Error(w, "missing session parameter", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	sess, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		if rateLimited {
			s.ipLimit.release(ip)
		}
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: s.originPatterns,
	})
	if err != nil {
		if rateLimited {
			s.ipLimit.release(ip)
		}
		log.Printf("browser websocket accept: %v", err)
		return
	}
	defer conn.CloseNow()
	if rateLimited {
		conn.Close(websocket.StatusTryAgainLater, "too many connections from your address")
		return
	}
	defer s.ipLimit.release(ip)
	conn.SetReadLimit(10 * 1024 * 1024) // 10MB

	p := sess.addBrowser(conn)
	defer func() {
		name := p.name
		if name == "" {
			name = "unknown"
		}
		sess.removeBrowser(conn)
		s.broadcastParticipants(r.Context(), sess)
		s.broadcastCursorState(r.Context(), sess)
		s.logActivity(r.Context(), sess,
			fmt.Sprintf("participant_leave name=%s session=%s", name, sessionID),
			fmt.Sprintf("%s left", name))
	}()

	log.Printf("[session=%s] browser_connect", sessionID[:12])

	// If session has a password, require it before identify
	sess.mu.RLock()
	hasPassword := sess.passwordHash != ""
	sess.mu.RUnlock()

	if hasPassword {
		// Send password_required to browser
		if reqData, err := protocol.Encode(protocol.TypePasswordRequired, protocol.PasswordRequired{}); err == nil {
			conn.Write(r.Context(), websocket.MessageText, reqData)
		}

		// Wait for session_auth
		authenticated := false
		attempts := 0
		const maxAttempts = 5
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			env, err := protocol.Decode(data)
			if err != nil {
				continue
			}
			if env.Type == protocol.TypeSessionAuth {
				var msg protocol.SessionAuth
				if err := protocol.DecodePayload(env, &msg); err == nil {
					// Host token bypasses password
					if msg.HostToken != "" && msg.HostToken == sess.hostToken {
						authenticated = true
						break
					}
					sess.mu.RLock()
					expected := sess.passwordHash
					sess.mu.RUnlock()
					if msg.Password != "" && checkPassword(msg.Password, expected) {
						authenticated = true
						break
					}
					attempts++
					if attempts >= maxAttempts {
						if errData, err := protocol.Encode(protocol.TypeError, protocol.Error{Message: "Too many attempts"}); err == nil {
							conn.Write(r.Context(), websocket.MessageText, errData)
						}
						return
					}
					// Exponential backoff: 1s, 2s, 4s, 8s
					time.Sleep(time.Duration(1<<(attempts-1)) * time.Second)
					// Wrong password — send error
					if errData, err := protocol.Encode(protocol.TypeError, protocol.Error{Message: "Wrong password"}); err == nil {
						conn.Write(r.Context(), websocket.MessageText, errData)
					}
				}
			}
		}
		if !authenticated {
			return
		}
	}

	// Wait for the identify message
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
				msg.Name = truncate(msg.Name, maxNameLen)
				sess.identifyBrowser(conn, msg.Name, msg.HostToken)
				s.logActivity(r.Context(), sess,
					fmt.Sprintf("participant_join name=%s role=%s session=%s", msg.Name, p.role, sessionID),
					fmt.Sprintf("%s joined as %s", msg.Name, p.role))
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

	// Rate limit: 50 messages/sec burst, refills at 20/sec
	limiter := newConnLimiter(50, 20)

	// Read messages from browser and relay to daemon
	for {
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}

		if !limiter.allow() {
			continue
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
			msg.Path = truncate(msg.Path, maxPathLen)
			p := sess.getParticipantByConn(conn)
			pName := "unknown"
			if p != nil { pName = p.name }
			sess.trackFileRequest(msg.Path, conn)
			reqData, err := protocol.Encode(protocol.TypeRequestFile, protocol.RequestFile{Path: msg.Path})
			if err == nil {
				sess.writeToDaemon(r.Context(), reqData)
			}
			s.logVerbose(r.Context(), sess,
				fmt.Sprintf("%s opened %s", pName, msg.Path))

		case protocol.TypeCloseFile:
			// Acknowledged, no action needed

		case protocol.TypeSaveFile:
			if !sess.hasRole(conn, protocol.RoleEditor) {
				continue
			}
			var msg protocol.SaveFile
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			msg.Path = truncate(msg.Path, maxPathLen)
			// Optimistic concurrency: reject if file changed since editor loaded it
			if msg.BaseHash != "" {
				currentHash := sess.getFileHash(msg.Path)
				if currentHash != "" && currentHash != msg.BaseHash {
					rejectData, err := protocol.Encode(protocol.TypeSaveRejected, protocol.SaveRejected{
						Path:    msg.Path,
						Content: sess.getCachedContent(msg.Path),
					})
					if err == nil {
						conn.Write(r.Context(), websocket.MessageText, rejectData)
					}
					continue
				}
			}
			p := sess.getParticipantByConn(conn)
			pName := "unknown"
			if p != nil { pName = p.name }
			writeData, err := protocol.Encode(protocol.TypeWriteFile, protocol.WriteFile{
				Path:    msg.Path,
				Content: msg.Content,
			})
			if err == nil {
				sess.writeToDaemon(r.Context(), writeData)
			}
			s.logActivity(r.Context(), sess,
				fmt.Sprintf("file_save name=%s file=%s", pName, msg.Path),
				fmt.Sprintf("%s saved %s", pName, msg.Path))

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
				File:         truncate(msg.File, maxPathLen),
				Line:         msg.Line,
				LineEnd:      msg.LineEnd,
				Body:         truncate(msg.Body, maxBodyLen),
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
			s.logActivity(r.Context(), sess,
				fmt.Sprintf("comment_add author=%s file=%s line=%d", p.name, msg.File, msg.Line),
				fmt.Sprintf("%s commented on %s:%d", p.name, msg.File, msg.Line))

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
				Body:      truncate(msg.Body, maxBodyLen),
				Timestamp: time.Now().UnixMilli(),
			}
			if err := s.store.SaveComment(sess.projectID, reply); err != nil {
				log.Printf("failed to save reply: %v", err)
				continue
			}
			s.broadcastComments(r.Context(), sess)
			s.logActivity(r.Context(), sess,
				fmt.Sprintf("comment_reply author=%s file=%s line=%d", p.name, parent.File, parent.Line),
				fmt.Sprintf("%s replied on %s:%d", p.name, parent.File, parent.Line))

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
			p := sess.getParticipantByConn(conn)
			pName := "unknown"
			if p != nil { pName = p.name }
			s.broadcastComments(r.Context(), sess)
			s.logActivity(r.Context(), sess,
				fmt.Sprintf("comment_toggle_resolve author=%s", pName),
				fmt.Sprintf("%s toggled resolve on a comment", pName))

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
			p := sess.getParticipantByConn(conn)
			pName := "unknown"
			if p != nil { pName = p.name }
			s.broadcastComments(r.Context(), sess)
			s.logActivity(r.Context(), sess,
				fmt.Sprintf("comment_delete author=%s", pName),
				fmt.Sprintf("%s deleted a comment", pName))

		case protocol.TypeTourSave:
			if !sess.hasRole(conn, protocol.RoleEditor) {
				continue
			}
			var tour protocol.Tour
			if err := protocol.DecodePayload(env, &tour); err != nil {
				continue
			}
			tour.Title = truncate(tour.Title, maxTitleLen)
			tour.Description = truncate(tour.Description, maxBodyLen)
			// Populate anchors from file cache
			for i := range tour.Steps {
				tour.Steps[i].Title = truncate(tour.Steps[i].Title, maxTitleLen)
				tour.Steps[i].Description = truncate(tour.Steps[i].Description, maxBodyLen)
				tour.Steps[i].File = truncate(tour.Steps[i].File, maxPathLen)
				if lines := sess.getFileLines(tour.Steps[i].File); lines != nil {
					anchor.PopulateTourStep(&tour.Steps[i], lines)
				}
			}
			if err := s.store.SaveTour(sess.projectID, tour); err != nil {
				log.Printf("failed to save tour: %v", err)
				continue
			}
			p := sess.getParticipantByConn(conn)
			pName := "unknown"
			if p != nil { pName = p.name }
			s.broadcastTours(r.Context(), sess)
			s.logActivity(r.Context(), sess,
				fmt.Sprintf("tour_save author=%s title=%s steps=%d", pName, tour.Title, len(tour.Steps)),
				fmt.Sprintf("%s saved tour \"%s\" (%d steps)", pName, tour.Title, len(tour.Steps)))

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
			p := sess.getParticipantByConn(conn)
			pName := "unknown"
			if p != nil { pName = p.name }
			s.broadcastTours(r.Context(), sess)
			s.logActivity(r.Context(), sess,
				fmt.Sprintf("tour_delete author=%s id=%s", pName, msg.ID),
				fmt.Sprintf("%s deleted a tour", pName))

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
				s.logActivity(r.Context(), sess,
					fmt.Sprintf("guide_start name=%s", p.name),
					fmt.Sprintf("%s started guiding", p.name))
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
			s.logActivity(r.Context(), sess,
				"guide_stop", "Guide mode ended")

		case protocol.TypeGuideState:
			if !sess.hasRole(conn, protocol.RoleHost) {
				continue
			}
			sess.mu.Lock()
			sess.guideState = data
			sess.mu.Unlock()
			sess.broadcastToBrowsers(r.Context(), data)

		case protocol.TypeSearchRequest:
			var msg protocol.SearchRequest
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			msg.Query = truncate(msg.Query, maxPathLen)
			if searchData, err := protocol.Encode(protocol.TypeSearchRequest, msg); err == nil {
				sess.writeToDaemon(r.Context(), searchData)
			}

		case protocol.TypeReanchor:
			// Browser has re-parsed files and computed corrected positions.
			// Only anchor-related fields are accepted; content fields
			// (Author, Body, Timestamp, etc.) are preserved from the DB.
			var msg protocol.Reanchor
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			if len(msg.Comments) > 0 {
				existing, _ := s.store.GetComments(sess.projectID)
				byID := make(map[string]*protocol.Comment, len(existing))
				for i := range existing {
					byID[existing[i].ID] = &existing[i]
				}
				var updated []protocol.Comment
				for _, incoming := range msg.Comments {
					c, ok := byID[incoming.ID]
					if !ok {
						continue
					}
					c.File = incoming.File
					c.Line = incoming.Line
					c.LineEnd = incoming.LineEnd
					c.SymbolPath = incoming.SymbolPath
					c.SymbolOffset = incoming.SymbolOffset
					c.Stale = incoming.Stale
					c.Orphaned = incoming.Orphaned
					if lines := sess.getFileLines(c.File); lines != nil {
						anchor.PopulateComment(c, lines)
					}
					updated = append(updated, *c)
				}
				if len(updated) > 0 {
					if err := s.store.UpdateComments(sess.projectID, updated); err != nil {
						log.Printf("failed to update reanchored comments: %v", err)
					}
					s.broadcastComments(r.Context(), sess)
				}
			}
			if len(msg.Tours) > 0 {
				existing, _ := s.store.GetTours(sess.projectID)
				byID := make(map[string]*protocol.Tour, len(existing))
				for i := range existing {
					byID[existing[i].ID] = &existing[i]
				}
				var updated []protocol.Tour
				for _, incoming := range msg.Tours {
					t, ok := byID[incoming.ID]
					if !ok {
						continue
					}
					for j, step := range incoming.Steps {
						if j >= len(t.Steps) {
							break
						}
						t.Steps[j].File = step.File
						t.Steps[j].Line = step.Line
						t.Steps[j].LineEnd = step.LineEnd
						t.Steps[j].SymbolPath = step.SymbolPath
						t.Steps[j].SymbolOffset = step.SymbolOffset
						t.Steps[j].Stale = step.Stale
						t.Steps[j].Orphaned = step.Orphaned
						if lines := sess.getFileLines(t.Steps[j].File); lines != nil {
							anchor.PopulateTourStep(&t.Steps[j], lines)
						}
					}
					updated = append(updated, *t)
				}
				if len(updated) > 0 {
					if err := s.store.UpdateTours(sess.projectID, updated); err != nil {
						log.Printf("failed to update reanchored tours: %v", err)
					}
					s.broadcastTours(r.Context(), sess)
				}
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
			s.logActivity(r.Context(), sess,
				fmt.Sprintf("role_change target=%s role=%s", msg.TargetName, msg.Role),
				fmt.Sprintf("%s is now %s %s", msg.TargetName, aOrAn(string(msg.Role)), msg.Role))



		case protocol.TypeRequestRole:
			var msg protocol.RequestRole
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			p := sess.getParticipantByConn(conn)
			if p == nil {
				continue
			}
			// For requests: inject sender's name. For denials: keep target name.
			if msg.Name == "" {
				msg.Name = p.name
			}
			relayData, err := protocol.Encode(protocol.TypeRequestRole, msg)
			if err == nil {
				sess.broadcastToBrowsers(r.Context(), relayData)
			}
			if string(msg.Role) != "denied" {
				s.logActivity(r.Context(), sess,
					fmt.Sprintf("role_request name=%s role=%s", msg.Name, msg.Role),
					fmt.Sprintf("%s requested %s access", msg.Name, msg.Role))
			}

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
			action := "unfollowed"
			if msg.Following { action = "is following" }
			s.logVerbose(r.Context(), sess,
				fmt.Sprintf("%s %s the guide", p.name, action))

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

// logActivity logs a structured relay event and sends a human-readable
// message to the daemon for host-facing output.
func (s *Server) logActivity(ctx context.Context, sess *session, relayMsg, daemonMsg string) {
	// Structured relay log (for hosted service analytics)
	log.Printf("[session=%s] %s", sess.id[:12], relayMsg)

	// Forward to daemon for host terminal
	if daemonMsg != "" {
		if data, err := protocol.Encode(protocol.TypeActivity, protocol.Activity{Message: daemonMsg}); err == nil {
			sess.writeToDaemon(ctx, data)
		}
	}
}

// logVerbose logs a debug-level event to the daemon only (not relay logs).
// Used for high-frequency events like file opens and follow status.
func (s *Server) logVerbose(ctx context.Context, sess *session, daemonMsg string) {
	if data, err := protocol.Encode(protocol.TypeActivity, protocol.Activity{Message: daemonMsg}); err == nil {
		sess.writeToDaemon(ctx, data)
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

func (s *Server) broadcastParticipants(ctx context.Context, sess *session) {
	list := sess.getParticipantList()
	data, err := protocol.Encode(protocol.TypeParticipantList, protocol.ParticipantList{
		Participants: list,
	})
	if err == nil {
		sess.broadcastToBrowsers(ctx, data)
	}
}

func checkPassword(password, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

const (
	maxNameLen  = 64
	maxTitleLen = 256
	maxBodyLen  = 10240
	maxPathLen  = 1024
)

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max]
	}
	return s
}

func aOrAn(word string) string {
	if len(word) > 0 && strings.ContainsRune("aeiou", rune(word[0])) {
		return "an"
	}
	return "a"
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := strings.IndexByte(fwd, ','); i > 0 {
			return strings.TrimSpace(fwd[:i])
		}
		return strings.TrimSpace(fwd)
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		return host[:i]
	}
	return host
}

func deriveOriginPatterns(publicURL string) []string {
	u, err := url.Parse(publicURL)
	if err != nil || u.Host == "" {
		return []string{"localhost:*", "127.0.0.1:*", "[::1]:*"}
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return []string{"localhost:*", "127.0.0.1:*", "[::1]:*"}
	}
	return []string{host, "*." + host}
}
