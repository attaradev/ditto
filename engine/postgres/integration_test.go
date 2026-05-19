//go:build integration

package postgres_test

import (
	"testing"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/testutil/integrationdb"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresDumpRestoreCycle(t *testing.T) {
	suite := integrationdb.NewSuite(t, "postgres")
	srcDB := suite.StartSource()
	seedPGWidgets(t, srcDB)

	copyDB := suite.DumpRestore(t, srcDB, "dump.pgc", engine.DumpOptions{})
	integrationdb.AssertTableCount(t, copyDB, "widgets", 2)
}

func TestPostgresSchemaOnlyDump(t *testing.T) {
	suite := integrationdb.NewSuite(t, "postgres")
	srcDB := suite.StartSource()
	seedPGWidgets(t, srcDB)

	copyDB := suite.DumpRestore(t, srcDB, "schema.pgc", engine.DumpOptions{SchemaOnly: true})
	integrationdb.AssertTableCount(t, copyDB, "widgets", 0)
}

func TestPostgresExcludeTableData(t *testing.T) {
	suite := integrationdb.NewSuite(t, "postgres")
	srcDB := suite.StartSource()
	seedPGWidgets(t, srcDB)
	seedPGEvents(t, srcDB)

	copyDB := suite.DumpRestore(t, srcDB, "dump.pgc", engine.DumpOptions{ExcludeTableData: []string{"events"}})
	integrationdb.AssertTableCount(t, copyDB, "widgets", 2)
	integrationdb.AssertTableCount(t, copyDB, "events", 0)
}

func seedPGWidgets(t *testing.T, db *integrationdb.Database) {
	t.Helper()
	integrationdb.ExecSQL(t, db,
		`CREATE TABLE widgets (id SERIAL PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO widgets (name) VALUES ('foo'), ('bar')`,
	)
}

func seedPGEvents(t *testing.T, db *integrationdb.Database) {
	t.Helper()
	integrationdb.ExecSQL(t, db,
		`CREATE TABLE events (id SERIAL PRIMARY KEY, payload TEXT)`,
		`INSERT INTO events (payload) VALUES ('e1'), ('e2'), ('e3')`,
	)
}
