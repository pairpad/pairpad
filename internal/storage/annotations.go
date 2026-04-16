package storage

import "time"

// Annotation stores a comment or tour as a JSON blob keyed by project.
type Annotation struct {
	ID        string
	ProjectID string
	Type      string // "comment" or "tour"
	Data      string // JSON blob
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ListAnnotations returns all annotations for a project, optionally filtered by type.
func (s *DB) ListAnnotations(projectID, annotationType string) ([]Annotation, error) {
	query := `SELECT id, project_id, type, data, created_at, updated_at
	          FROM annotations WHERE project_id = ?`
	args := []any{projectID}

	if annotationType != "" {
		query += ` AND type = ?`
		args = append(args, annotationType)
	}

	query += ` ORDER BY created_at`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var annotations []Annotation
	for rows.Next() {
		var a Annotation
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Type, &a.Data, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		annotations = append(annotations, a)
	}
	return annotations, rows.Err()
}

// UpsertAnnotation creates or updates an annotation.
func (s *DB) UpsertAnnotation(a Annotation) error {
	_, err := s.db.Exec(`
		INSERT INTO annotations (id, project_id, type, data, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			data = excluded.data,
			updated_at = excluded.updated_at
	`, a.ID, a.ProjectID, a.Type, a.Data, a.CreatedAt, a.UpdatedAt)
	return err
}

// DeleteAnnotation removes an annotation by ID.
func (s *DB) DeleteAnnotation(id string) error {
	_, err := s.db.Exec(`DELETE FROM annotations WHERE id = ?`, id)
	return err
}

// CountAnnotations returns the number of annotations of a given type for a project.
func (s *DB) CountAnnotations(projectID, annotationType string) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM annotations WHERE project_id = ? AND type = ?`,
		projectID, annotationType,
	).Scan(&count)
	return count, err
}

// Now returns the current time (for consistency in tests).
func Now() time.Time {
	return time.Now()
}
