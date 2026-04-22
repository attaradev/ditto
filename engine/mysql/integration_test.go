//go:build integration

package mysql_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/testutil/integrationdb"
	_ "github.com/go-sql-driver/mysql"
)

func TestMySQLDumpRestoreCycle(t *testing.T) {
	ctx := t.Context()
	suite := integrationdb.NewSuite(t, "mysql")
	eng := suite.Engine
	srcDB := suite.StartSource()
	seedMySQLWidgets(t, ctx, srcDB.LocalDSN())

	dumpDir := t.TempDir()
	dumpPath := dumpDir + "/dump.sql.gz"
	if err := eng.Dump(ctx, suite.Docker, "", srcDB.NetworkSourceConfig(), dumpPath, engine.DumpOptions{}); err != nil {
		t.Fatalf("Dump: %v", err)
	}

	copyDB := suite.StartCopy(dumpDir)
	if err := eng.Restore(ctx, suite.Docker, dumpPath, copyDB.Name, copyDB.Bootstrap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	assertMySQLWidgetCount(t, ctx, copyDB.LocalDSN(), 2)
}

func TestMySQLSchemaOnlyDump(t *testing.T) {
	ctx := t.Context()
	suite := integrationdb.NewSuite(t, "mysql")
	eng := suite.Engine
	srcDB := suite.StartSource()
	seedMySQLWidgets(t, ctx, srcDB.LocalDSN())

	dumpDir := t.TempDir()
	dumpPath := dumpDir + "/schema.sql.gz"
	if err := eng.Dump(ctx, suite.Docker, "", srcDB.NetworkSourceConfig(), dumpPath, engine.DumpOptions{SchemaOnly: true}); err != nil {
		t.Fatalf("Dump schema-only: %v", err)
	}

	copyDB := suite.StartCopy(dumpDir)
	if err := eng.Restore(ctx, suite.Docker, dumpPath, copyDB.Name, copyDB.Bootstrap); err != nil {
		t.Fatalf("Restore schema-only: %v", err)
	}

	// Table must exist but contain zero rows.
	assertMySQLWidgetCount(t, ctx, copyDB.LocalDSN(), 0)
}

func seedMySQLWidgets(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open source db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(ctx, `CREATE TABLE widgets (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(255) NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO widgets (name) VALUES ('foo'), ('bar')`); err != nil {
		t.Fatalf("insert rows: %v", err)
	}
}

func assertMySQLWidgetCount(t *testing.T, ctx context.Context, dsn string, want int) {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
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
