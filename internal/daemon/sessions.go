package daemon

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// sessionRecord persists the session ID and host token for a project locally.
type sessionRecord struct {
	SessionID      string `json:"session_id"`
	HostToken      string `json:"host_token"`
	ProjectID      string `json:"project_id"`
	EncryptionSeed string `json:"encryption_seed"` // base64url-encoded 8-byte seed
}

// sessionsDir returns the directory for storing session records.
func sessionsDir() string {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		switch runtime.GOOS {
		case "darwin":
			configDir = filepath.Join(home, "Library", "Application Support")
		default:
			configDir = filepath.Join(home, ".config")
		}
	}
	dir := filepath.Join(configDir, "pairpad", "sessions")
	os.MkdirAll(dir, 0o755)
	return dir
}

// loadSession loads the session record for a project, or generates a new one.
func loadSession(projectID string, forceNew bool) (sessionID, hostToken, encryptionSeed string) {
	dir := sessionsDir()
	if dir == "" {
		return generateSessionID(), generateSessionID(), generateEncryptionSeed()
	}

	path := filepath.Join(dir, projectID+".json")

	if !forceNew {
		data, err := os.ReadFile(path)
		if err == nil {
			var rec sessionRecord
			if json.Unmarshal(data, &rec) == nil && rec.SessionID != "" && rec.HostToken != "" {
				seed := rec.EncryptionSeed
				if seed == "" {
					seed = generateEncryptionSeed()
					saveSession(projectID, rec.SessionID, rec.HostToken, seed)
				}
				return rec.SessionID, rec.HostToken, seed
			}
		}
	}

	// Generate new session ID, host token, and encryption seed; save
	sessionID = generateSessionID()
	hostToken = generateSessionID()
	encryptionSeed = generateEncryptionSeed()
	saveSession(projectID, sessionID, hostToken, encryptionSeed)
	return sessionID, hostToken, encryptionSeed
}

// saveSession persists the session record for a project.
func saveSession(projectID, sessionID, hostToken, encryptionSeed string) {
	dir := sessionsDir()
	if dir == "" {
		return
	}
	rec := sessionRecord{SessionID: sessionID, HostToken: hostToken, ProjectID: projectID, EncryptionSeed: encryptionSeed}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(filepath.Join(dir, projectID+".json"), data, 0o600)
}

func generateEncryptionSeed() string {
	b := make([]byte, 8)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func generateSessionID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
