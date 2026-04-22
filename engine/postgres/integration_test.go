//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/testutil/integrationdb"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresDumpRestoreCycle(t *testing.T) {
	ctx := t.Context()
	suite := integrationdb.NewSuite(t, "postgres")
	eng := suite.Engine
	srcDB := suite.StartSource()
	seedPGWidgets(t, ctx, srcDB.LocalDSN())

	dumpDir := t.TempDir()
	dumpPath := dumpDir + "/dump.pgc"
	if err := eng.Dump(ctx, suite.Docker, "", srcDB.NetworkSourceConfig(), dumpPath, engine.DumpOptions{}); err != nil {
		t.Fatalf("Dump: %v", err)
	}

	copyDB := suite.StartCopy(dumpDir)
	if err := eng.Restore(ctx, suite.Docker, dumpPath, copyDB.Name, copyDB.Bootstrap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	assertPGWidgetCount(t, ctx, copyDB.LocalDSN(), 2)
}

func TestPostgresSchemaOnlyDump(t *testing.T) {
	ctx := t.Context()
	suite := integrationdb.NewSuite(t, "postgres")
	eng := suite.Engine
	srcDB := suite.StartSource()
	seedPGWidgets(t, ctx, srcDB.LocalDSN())

	dumpDir := t.TempDir()
	dumpPath := dumpDir + "/schema.pgc"
	if err := eng.Dump(ctx, suite.Docker, "", srcDB.NetworkSourceConfig(), dumpPath, engine.DumpOptions{SchemaOnly: true}); err != nil {
		t.Fatalf("Dump schema-only: %v", err)
	}

	copyDB := suite.StartCopy(dumpDir)
	if err := eng.Restore(ctx, suite.Docker, dumpPath, copyDB.Name, copyDB.Bootstrap); err != nil {
		t.Fatalf("Restore schema-only: %v", err)
	}

	// Table must exist but contain zero rows (schema-only dump has no data).
	assertPGWidgetCount(t, ctx, copyDB.LocalDSN(), 0)
}

func seedPGWidgets(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open source db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(ctx, `CREATE TABLE widgets (id SERIAL PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO widgets (name) VALUES ('foo'), ('bar')`); err != nil {
		t.Fatalf("insert rows: %v", err)
	}
}

func assertPGWidgetCount(t *testing.T, ctx context.Context, dsn string, want int) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open copy db: %v", err)
	}
	defer func() { _ = db.Close() }()

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM widgets").Scan(&count); err != nil {
		t.Fatalf("count widgets: %v", err)
	}
	if count != want {
		t.Errorf("widget count: got %d, want %d", count, want)
	}
}
