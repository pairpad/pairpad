package storage

import (
	"database/sql"
	"time"
)

type SessionRecord struct {
	ID           string
	ProjectID    string
	HostToken    string
	PasswordHash string
	CreatedAt    time.Time
	LastSeenAt   time.Time
}

func (s *DB) SaveSession(id, projectID, hostToken, passwordHash string) error {
	_, err := s.db.Exec(`
		INSERT INTO sessions (id, project_id, host_token, password_hash)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			last_seen_at = CURRENT_TIMESTAMP
	`, id, projectID, hostToken, passwordHash)
	return err
}

func (s *DB) GetSession(id string) (*SessionRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, project_id, host_token, password_hash, created_at, last_seen_at
		FROM sessions WHERE id = ?
	`, id)
	var rec SessionRecord
	err := row.Scan(&rec.ID, &rec.ProjectID, &rec.HostToken, &rec.PasswordHash, &rec.CreatedAt, &rec.LastSeenAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *DB) TouchSession(id string) error {
	_, err := s.db.Exec(`UPDATE sessions SET last_seen_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

func (s *DB) DeleteSession(id string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return err
}

func (s *DB) DeleteStaleSessions(olderThan time.Duration) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE last_seen_at < ?`, time.Now().Add(-olderThan))
	return err
}
