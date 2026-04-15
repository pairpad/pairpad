package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/pairpad/pairpad/internal/protocol"
	"nhooyr.io/websocket"
)



// Config holds the daemon configuration.
type Config struct {
	ProjectDir string
	ServerURL  string
}

// Daemon connects the local filesystem to the Pairpad server.
type Daemon struct {
	cfg    Config
	ignore *ignoreMatcher
}

// New creates a new Daemon with the given configuration.
func New(cfg Config) (*Daemon, error) {
	info, err := os.Stat(cfg.ProjectDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("project directory does not exist: %s", cfg.ProjectDir)
	}

	return &Daemon{
		cfg:    cfg,
		ignore: newIgnoreMatcher(cfg.ProjectDir),
	}, nil
}

// Run starts the daemon: connects to the server, sends the file tree,
// watches for local changes, and handles server requests.
func (d *Daemon) Run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, d.cfg.ServerURL+"/ws/daemon", nil)
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}
	defer conn.CloseNow()

	// Send initial file tree
	if err := d.sendFileTree(ctx, conn); err != nil {
		return fmt.Errorf("failed to send file tree: %w", err)
	}

	// Start filesystem watcher
	events, err := startWatcher(d.cfg.ProjectDir, d.ignore)
	if err != nil {
		return fmt.Errorf("failed to start watcher: %w", err)
	}

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
				log.Printf("error handling fs event: %v", err)
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

	case protocol.TypeSessionReady:
		var msg protocol.SessionReady
		if err := protocol.DecodePayload(env, &msg); err != nil {
			return err
		}
		fmt.Printf("\n  Session is ready! Share this link to collaborate:\n\n    %s\n\n", msg.JoinURL)

	case protocol.TypePing:
		return d.send(ctx, conn, protocol.TypePong, nil)

	default:
		log.Printf("unhandled message type: %s", env.Type)
	}

	return nil
}

func (d *Daemon) handleFSEvent(ctx context.Context, conn *websocket.Conn, event watcherEvent) error {
	switch event.Type {
	case protocol.TypeFileCreated, protocol.TypeFileChanged:
		content, err := readFile(d.cfg.ProjectDir, event.RelPath, d.ignore)
		if err != nil {
			return err
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

