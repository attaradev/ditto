//go:build integration

package mysql_test

import (
	"testing"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/testutil/integrationdb"
	_ "github.com/go-sql-driver/mysql"
)

func TestMySQLDumpRestoreCycle(t *testing.T) {
	suite := integrationdb.NewSuite(t, "mysql")
	srcDB := suite.StartSource()
	seedMySQLWidgets(t, srcDB)

	copyDB := suite.DumpRestore(t, srcDB, "dump.sql.gz", engine.DumpOptions{})
	integrationdb.AssertTableCount(t, copyDB, "widgets", 2)
}

func TestMySQLSchemaOnlyDump(t *testing.T) {
	suite := integrationdb.NewSuite(t, "mysql")
	srcDB := suite.StartSource()
	seedMySQLWidgets(t, srcDB)

	copyDB := suite.DumpRestore(t, srcDB, "schema.sql.gz", engine.DumpOptions{SchemaOnly: true})
	integrationdb.AssertTableCount(t, copyDB, "widgets", 0)
}

func TestMySQLExcludeTableData(t *testing.T) {
	suite := integrationdb.NewSuite(t, "mysql")
	srcDB := suite.StartSource()
	seedMySQLWidgets(t, srcDB)
	seedMySQLEvents(t, srcDB)

	copyDB := suite.DumpRestore(t, srcDB, "dump.sql.gz", engine.DumpOptions{ExcludeTableData: []string{"events"}})
	integrationdb.AssertTableCount(t, copyDB, "widgets", 2)
	integrationdb.AssertTableCount(t, copyDB, "events", 0)
}

func seedMySQLWidgets(t *testing.T, db *integrationdb.Database) {
	t.Helper()
	integrationdb.ExecSQL(t, db,
		`CREATE TABLE widgets (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(255) NOT NULL)`,
		`INSERT INTO widgets (name) VALUES ('foo'), ('bar')`,
	)
}

func seedMySQLEvents(t *testing.T, db *integrationdb.Database) {
	t.Helper()
	integrationdb.ExecSQL(t, db,
		`CREATE TABLE events (id INT AUTO_INCREMENT PRIMARY KEY, payload TEXT)`,
		`INSERT INTO events (payload) VALUES ('e1'), ('e2'), ('e3')`,
	)
}
