package storage

import (
	"encoding/json"
	"time"

	"github.com/pairpad/pairpad/internal/protocol"
)

// SaveTour stores a tour as an annotation in the database.
func (s *DB) SaveTour(projectID string, tour protocol.Tour) error {
	data, err := json.Marshal(tour)
	if err != nil {
		return err
	}
	now := time.Now()
	return s.UpsertAnnotation(Annotation{
		ID:        tour.ID,
		ProjectID: projectID,
		Type:      "tour",
		Data:      string(data),
		CreatedAt: now,
		UpdatedAt: now,
	})
}

// GetTours loads all tours for a project.
func (s *DB) GetTours(projectID string) ([]protocol.Tour, error) {
	annotations, err := s.ListAnnotations(projectID, "tour")
	if err != nil {
		return nil, err
	}

	tours := make([]protocol.Tour, 0, len(annotations))
	for _, a := range annotations {
		var t protocol.Tour
		if err := json.Unmarshal([]byte(a.Data), &t); err != nil {
			continue
		}
		tours = append(tours, t)
	}
	return tours, nil
}

// DeleteTour removes a tour by ID.
func (s *DB) DeleteTour(tourID string) error {
	return s.DeleteAnnotation(tourID)
}

// UpdateTours bulk-updates all tours for a project (used by re-anchoring).
func (s *DB) UpdateTours(projectID string, tours []protocol.Tour) error {
	for _, t := range tours {
		if err := s.SaveTour(projectID, t); err != nil {
			return err
		}
	}
	return nil
}
