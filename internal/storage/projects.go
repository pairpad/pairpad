package storage

import (
	"database/sql"
	"time"
)

// Project represents a codebase tracked by Pairpad.
type Project struct {
	ID         string
	Name       string
	RemoteURL  string
	Visibility string // "public" or "private"
	CreatedAt  time.Time
}

// GetOrCreateProject loads an existing project by ID, or creates a new one.
func (s *DB) GetOrCreateProject(id, name, remoteURL string) (*Project, error) {
	p, err := s.GetProject(id)
	if err == nil {
		return p, nil
	}

	_, err = s.db.Exec(
		`INSERT INTO projects (id, name, remote_url) VALUES (?, ?, ?)`,
		id, name, remoteURL,
	)
	if err != nil {
		return nil, err
	}

	return s.GetProject(id)
}

// GetProject loads a project by ID.
func (s *DB) GetProject(id string) (*Project, error) {
	var p Project
	var remoteURL sql.NullString
	err := s.db.QueryRow(
		`SELECT id, name, remote_url, visibility, created_at FROM projects WHERE id = ?`,
		id,
	).Scan(&p.ID, &p.Name, &remoteURL, &p.Visibility, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	p.RemoteURL = remoteURL.String
	return &p, nil
}
