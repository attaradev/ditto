package store

import (
	"path/filepath"
	"testing"
)

func TestOpenCreatesParentDirectory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nested", "ditto.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
}
