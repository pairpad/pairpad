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

	"github.com/pairpad/pairpad/internal/protocol"
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
	mu       sync.RWMutex
	sessions map[string]*session
}

// New creates a new Server.
func New(cfg Config) (*Server, error) {
	return &Server{
		cfg:      cfg,
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

	sessionID := generateID()
	sess := newSession(sessionID, conn)

	s.mu.Lock()
	s.sessions[sessionID] = sess
	s.mu.Unlock()

	log.Printf("daemon connected, session %s", sessionID)

	// Send session_ready to daemon with the join URL
	joinURL := fmt.Sprintf("%s/#%s", s.cfg.PublicURL, sessionID)
	readyData, err := protocol.Encode(protocol.TypeSessionReady, protocol.SessionReady{
		SessionID: sessionID,
		JoinURL:   joinURL,
	})
	if err == nil {
		conn.Write(r.Context(), websocket.MessageText, readyData)
	}

	defer func() {
		s.mu.Lock()
		delete(s.sessions, sessionID)
		s.mu.Unlock()
		log.Printf("daemon disconnected, session %s", sessionID)
	}()

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

		case protocol.TypeFileContent:
			// Route to the browser that requested it, not everyone
			var msg protocol.FileContent
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			if requester := sess.resolveFileRequest(msg.Path); requester != nil {
				requester.Write(r.Context(), websocket.MessageText, data)
			}

		case protocol.TypeFileChanged, protocol.TypeFileCreated, protocol.TypeFileDeleted,
			protocol.TypeCommentList, protocol.TypeTourList:
			// Broadcast daemon-initiated changes to all browsers
			sess.broadcastToBrowsers(r.Context(), data)

		case protocol.TypePong:
			// Keepalive response, nothing to do

		default:
			log.Printf("unhandled daemon message: %s", env.Type)
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
				sess.identifyBrowser(conn, msg.Name)
				log.Printf("%s joined session %s (color %s)", msg.Name, sessionID, p.color)
				break
			}
		}
	}

	// Send participant's assigned color back
	colorData, err := protocol.Encode("your_color", struct {
		Color string `json:"color"`
	}{Color: p.color})
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

	// Ask daemon to send comments and tours (daemon broadcasts to all browsers)
	reqComments, err := protocol.Encode(protocol.TypeRequestComments, nil)
	if err == nil {
		sess.daemon.Write(r.Context(), websocket.MessageText, reqComments)
	}
	reqTours, err := protocol.Encode(protocol.TypeRequestTours, nil)
	if err == nil {
		sess.daemon.Write(r.Context(), websocket.MessageText, reqTours)
	}

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
				sess.daemon.Write(r.Context(), websocket.MessageText, reqData)
			}

		case protocol.TypeSaveFile:
			var msg protocol.SaveFile
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			writeData, err := protocol.Encode(protocol.TypeWriteFile, protocol.WriteFile{
				Path:    msg.Path,
				Content: msg.Content,
			})
			if err == nil {
				sess.daemon.Write(r.Context(), websocket.MessageText, writeData)
			}

		case protocol.TypeCursorUpdate:
			var msg protocol.CursorUpdate
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			sess.updateCursor(conn, msg.File, msg.Line, msg.SelectionFrom, msg.SelectionTo)
			s.broadcastCursorState(r.Context(), sess)

		case protocol.TypeCommentAdd:
			// Inject author info and relay to daemon
			var msg protocol.CommentAdd
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			p := sess.getParticipantByConn(conn)
			if p == nil {
				continue
			}
			msg.Author = p.name
			msg.Color = p.color
			relayData, err := protocol.Encode(protocol.TypeCommentAdd, msg)
			if err == nil {
				sess.daemon.Write(r.Context(), websocket.MessageText, relayData)
			}

		case protocol.TypeCommentReply:
			// Inject author info and relay to daemon
			var msg protocol.CommentReply
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			p := sess.getParticipantByConn(conn)
			if p == nil {
				continue
			}
			msg.Author = p.name
			msg.Color = p.color
			relayData, err := protocol.Encode(protocol.TypeCommentReply, msg)
			if err == nil {
				sess.daemon.Write(r.Context(), websocket.MessageText, relayData)
			}

		case protocol.TypeCommentResolve:
			// Relay directly to daemon
			sess.daemon.Write(r.Context(), websocket.MessageText, data)

		case protocol.TypeGuideStart:
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
				sess.broadcastToBrowsers(r.Context(), relayData)
			}

		case protocol.TypeGuideStop:
			sess.broadcastToBrowsers(r.Context(), data)

		case protocol.TypeGuideState:
			sess.broadcastToBrowsers(r.Context(), data)

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
			log.Printf("unhandled browser message: %s", env.Type)
		}
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
