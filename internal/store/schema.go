// Package store manages the SQLite metadata database for ditto.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open opens (or creates) the SQLite database at path, applies WAL mode
// pragmas, and runs all pending migrations. It must be called once at startup;
// the returned *sql.DB is safe for concurrent use.
func Open(path string) (*sql.DB, error) {
	if path != "" && path != ":memory:" {
		dir := filepath.Dir(path)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return nil, fmt.Errorf("store: mkdir %s: %w", dir, err)
			}
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	// SQLite is not safe for concurrent writes without WAL mode.
	// These pragmas must run after every Open.
	pragmas := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA cache_size=-64000`,
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("store: pragma %q: %w", p, err)
		}
	}

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}

	return db, nil
}

// migrations is an ordered list of DDL statements. Each entry is applied
// exactly once; new entries must be appended, never modified.
var migrations = []string{
	// v1: initial schema
	`CREATE TABLE IF NOT EXISTS schema_version (
		version  INTEGER PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
	)`,

	`CREATE TABLE IF NOT EXISTS copies (
		id               TEXT PRIMARY KEY,
		status           TEXT NOT NULL,
		port             INTEGER,
		container_id     TEXT,
		connection_string TEXT,
		gha_run_id       TEXT,
		gha_job_name     TEXT,
		error_message    TEXT,
		created_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
		ready_at         DATETIME,
		destroyed_at     DATETIME,
		ttl_seconds      INTEGER NOT NULL DEFAULT 7200
	)`,

	`CREATE TABLE IF NOT EXISTS events (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		entity_type TEXT NOT NULL,
		entity_id   TEXT NOT NULL,
		action      TEXT NOT NULL,
		actor       TEXT NOT NULL,
		metadata    TEXT,
		created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
	)`,

	`CREATE INDEX IF NOT EXISTS idx_copies_status ON copies(status)`,
	`CREATE INDEX IF NOT EXISTS idx_events_entity ON events(entity_type, entity_id)`,

	// v2: covering index for the TTL expiry query (status + created_at + ttl_seconds)
	// so ListExpired() does not require a full-table scan.
	`CREATE INDEX IF NOT EXISTS idx_copies_ttl ON copies(status, created_at, ttl_seconds)`,

	// v3: warm copy pool column. Default 0 (false) for all existing rows.
	`ALTER TABLE copies ADD COLUMN warm INTEGER NOT NULL DEFAULT 0`,

	// v4: rename automation-tracking columns to generic names.
	`ALTER TABLE copies RENAME COLUMN gha_run_id TO run_id`,
	`ALTER TABLE copies RENAME COLUMN gha_job_name TO job_name`,

	// v5: track copy ownership for remote API authorization.
	`ALTER TABLE copies ADD COLUMN owner_subject TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX IF NOT EXISTS idx_copies_owner ON copies(owner_subject)`,

	// v6: enforce positive TTLs at the database layer.
	`CREATE TRIGGER IF NOT EXISTS copies_ttl_positive_insert
	BEFORE INSERT ON copies
	FOR EACH ROW
	WHEN NEW.ttl_seconds <= 0
	BEGIN
		SELECT RAISE(ABORT, 'ttl_seconds must be greater than zero');
	END`,
	`CREATE TRIGGER IF NOT EXISTS copies_ttl_positive_update
	BEFORE UPDATE OF ttl_seconds ON copies
	FOR EACH ROW
	WHEN NEW.ttl_seconds <= 0
	BEGIN
		SELECT RAISE(ABORT, 'ttl_seconds must be greater than zero');
	END`,
}

// migrate applies any migrations that have not yet been recorded in
// schema_version. Runs each statement in its own transaction so a failure
// leaves the database in a known state.
func migrate(db *sql.DB) error {
	// Ensure schema_version exists before we query it.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version    INTEGER PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
	)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	var current int
	row := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`)
	if err := row.Scan(&current); err != nil {
		return fmt.Errorf("read schema_version: %w", err)
	}

	for i, stmt := range migrations {
		version := i + 1
		if version <= current {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}
	return nil
}
