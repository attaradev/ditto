package store

import (
	"testing"
)

func newTestEventStore(t *testing.T) (*EventStore, *CopyStore) {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewEventStore(db), NewCopyStore(db)
}

func TestEventsAppendAndList(t *testing.T) {
	es, _ := newTestEventStore(t)

	entityID := "COPY001"
	actions := []string{"created", "started", "ready"}
	for _, action := range actions {
		if err := es.Append("copy", entityID, action, "test", map[string]any{"step": action}); err != nil {
			t.Fatalf("Append %s: %v", action, err)
		}
	}

	events, err := es.List(entityID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("List: got %d events, want 3", len(events))
	}

	// Verify ordering (ASC by created_at).
	for i, e := range events {
		if e.Action != actions[i] {
			t.Errorf("event[%d].Action: got %q, want %q", i, e.Action, actions[i])
		}
		if e.Metadata["step"] != actions[i] {
			t.Errorf("event[%d].Metadata[step]: got %v, want %q", i, e.Metadata["step"], actions[i])
		}
	}
}

func TestEventsNilMetadata(t *testing.T) {
	es, _ := newTestEventStore(t)

	if err := es.Append("copy", "COPY002", "destroyed", "system", nil); err != nil {
		t.Fatalf("Append nil metadata: %v", err)
	}

	events, err := es.List("COPY002")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}
