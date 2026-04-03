package store

import (
	"testing"
	"time"
)

func newTestDB(t *testing.T) *CopyStore {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewCopyStore(db)
}

func TestCopyCRUD(t *testing.T) {
	cs := newTestDB(t)

	c := &Copy{
		ID:         "01ABCDEFGHJKMNPQRSTVWXYZ01",
		Status:     StatusPending,
		Port:       5433,
		TTLSeconds: 3600,
		GHARunID:   "run-123",
	}
	if err := cs.Create(c); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := cs.Get(c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != c.ID {
		t.Errorf("ID: got %q, want %q", got.ID, c.ID)
	}
	if got.Port != c.Port {
		t.Errorf("Port: got %d, want %d", got.Port, c.Port)
	}
	if got.Status != StatusPending {
		t.Errorf("Status: got %q, want %q", got.Status, StatusPending)
	}

	// Update status to READY with connection string and ready_at.
	now := time.Now().UTC().Truncate(time.Second)
	if err := cs.UpdateStatus(c.ID, StatusReady,
		WithConnectionString("postgres://ditto:ditto@localhost:5433/ditto"),
		WithReadyAt(now),
	); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	got, err = cs.Get(c.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.Status != StatusReady {
		t.Errorf("Status after update: got %q, want %q", got.Status, StatusReady)
	}
	if got.ConnectionString == "" {
		t.Error("ConnectionString should be set")
	}
	if got.ReadyAt == nil {
		t.Error("ReadyAt should be set")
	}
}

func TestCopyList(t *testing.T) {
	cs := newTestDB(t)

	for i, status := range []CopyStatus{StatusReady, StatusDestroyed, StatusFailed} {
		id := "01ABCDEFGHJKMNPQRSTVWXYZ0" + string(rune('A'+i))
		if err := cs.Create(&Copy{ID: id, Status: status, TTLSeconds: 3600}); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}

	active, err := cs.List(ListFilter{Statuses: []CopyStatus{StatusReady}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(active) != 1 {
		t.Errorf("List(ready): got %d, want 1", len(active))
	}

	all, err := cs.List(ListFilter{})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("List(all): got %d, want 3", len(all))
	}
}

func TestCopyListExpired(t *testing.T) {
	cs := newTestDB(t)

	// Insert a copy that has already expired (TTL = 1 second, created 10s ago).
	_, err := cs.db.Exec(`
		INSERT INTO copies (id, status, port, ttl_seconds, created_at)
		VALUES (?, ?, ?, ?, datetime('now', '-10 seconds'))`,
		"EXPIREDID", string(StatusReady), 5433, 1,
	)
	if err != nil {
		t.Fatalf("insert expired copy: %v", err)
	}

	expired, err := cs.ListExpired()
	if err != nil {
		t.Fatalf("ListExpired: %v", err)
	}
	if len(expired) != 1 || expired[0].ID != "EXPIREDID" {
		t.Errorf("ListExpired: got %v, want [EXPIREDID]", expired)
	}
}
