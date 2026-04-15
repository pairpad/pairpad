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
	"nhooyr.io/websocket"
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

		case protocol.TypeFileContent, protocol.TypeFileChanged,
			protocol.TypeFileCreated, protocol.TypeFileDeleted:
			// Relay directly to browsers
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

	sess.addBrowser(conn)
	defer func() {
		sess.removeBrowser(conn)
		s.broadcastParticipantCount(r.Context(), sess)
	}()

	log.Printf("browser joined session %s", sessionID)
	s.broadcastParticipantCount(r.Context(), sess)

	// Send current file tree to the newly connected browser
	if tree := sess.getFileTree(); tree != nil {
		data, err := protocol.Encode(protocol.TypeFileTree, protocol.FileTree{Files: tree})
		if err == nil {
			conn.Write(r.Context(), websocket.MessageText, data)
		}
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
			// Browser wants to open a file — ask daemon for it
			var msg protocol.OpenFile
			if err := protocol.DecodePayload(env, &msg); err != nil {
				continue
			}
			reqData, err := protocol.Encode(protocol.TypeRequestFile, protocol.RequestFile{Path: msg.Path})
			if err == nil {
				sess.daemon.Write(r.Context(), websocket.MessageText, reqData)
			}

		case protocol.TypeSaveFile:
			// Browser wants to save — tell daemon to write
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

		default:
			log.Printf("unhandled browser message: %s", env.Type)
		}
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
		fmt.Fprintf(w, `{"id":%q,"browsers":%d,"files":%d}`, id, len(sess.browsers), len(sess.fileTree))
		sess.mu.RUnlock()
		i++
	}
	fmt.Fprint(w, "]}")
}

func (s *Server) broadcastParticipantCount(ctx context.Context, sess *session) {
	sess.mu.RLock()
	count := len(sess.browsers)
	sess.mu.RUnlock()

	data, err := protocol.Encode(protocol.TypeParticipantInfo, protocol.ParticipantInfo{Count: count})
	if err == nil {
		sess.broadcastToBrowsers(ctx, data)
	}
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
