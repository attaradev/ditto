package store

import (
	"testing"
)

func TestOpenAndMigrateIdempotent(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	defer db.Close()

	// Running migrations a second time on the same DB must be a no-op.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	// Verify schema_version has entries.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&count); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if count == 0 {
		t.Fatal("expected schema_version to have rows after migration")
	}

	// Verify tables exist.
	for _, table := range []string{"copies", "events"} {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&name)
		if err != nil {
			t.Fatalf("table %q not found: %v", table, err)
		}
	}
}
