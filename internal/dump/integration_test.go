//go:build integration

package dump_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/config"
	"github.com/attaradev/ditto/internal/dump"
	"github.com/attaradev/ditto/internal/store"
	"github.com/attaradev/ditto/internal/testutil/integrationdb"
)

func TestSchedulerRunOnceBakesObfuscation(t *testing.T) {
	for _, engineName := range []string{integrationdb.EnginePostgres, integrationdb.EngineMySQL} {
		t.Run(engineName, func(t *testing.T) {
			suite := integrationdb.NewSuite(t, engineName)
			sourceDB := suite.StartSource()
			raw := integrationdb.SeedObfuscationDemo(t, integrationdb.DBConn{EngineName: engineName, DSN: sourceDB.LocalDSN()})
			integrationdb.AssertRawSnapshot(t, raw)

			dumpDir := t.TempDir()
			dumpPath := filepath.Join(dumpDir, "latest.gz")
			scheduler := newScheduler(t, schedulerFixture{
				suite:    suite,
				sourceDB: sourceDB,
				dumpPath: dumpPath,
				rules:    integrationdb.ObfuscationRules(),
			})

			if err := scheduler.RunOnce(t.Context()); err != nil {
				t.Fatalf("RunOnce: %v", err)
			}

			copyDB := suite.StartCopy(dumpDir)
			if err := suite.Engine.Restore(t.Context(), engine.RestoreRequest{
				Docker:        suite.Docker,
				DumpPath:      dumpPath,
				ContainerName: copyDB.Name,
				Copy:          copyDB.Bootstrap,
			}); err != nil {
				t.Fatalf("Restore obfuscated dump: %v", err)
			}

			got := integrationdb.SnapshotObfuscationDemo(t, integrationdb.DBConn{EngineName: engineName, DSN: copyDB.LocalDSN()})
			integrationdb.AssertObfuscatedSnapshot(t, raw, got)
		})
	}
}

func TestSchedulerRunOnceWarnOnly(t *testing.T) {
	for _, engineName := range []string{integrationdb.EnginePostgres, integrationdb.EngineMySQL} {
		t.Run(engineName, func(t *testing.T) {
			suite := integrationdb.NewSuite(t, engineName)
			sourceDB := suite.StartSource()
			raw := integrationdb.SeedObfuscationDemo(t, integrationdb.DBConn{EngineName: engineName, DSN: sourceDB.LocalDSN()})

			failingDumpPath := filepath.Join(t.TempDir(), "fail.gz")
			failingScheduler := newScheduler(t, schedulerFixture{
				suite:    suite,
				sourceDB: sourceDB,
				dumpPath: failingDumpPath,
				rules:    integrationdb.ObfuscationRulesWithWarnOnlyProbe(false),
			})
			err := failingScheduler.RunOnce(t.Context())
			if err == nil {
				t.Fatal("RunOnce without warn_only: got nil error, want failure")
			}
			if !strings.Contains(err.Error(), "archived_customers.email matched 0 rows") {
				t.Fatalf("RunOnce without warn_only: got %q, want zero-row error", err)
			}
			if _, statErr := os.Stat(failingDumpPath); !os.IsNotExist(statErr) {
				t.Fatalf("failed run should not leave final dump at %s", failingDumpPath)
			}

			dumpDir := t.TempDir()
			passingDumpPath := filepath.Join(dumpDir, "warn-only.gz")
			passingScheduler := newScheduler(t, schedulerFixture{
				suite:    suite,
				sourceDB: sourceDB,
				dumpPath: passingDumpPath,
				rules:    integrationdb.ObfuscationRulesWithWarnOnlyProbe(true),
			})
			if err := passingScheduler.RunOnce(t.Context()); err != nil {
				t.Fatalf("RunOnce with warn_only: %v", err)
			}

			copyDB := suite.StartCopy(dumpDir)
			if err := suite.Engine.Restore(t.Context(), engine.RestoreRequest{
				Docker:        suite.Docker,
				DumpPath:      passingDumpPath,
				ContainerName: copyDB.Name,
				Copy:          copyDB.Bootstrap,
			}); err != nil {
				t.Fatalf("Restore warn_only dump: %v", err)
			}

			got := integrationdb.SnapshotObfuscationDemo(t, integrationdb.DBConn{EngineName: engineName, DSN: copyDB.LocalDSN()})
			integrationdb.AssertObfuscatedSnapshot(t, raw, got)
		})
	}
}

type schedulerFixture struct {
	suite    *integrationdb.Suite
	sourceDB *integrationdb.Database
	dumpPath string
	rules    []config.ObfuscationRule
}

func newScheduler(t *testing.T, fixture schedulerFixture) *dump.Scheduler {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return dump.New(
		&config.Config{
			Source: config.Source{
				Engine:   fixture.suite.EngineName,
				Host:     fixture.suite.HostAccessAddress(),
				Port:     fixture.sourceDB.Port,
				Database: fixture.sourceDB.Bootstrap.Database,
				User:     fixture.sourceDB.Bootstrap.User,
				Password: fixture.sourceDB.Bootstrap.Password,
			},
			Dump: config.Dump{
				Path: fixture.dumpPath,
			},
			Obfuscation: config.Obfuscation{Rules: fixture.rules},
		},
		fixture.suite.Engine,
		store.NewEventStore(db),
		fixture.suite.Docker,
	)
}
