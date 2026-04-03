package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Event is a single append-only audit log entry.
type Event struct {
	ID         int64
	EntityType string
	EntityID   string
	Action     string
	Actor      string
	Metadata   map[string]any
	CreatedAt  time.Time
}

// EventStore wraps a *sql.DB and exposes the append-only events log.
type EventStore struct {
	db *sql.DB
}

// NewEventStore returns an EventStore backed by db.
func NewEventStore(db *sql.DB) *EventStore {
	return &EventStore{db: db}
}

// Append adds a new event to the log. metadata may be nil or any
// JSON-marshallable value.
func (s *EventStore) Append(entityType, entityID, action, actor string, metadata any) error {
	var metaJSON []byte
	if metadata != nil {
		b, err := json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("event.Append marshal metadata: %w", err)
		}
		metaJSON = b
	}

	_, err := s.db.Exec(`
		INSERT INTO events (entity_type, entity_id, action, actor, metadata)
		VALUES (?, ?, ?, ?, ?)`,
		entityType, entityID, action, actor, sql.NullString{
			String: string(metaJSON),
			Valid:  metaJSON != nil,
		},
	)
	if err != nil {
		return fmt.Errorf("event.Append %s/%s/%s: %w", entityType, entityID, action, err)
	}
	return nil
}

// List returns all events for entityID, ordered by created_at ASC.
func (s *EventStore) List(entityID string) ([]*Event, error) {
	rows, err := s.db.Query(`
		SELECT id, entity_type, entity_id, action, actor, metadata, created_at
		FROM events
		WHERE entity_id = ?
		ORDER BY created_at ASC`, entityID)
	if err != nil {
		return nil, fmt.Errorf("event.List %s: %w", entityID, err)
	}
	defer rows.Close()

	var events []*Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func scanEvent(rows *sql.Rows) (*Event, error) {
	var e Event
	var createdAt string
	var metaStr sql.NullString

	if err := rows.Scan(
		&e.ID, &e.EntityType, &e.EntityID, &e.Action, &e.Actor,
		&metaStr, &createdAt,
	); err != nil {
		return nil, fmt.Errorf("event scan: %w", err)
	}

	e.CreatedAt = parseTime(createdAt)

	if metaStr.Valid && metaStr.String != "" {
		if err := json.Unmarshal([]byte(metaStr.String), &e.Metadata); err != nil {
			// Non-fatal: store the raw string under a "raw" key.
			e.Metadata = map[string]any{"raw": metaStr.String}
		}
	}
	return &e, nil
}
