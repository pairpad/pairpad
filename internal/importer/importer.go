package importer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/pairpad/pairpad/internal/anchor"
	"github.com/pairpad/pairpad/internal/daemon"
	"github.com/pairpad/pairpad/internal/protocol"
)

type TourDef struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Steps       []StepDef `json:"steps"`
}

type StepDef struct {
	File        string `json:"file"`
	Line        int    `json:"line"`
	LineEnd     int    `json:"line_end,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type Config struct {
	ProjectDir string
	ServerURL  string
	SessionID  string
	FilePath   string
}

func Import(cfg Config) error {
	data, err := os.ReadFile(cfg.FilePath)
	if err != nil {
		return fmt.Errorf("reading tour file: %w", err)
	}

	var defs []TourDef
	if err := json.Unmarshal(data, &defs); err != nil {
		return fmt.Errorf("parsing tour file: %w", err)
	}
	if len(defs) == 0 {
		return fmt.Errorf("no tours found in %s", cfg.FilePath)
	}

	projectDir, err := filepath.Abs(cfg.ProjectDir)
	if err != nil {
		return fmt.Errorf("invalid directory: %w", err)
	}

	project := daemon.DetectProject(projectDir)

	var sessionID, hostToken, encryptionSeed string
	if cfg.SessionID != "" {
		sessionID = cfg.SessionID
		_, hostToken, encryptionSeed = daemon.LoadSession(project.ID, false)
	} else {
		sessionID, hostToken, encryptionSeed = daemon.LoadSession(project.ID, false)
	}
	if sessionID == "" || hostToken == "" {
		return fmt.Errorf("no saved session for project %s — run 'pairpad connect' first", project.Name)
	}

	seedBytes, err := base64.RawURLEncoding.DecodeString(encryptionSeed)
	if err != nil {
		return fmt.Errorf("invalid encryption seed: %w", err)
	}
	encKey, err := daemon.DeriveKey(seedBytes)
	if err != nil {
		return fmt.Errorf("deriving encryption key: %w", err)
	}
	hmacKey, err := daemon.DeriveHMACKey(seedBytes)
	if err != nil {
		return fmt.Errorf("deriving HMAC key: %w", err)
	}

	tours, err := prepareTours(defs, projectDir, encKey, hmacKey)
	if err != nil {
		return err
	}

	return sendTours(cfg.ServerURL, sessionID, hostToken, tours)
}

func prepareTours(defs []TourDef, projectDir string, encKey, hmacKey []byte) ([]protocol.Tour, error) {
	tours := make([]protocol.Tour, 0, len(defs))

	for _, def := range defs {
		tour := protocol.Tour{
			ID:          def.ID,
			Title:       encryptField(encKey, def.Title),
			Description: encryptField(encKey, def.Description),
		}

		for _, sd := range def.Steps {
			step := protocol.TourStep{
				File:        daemon.PathToken(hmacKey, sd.File),
				Line:        sd.Line,
				LineEnd:     sd.LineEnd,
				Title:       encryptField(encKey, sd.Title),
				Description: encryptField(encKey, sd.Description),
			}

			lines, err := readFileLines(filepath.Join(projectDir, sd.File))
			if err == nil && sd.Line >= 1 && sd.Line <= len(lines) {
				idx := sd.Line - 1
				step.AnchorText = encryptField(encKey, lines[idx])
				step.AnchorContext = encryptFields(encKey, anchor.GetContext(lines, idx))

				if sd.LineEnd > sd.Line && sd.LineEnd <= len(lines) {
					endIdx := sd.LineEnd - 1
					step.AnchorTextEnd = encryptField(encKey, lines[endIdx])
					step.AnchorContextEnd = encryptFields(encKey, anchor.GetContext(lines, endIdx))
				}
			}

			tour.Steps = append(tour.Steps, step)
		}

		tours = append(tours, tour)
	}

	return tours, nil
}

func sendTours(serverURL, sessionID, hostToken string, tours []protocol.Tour) error {
	wsURL := serverURL + "/ws/browser?session=" + sessionID
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("connecting to relay: %w", err)
	}
	defer conn.CloseNow()

	// Authenticate with host token
	if err := send(ctx, conn, protocol.TypeSessionAuth, protocol.SessionAuth{
		HostToken: hostToken,
	}); err != nil {
		return fmt.Errorf("sending auth: %w", err)
	}

	// Identify as the importer
	if err := send(ctx, conn, protocol.TypeIdentify, protocol.Identify{
		Name:      "pairpad",
		HostToken: hostToken,
	}); err != nil {
		return fmt.Errorf("sending identify: %w", err)
	}

	// Wait for your_color (confirms identification)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("waiting for identification: %w", err)
		}
		env, err := protocol.Decode(data)
		if err != nil {
			continue
		}
		if env.Type == protocol.TypeYourColor {
			break
		}
	}

	// Send each tour
	for i, tour := range tours {
		if err := send(ctx, conn, protocol.TypeTourSave, tour); err != nil {
			return fmt.Errorf("sending tour %d: %w", i+1, err)
		}
		fmt.Printf("  imported: %s (%d steps)\n", tour.ID, len(tour.Steps))
	}

	// Wait for final tour_list broadcast to confirm persistence
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		env, err := protocol.Decode(data)
		if err != nil {
			continue
		}
		if env.Type == protocol.TypeTourList {
			break
		}
	}

	conn.Close(websocket.StatusNormalClosure, "import complete")
	return nil
}

func send(ctx context.Context, conn *websocket.Conn, msgType protocol.MessageType, payload any) error {
	data, err := protocol.Encode(msgType, payload)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

func encryptField(key []byte, plaintext string) string {
	if plaintext == "" {
		return ""
	}
	ct, err := daemon.EncryptContent(key, []byte(plaintext))
	if err != nil {
		return plaintext
	}
	return base64.StdEncoding.EncodeToString(ct)
}

func encryptFields(key []byte, plaintexts []string) []string {
	out := make([]string, len(plaintexts))
	for i, s := range plaintexts {
		out[i] = encryptField(key, s)
	}
	return out
}

func readFileLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(string(data), "\n"), nil
}
