//go:build integration

package copy_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/config"
	copyapi "github.com/attaradev/ditto/internal/copy"
	"github.com/attaradev/ditto/internal/store"
	"github.com/attaradev/ditto/internal/testutil/integrationdb"
)

func TestManagerCreateAppliesPostRestoreObfuscation(t *testing.T) {
	for _, engineName := range []string{integrationdb.EnginePostgres, integrationdb.EngineMySQL} {
		t.Run(engineName, func(t *testing.T) {
			suite := integrationdb.NewSuite(t, engineName)
			sourceDB := suite.StartSource()
			raw := integrationdb.SeedObfuscationDemo(t, engineName, sourceDB.LocalDSN())
			integrationdb.AssertRawSnapshot(t, raw)

			dumpDir := t.TempDir()
			rawDumpPath := filepath.Join(dumpDir, "raw.gz")
			if err := suite.Engine.Dump(t.Context(), suite.Docker, "", sourceDB.NetworkSourceConfig(), rawDumpPath, engine.DumpOptions{}); err != nil {
				t.Fatalf("Dump raw source: %v", err)
			}

			manager := newManager(t, suite, rawDumpPath)

			rawCopy, err := manager.Create(t.Context(), copyapi.CreateOptions{Obfuscate: false})
			if err != nil {
				t.Fatalf("Create raw copy: %v", err)
			}
			rawSnapshot := integrationdb.SnapshotObfuscationDemo(t, engineName, rawCopy.ConnectionString)
			integrationdb.AssertRawSnapshot(t, rawSnapshot)
			if err := manager.Destroy(t.Context(), rawCopy.ID); err != nil {
				t.Fatalf("Destroy raw copy: %v", err)
			}

			obfuscatedCopy, err := manager.Create(t.Context(), copyapi.CreateOptions{Obfuscate: true})
			if err != nil {
				t.Fatalf("Create obfuscated copy: %v", err)
			}
			t.Cleanup(func() {
				_ = manager.Destroy(context.Background(), obfuscatedCopy.ID)
			})

			obfuscatedSnapshot := integrationdb.SnapshotObfuscationDemo(t, engineName, obfuscatedCopy.ConnectionString)
			integrationdb.AssertObfuscatedSnapshot(t, rawSnapshot, obfuscatedSnapshot)
		})
	}
}

func newManager(t *testing.T, suite *integrationdb.Suite, dumpPath string) *copyapi.Manager {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	port := integrationdb.MustFreePort(t)
	manager, err := copyapi.NewManager(
		&config.Config{
			Dump: config.Dump{
				Path:           dumpPath,
				StaleThreshold: 3600,
			},
			CopyTTLSeconds: 3600,
			PortPoolStart:  port,
			PortPoolEnd:    port,
			Obfuscation: config.Obfuscation{
				Rules: integrationdb.ObfuscationRules(),
			},
		},
		suite.Engine,
		store.NewCopyStore(db),
		store.NewEventStore(db),
		copyapi.NewPortPool(port, port, nil),
		suite.Docker,
	)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return manager
}
