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
	SessionID string `json:"session_id"`
	HostToken string `json:"host_token"`
	ProjectID string `json:"project_id"`
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
func loadSession(projectID string, forceNew bool) (string, string) {
	dir := sessionsDir()
	if dir == "" {
		return generateSessionID(), generateSessionID()
	}

	path := filepath.Join(dir, projectID+".json")

	if !forceNew {
		data, err := os.ReadFile(path)
		if err == nil {
			var rec sessionRecord
			if json.Unmarshal(data, &rec) == nil && rec.SessionID != "" && rec.HostToken != "" {
				return rec.SessionID, rec.HostToken
			}
		}
	}

	// Generate new session ID and host token, save
	sessionID := generateSessionID()
	hostToken := generateSessionID()
	saveSession(projectID, sessionID, hostToken)
	return sessionID, hostToken
}

// saveSession persists the session record for a project.
func saveSession(projectID, sessionID, hostToken string) {
	dir := sessionsDir()
	if dir == "" {
		return
	}
	rec := sessionRecord{SessionID: sessionID, HostToken: hostToken, ProjectID: projectID}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(filepath.Join(dir, projectID+".json"), data, 0o600)
}

func generateSessionID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
