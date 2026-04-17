package storage

import (
	"encoding/json"
	"time"

	"github.com/pairpad/pairpad/internal/protocol"
)

// SaveComment stores a comment as an annotation in the database.
func (s *DB) SaveComment(projectID string, comment protocol.Comment) error {
	data, err := json.Marshal(comment)
	if err != nil {
		return err
	}
	now := time.Now()
	return s.UpsertAnnotation(Annotation{
		ID:        comment.ID,
		ProjectID: projectID,
		Type:      "comment",
		Data:      string(data),
		CreatedAt: now,
		UpdatedAt: now,
	})
}

// GetComments loads all comments for a project.
func (s *DB) GetComments(projectID string) ([]protocol.Comment, error) {
	annotations, err := s.ListAnnotations(projectID, "comment")
	if err != nil {
		return nil, err
	}

	comments := make([]protocol.Comment, 0, len(annotations))
	for _, a := range annotations {
		var c protocol.Comment
		if err := json.Unmarshal([]byte(a.Data), &c); err != nil {
			continue
		}
		comments = append(comments, c)
	}
	return comments, nil
}

// ResolveComment toggles the resolved state of a comment.
func (s *DB) ResolveComment(projectID, commentID string) error {
	comments, err := s.GetComments(projectID)
	if err != nil {
		return err
	}
	for _, c := range comments {
		if c.ID == commentID && c.ParentID == "" {
			c.Resolved = !c.Resolved
			return s.SaveComment(projectID, c)
		}
	}
	return nil
}

// DeleteComment removes a comment and all its replies.
func (s *DB) DeleteComment(projectID, commentID string) error {
	comments, err := s.GetComments(projectID)
	if err != nil {
		return err
	}
	// Delete the comment and any replies to it
	for _, c := range comments {
		if c.ID == commentID || c.ParentID == commentID {
			if err := s.DeleteAnnotation(c.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// UpdateComments bulk-updates all comments for a project (used by re-anchoring).
func (s *DB) UpdateComments(projectID string, comments []protocol.Comment) error {
	for _, c := range comments {
		if err := s.SaveComment(projectID, c); err != nil {
			return err
		}
	}
	return nil
}
