//go:build integration

package dump_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
			raw := integrationdb.SeedObfuscationDemo(t, engineName, sourceDB.LocalDSN())
			integrationdb.AssertRawSnapshot(t, raw)

			dumpDir := t.TempDir()
			dumpPath := filepath.Join(dumpDir, "latest.gz")
			scheduler := newScheduler(t, suite, sourceDB, dumpPath, integrationdb.ObfuscationRules())

			if err := scheduler.RunOnce(t.Context()); err != nil {
				t.Fatalf("RunOnce: %v", err)
			}

			copyDB := suite.StartCopy(dumpDir)
			if err := suite.Engine.Restore(t.Context(), suite.Docker, dumpPath, copyDB.Name, copyDB.Bootstrap); err != nil {
				t.Fatalf("Restore obfuscated dump: %v", err)
			}

			got := integrationdb.SnapshotObfuscationDemo(t, engineName, copyDB.LocalDSN())
			integrationdb.AssertObfuscatedSnapshot(t, raw, got)
		})
	}
}

func TestSchedulerRunOnceWarnOnly(t *testing.T) {
	for _, engineName := range []string{integrationdb.EnginePostgres, integrationdb.EngineMySQL} {
		t.Run(engineName, func(t *testing.T) {
			suite := integrationdb.NewSuite(t, engineName)
			sourceDB := suite.StartSource()
			raw := integrationdb.SeedObfuscationDemo(t, engineName, sourceDB.LocalDSN())

			failingDumpPath := filepath.Join(t.TempDir(), "fail.gz")
			failingScheduler := newScheduler(t, suite, sourceDB, failingDumpPath, integrationdb.ObfuscationRulesWithWarnOnlyProbe(false))
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
			passingScheduler := newScheduler(t, suite, sourceDB, passingDumpPath, integrationdb.ObfuscationRulesWithWarnOnlyProbe(true))
			if err := passingScheduler.RunOnce(t.Context()); err != nil {
				t.Fatalf("RunOnce with warn_only: %v", err)
			}

			copyDB := suite.StartCopy(dumpDir)
			if err := suite.Engine.Restore(t.Context(), suite.Docker, passingDumpPath, copyDB.Name, copyDB.Bootstrap); err != nil {
				t.Fatalf("Restore warn_only dump: %v", err)
			}

			got := integrationdb.SnapshotObfuscationDemo(t, engineName, copyDB.LocalDSN())
			integrationdb.AssertObfuscatedSnapshot(t, raw, got)
		})
	}
}

func newScheduler(
	t *testing.T,
	suite *integrationdb.Suite,
	sourceDB *integrationdb.Database,
	dumpPath string,
	rules []config.ObfuscationRule,
) *dump.Scheduler {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return dump.New(
		&config.Config{
			Source: config.Source{
				Engine:   suite.EngineName,
				Host:     suite.HostAccessAddress(),
				Port:     sourceDB.Port,
				Database: sourceDB.Bootstrap.Database,
				User:     sourceDB.Bootstrap.User,
				Password: sourceDB.Bootstrap.Password,
			},
			Dump: config.Dump{
				Path: dumpPath,
			},
			Obfuscation: config.Obfuscation{Rules: rules},
		},
		suite.Engine,
		store.NewEventStore(db),
		suite.Docker,
	)
}
