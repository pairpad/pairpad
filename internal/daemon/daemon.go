package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pairpad/pairpad/internal/protocol"
	"github.com/coder/websocket"
)

// Config holds the daemon configuration.
type Config struct {
	ProjectDir string
	ServerURL  string
	NewSession bool                 // force a new session (ignore saved session ID)
	SessionID  string               // explicit session ID (overrides saved and new)
	OnReady    func(joinURL string) // called when session is ready (optional)
}

// Daemon connects the local filesystem to the Pairpad server.
type Daemon struct {
	cfg           Config
	ignore        *ignoreMatcher
	project       projectInfo
	sessionID     string
	hostToken     string
	everConnected bool
}

// New creates a new Daemon with the given configuration.
func New(cfg Config) (*Daemon, error) {
	info, err := os.Stat(cfg.ProjectDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("project directory does not exist: %s", cfg.ProjectDir)
	}

	project := detectProject(cfg.ProjectDir)
	var sessionID, hostToken string
	if cfg.SessionID != "" {
		sessionID = cfg.SessionID
		_, hostToken = loadSession(project.ID, false)
	} else {
		sessionID, hostToken = loadSession(project.ID, cfg.NewSession)
	}

	return &Daemon{
		cfg:       cfg,
		ignore:    newIgnoreMatcher(cfg.ProjectDir),
		project:   project,
		sessionID: sessionID,
		hostToken: hostToken,
	}, nil
}

// Run starts the daemon with auto-reconnect. Connects to the relay,
// sends project identity and file tree, and handles messages. On
// disconnect, retries every 2 seconds until the context is cancelled.
func (d *Daemon) Run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Start filesystem watcher once (survives reconnects)
	events, err := startWatcher(d.cfg.ProjectDir, d.ignore)
	if err != nil {
		return fmt.Errorf("failed to start watcher: %w", err)
	}

	d.everConnected = false
	for {
		if ctx.Err() != nil {
			return nil
		}

		err := d.connectAndServe(ctx, events)
		if ctx.Err() != nil {
			return nil // clean shutdown
		}

		if err != nil && !d.everConnected {
			return fmt.Errorf("could not connect to relay at %s — is it running?", d.cfg.ServerURL)
		}

		fmt.Printf("pairpad: lost connection to relay, reconnecting...\n")
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

func (d *Daemon) connectAndServe(ctx context.Context, events <-chan watcherEvent) error {
	conn, _, err := websocket.Dial(ctx, d.cfg.ServerURL+"/ws/daemon", nil)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(10 * 1024 * 1024) // 10MB

	// Identify the project and session
	if err := d.send(ctx, conn, protocol.TypeProjectConnect, protocol.ProjectConnect{
		ProjectID: d.project.ID,
		SessionID: d.sessionID,
		HostToken: d.hostToken,
		Name:      d.project.Name,
		RemoteURL: d.project.RemoteURL,
	}); err != nil {
		return err
	}
	fmt.Printf("pairpad: project %s (session %s)\n", d.project.Name, d.sessionID[:12])

	fmt.Printf("pairpad: loading project...\n")

	// Send initial file tree
	if err := d.sendFileTree(ctx, conn); err != nil {
		return err
	}

	if d.everConnected {
		fmt.Printf("pairpad: reconnected to relay\n")
	} else {
		fmt.Printf("pairpad: ready\n")
	}
	d.everConnected = true

	// Handle incoming messages from server and outgoing FS events
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.readLoop(ctx, conn)
	}()

	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "daemon shutting down")
			return nil

		case event, ok := <-events:
			if !ok {
				return nil
			}
			if err := d.handleFSEvent(ctx, conn, event); err != nil {
				return err // connection likely dead, trigger reconnect
			}

		case err := <-errCh:
			return err
		}
	}
}

func (d *Daemon) sendFileTree(ctx context.Context, conn *websocket.Conn) error {
	files, err := scanTree(d.cfg.ProjectDir, d.ignore)
	if err != nil {
		return err
	}
	return d.send(ctx, conn, protocol.TypeFileTree, protocol.FileTree{Files: files})
}

func (d *Daemon) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}

		env, err := protocol.Decode(data)
		if err != nil {
			log.Printf("invalid message: %v", err)
			continue
		}

		if err := d.handleServerMessage(ctx, conn, env); err != nil {
			log.Printf("error handling message %s: %v", env.Type, err)
		}
	}
}

func (d *Daemon) handleServerMessage(ctx context.Context, conn *websocket.Conn, env protocol.Envelope) error {
	switch env.Type {
	case protocol.TypeRequestFile:
		var msg protocol.RequestFile
		if err := protocol.DecodePayload(env, &msg); err != nil {
			return err
		}
		content, err := readFile(d.cfg.ProjectDir, msg.Path, d.ignore)
		if err != nil {
			return d.send(ctx, conn, protocol.TypeError, protocol.Error{
				Message: fmt.Sprintf("cannot read %s: %v", msg.Path, err),
			})
		}
		return d.send(ctx, conn, protocol.TypeFileContent, protocol.FileContent{
			Path:    msg.Path,
			Content: content,
		})

	case protocol.TypeWriteFile:
		var msg protocol.WriteFile
		if err := protocol.DecodePayload(env, &msg); err != nil {
			return err
		}
		return writeFile(d.cfg.ProjectDir, msg.Path, msg.Content, d.ignore)

	case protocol.TypeDeleteFile:
		var msg protocol.DeleteFile
		if err := protocol.DecodePayload(env, &msg); err != nil {
			return err
		}
		if d.ignore.shouldIgnore(msg.Path) {
			return nil
		}
		return os.Remove(fmt.Sprintf("%s/%s", d.cfg.ProjectDir, msg.Path))

	case protocol.TypeActivity:
		var msg protocol.Activity
		if err := protocol.DecodePayload(env, &msg); err == nil {
			fmt.Printf("  > %s\n", msg.Message)
		}

	case protocol.TypeSessionReady:
		var msg protocol.SessionReady
		if err := protocol.DecodePayload(env, &msg); err != nil {
			return err
		}
		// Print host URL (with token) for the daemon owner
		hostURL := msg.JoinURL + "?host=" + msg.HostToken
		fmt.Printf("\n  Session is ready!\n\n")
		fmt.Printf("  Host (you):    %s\n", hostURL)
		fmt.Printf("  Collaborators: %s\n\n", msg.JoinURL)
		if d.cfg.OnReady != nil {
			d.cfg.OnReady(hostURL)
		}

	case protocol.TypePing:
		return d.send(ctx, conn, protocol.TypePong, nil)

	case protocol.TypeError:
		var msg protocol.Error
		if err := protocol.DecodePayload(env, &msg); err == nil {
			log.Printf("server error: %s", msg.Message)
		}

	default:
		log.Printf("unhandled message: %s payload=%s", env.Type, string(env.Payload))
	}

	return nil
}

func (d *Daemon) handleFSEvent(ctx context.Context, conn *websocket.Conn, event watcherEvent) error {
	switch event.Type {
	case protocol.TypeFileCreated, protocol.TypeFileChanged:
		content, err := readFile(d.cfg.ProjectDir, event.RelPath, d.ignore)
		if err != nil {
			// File may have been deleted between the event and the read
			// (common with temp files from editors and build tools)
			return nil
		}

		return d.send(ctx, conn, event.Type, protocol.FileContent{
			Path:    event.RelPath,
			Content: content,
		})

	case protocol.TypeFileDeleted:
		return d.send(ctx, conn, protocol.TypeFileDeleted, protocol.FileDeleted{
			Path: event.RelPath,
		})
	}

	return nil
}

func (d *Daemon) send(ctx context.Context, conn *websocket.Conn, msgType protocol.MessageType, payload any) error {
	data, err := protocol.Encode(msgType, payload)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}


