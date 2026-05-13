package daemon

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/pairpad/pairpad/internal/protocol"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/hkdf"
)

// Config holds the daemon configuration.
type Config struct {
	ProjectDir string
	ServerURL  string
	NewSession bool                 // force a new session (ignore saved session ID)
	SessionID  string               // explicit session ID (overrides saved and new)
	Password   string               // session password (empty = no password)
	OnReady    func(joinURL string) // called when session is ready (optional)
}

// Daemon connects the local filesystem to the Pairpad server.
type Daemon struct {
	cfg            Config
	ignore         *ignoreMatcher
	project        ProjectInfo
	sessionID      string
	hostToken      string
	encryptionSeed string
	encKey         []byte
	hmacKey        []byte
	tokenToPath    map[string]string
	everConnected  bool
}

// New creates a new Daemon with the given configuration.
func New(cfg Config) (*Daemon, error) {
	info, err := os.Stat(cfg.ProjectDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("project directory does not exist: %s", cfg.ProjectDir)
	}

	project := DetectProject(cfg.ProjectDir)
	var sessionID, hostToken, encryptionSeed string
	if cfg.SessionID != "" {
		sessionID = cfg.SessionID
		_, hostToken, encryptionSeed = LoadSession(project.ID, false)
	} else {
		sessionID, hostToken, encryptionSeed = LoadSession(project.ID, cfg.NewSession)
	}

	seedBytes, err := base64.RawURLEncoding.DecodeString(encryptionSeed)
	if err != nil {
		return nil, fmt.Errorf("invalid encryption seed: %w", err)
	}
	encKey, err := DeriveKey(seedBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to derive encryption key: %w", err)
	}
	hmacKey, err := DeriveHMACKey(seedBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to derive HMAC key: %w", err)
	}

	return &Daemon{
		cfg:            cfg,
		ignore:         newIgnoreMatcher(cfg.ProjectDir),
		project:        project,
		sessionID:      sessionID,
		hostToken:      hostToken,
		encryptionSeed: encryptionSeed,
		encKey:         encKey,
		hmacKey:        hmacKey,
		tokenToPath:    make(map[string]string),
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

		if err != nil {
			var closeErr websocket.CloseError
			if errors.As(err, &closeErr) && closeErr.Code == websocket.StatusPolicyViolation {
				return fmt.Errorf("relay rejected connection: %s", closeErr.Reason)
			}
			if !d.everConnected {
				return fmt.Errorf("could not connect to relay at %s — is it running?", d.cfg.ServerURL)
			}
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
	// Hash password if set
	var passwordHash string
	if d.cfg.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(d.cfg.Password), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("failed to hash password: %w", err)
		}
		passwordHash = string(hash)
	}

	if err := d.send(ctx, conn, protocol.TypeProjectConnect, protocol.ProjectConnect{
		ProjectID:    d.project.ID,
		SessionID:    d.sessionID,
		HostToken:    d.hostToken,
		PasswordHash: passwordHash,
		Name:         d.project.Name,
		RemoteURL:    d.project.RemoteURL,
	}); err != nil {
		return err
	}
	fmt.Printf("pairpad: project %s (session %s)\n", d.project.Name, d.sessionID)

	// Send initial file tree
	if err := d.sendFileTree(ctx, conn); err != nil {
		return err
	}

	if d.everConnected {
		fmt.Printf("pairpad: reconnected\n")
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
	for i := range files {
		realPath := files[i].Path
		token := PathToken(d.hmacKey, realPath)
		d.tokenToPath[token] = realPath
		encPath, err := EncryptContent(d.encKey, []byte(realPath))
		if err != nil {
			return err
		}
		files[i].Path = token
		files[i].DisplayPath = base64.RawURLEncoding.EncodeToString(encPath)
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
		realPath, ok := d.tokenToPath[msg.Path]
		if !ok {
			return d.send(ctx, conn, protocol.TypeError, protocol.Error{
				Message: "unknown file token",
			})
		}
		content, err := readFile(d.cfg.ProjectDir, realPath, d.ignore)
		if err != nil {
			return d.send(ctx, conn, protocol.TypeError, protocol.Error{
				Message: fmt.Sprintf("cannot read file: %v", err),
			})
		}
		h := sha256.Sum256(content)
		encrypted, err := EncryptContent(d.encKey, content)
		if err != nil {
			return err
		}
		return d.send(ctx, conn, protocol.TypeFileContent, protocol.FileContent{
			Path:        msg.Path,
			Content:     encrypted,
			ContentHash: hex.EncodeToString(h[:]),
		})

	case protocol.TypeWriteFile:
		var msg protocol.WriteFile
		if err := protocol.DecodePayload(env, &msg); err != nil {
			return err
		}
		realPath, ok := d.tokenToPath[msg.Path]
		if !ok {
			return fmt.Errorf("unknown file token for write")
		}
		plaintext, err := DecryptContent(d.encKey, msg.Content)
		if err != nil {
			return err
		}
		return writeFile(d.cfg.ProjectDir, realPath, plaintext, d.ignore)

	case protocol.TypeDeleteFile:
		var msg protocol.DeleteFile
		if err := protocol.DecodePayload(env, &msg); err != nil {
			return err
		}
		realPath, ok := d.tokenToPath[msg.Path]
		if !ok {
			return fmt.Errorf("unknown file token for delete")
		}
		if d.ignore.shouldIgnore(realPath) {
			return nil
		}
		absPath := filepath.Join(d.cfg.ProjectDir, realPath)
		if !isWithinDir(absPath, d.cfg.ProjectDir) {
			return fs.ErrPermission
		}
		return os.Remove(absPath)

	case protocol.TypeSearchRequest:
		var msg protocol.SearchRequest
		if err := protocol.DecodePayload(env, &msg); err != nil {
			return err
		}
		results := d.searchFiles(msg.Query)
		return d.send(ctx, conn, protocol.TypeSearchResults, results)

	case protocol.TypeActivity:
		var msg protocol.Activity
		if err := protocol.DecodePayload(env, &msg); err == nil {
			parts := strings.SplitN(msg.Message, "\t", 2)
			display := parts[0]
			if len(parts) == 2 && strings.HasPrefix(parts[1], "file:") {
				token := strings.TrimPrefix(parts[1], "file:")
				if path, ok := d.tokenToPath[token]; ok {
					display = strings.Replace(display, "a file", path, 1)
				}
			}
			fmt.Printf("  > %s\n", display)
		}

	case protocol.TypeSessionReady:
		var msg protocol.SessionReady
		if err := protocol.DecodePayload(env, &msg); err != nil {
			return err
		}
		// Append encryption seed to URL fragment
		joinURL := msg.JoinURL
		if d.encryptionSeed != "" {
			joinURL = joinURL + "," + d.encryptionSeed
		}
		// Print host URL (with token) for the daemon owner
		hostURL := joinURL + "?host=" + msg.HostToken
		fmt.Printf("\n  Session is ready!\n\n")
		fmt.Printf("  Host (you):    %s\n", hostURL)
		fmt.Printf("  Collaborators: %s\n\n", joinURL)
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
	token := PathToken(d.hmacKey, event.RelPath)

	switch event.Type {
	case protocol.TypeFileCreated:
		content, err := readFile(d.cfg.ProjectDir, event.RelPath, d.ignore)
		if err != nil {
			return nil
		}
		h := sha256.Sum256(content)
		encrypted, err := EncryptContent(d.encKey, content)
		if err != nil {
			return err
		}
		d.tokenToPath[token] = event.RelPath
		encPath, err := EncryptContent(d.encKey, []byte(event.RelPath))
		if err != nil {
			return err
		}
		return d.send(ctx, conn, protocol.TypeFileCreated, protocol.FileCreated{
			Path:        token,
			Content:     encrypted,
			ContentHash: hex.EncodeToString(h[:]),
			DisplayPath: base64.RawURLEncoding.EncodeToString(encPath),
		})

	case protocol.TypeFileChanged:
		content, err := readFile(d.cfg.ProjectDir, event.RelPath, d.ignore)
		if err != nil {
			return nil
		}
		h := sha256.Sum256(content)
		encrypted, err := EncryptContent(d.encKey, content)
		if err != nil {
			return err
		}
		d.tokenToPath[token] = event.RelPath
		return d.send(ctx, conn, protocol.TypeFileChanged, protocol.FileContent{
			Path:        token,
			Content:     encrypted,
			ContentHash: hex.EncodeToString(h[:]),
		})

	case protocol.TypeFileDeleted:
		return d.send(ctx, conn, protocol.TypeFileDeleted, protocol.FileDeleted{
			Path: token,
		})
	}

	return nil
}

const maxSearchResults = 100

func (d *Daemon) searchFiles(query string) protocol.SearchResults {
	if query == "" {
		return protocol.SearchResults{}
	}

	var matches []protocol.SearchMatch
	truncated := false
	lowerQuery := strings.ToLower(query)

	filepath.WalkDir(d.cfg.ProjectDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			rel, _ := filepath.Rel(d.cfg.ProjectDir, path)
			if entry != nil && entry.IsDir() && rel != "." && d.ignore.shouldIgnore(rel) {
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(d.cfg.ProjectDir, path)
		if err != nil || d.ignore.shouldIgnore(rel) {
			return nil
		}

		info, err := entry.Info()
		if err != nil || info.Size() > maxFileSize {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		token := PathToken(d.hmacKey, rel)
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if strings.Contains(strings.ToLower(line), lowerQuery) {
				encPath, _ := EncryptContent(d.encKey, []byte(rel))
				encLine, _ := EncryptContent(d.encKey, []byte(strings.TrimSpace(line)))
				matches = append(matches, protocol.SearchMatch{
					File:        token,
					DisplayPath: base64.RawURLEncoding.EncodeToString(encPath),
					LineNumber:  i + 1,
					Content:     base64.RawURLEncoding.EncodeToString(encLine),
				})
				if len(matches) >= maxSearchResults {
					truncated = true
					return filepath.SkipAll
				}
			}
		}
		return nil
	})

	return protocol.SearchResults{
		Matches:   matches,
		Truncated: truncated,
	}
}

func (d *Daemon) send(ctx context.Context, conn *websocket.Conn, msgType protocol.MessageType, payload any) error {
	data, err := protocol.Encode(msgType, payload)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

func DeriveHMACKey(seed []byte) ([]byte, error) {
	r := hkdf.New(sha256.New, seed, nil, []byte("pairpad-path"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, err
	}
	return key, nil
}

func PathToken(hmacKey []byte, path string) string {
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write([]byte(path))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func DeriveKey(seed []byte) ([]byte, error) {
	r := hkdf.New(sha256.New, seed, nil, []byte("pairpad-e2e"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, err
	}
	return key, nil
}

func EncryptContent(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func DecryptContent(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, ct, nil)
}


