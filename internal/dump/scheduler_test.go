package dump

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/config"
	"github.com/attaradev/ditto/internal/store"
	"github.com/docker/docker/client"
)

// dumpMock is a minimal engine.Engine whose Dump writes fixed content to the
// requested path (or returns dumpErr if set).
type dumpMock struct {
	content []byte
	dumpErr error
}

func (d *dumpMock) Name() string           { return "mock" }
func (d *dumpMock) ContainerImage() string { return "mock:latest" }
func (d *dumpMock) ContainerSpec(_ engine.CopyBootstrap) engine.ContainerSpec {
	return engine.ContainerSpec{}
}
func (d *dumpMock) ContainerPort() int                                { return 1234 }
func (d *dumpMock) ConnectionString(_ engine.ConnectionConfig) string { return "mock://" }
func (d *dumpMock) WaitReady(_ engine.ConnectionConfig, _ time.Duration) error {
	return nil
}
func (d *dumpMock) Restore(_ context.Context, _ *client.Client, _ string, _ string, _ engine.CopyBootstrap) error {
	return nil
}
func (d *dumpMock) DumpFromContainer(_ context.Context, _ *client.Client, _ string, _ string, _ engine.CopyBootstrap, _ engine.DumpOptions) error {
	return nil
}
func (d *dumpMock) Dump(_ context.Context, _ *client.Client, _ string, _ engine.SourceConfig, dest string, _ engine.DumpOptions) error {
	if d.dumpErr != nil {
		return d.dumpErr
	}
	return os.WriteFile(dest, d.content, 0o600)
}

var _ engine.Engine = (*dumpMock)(nil)

func newTestScheduler(t *testing.T, destPath string, eng engine.Engine) *Scheduler {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})

	cfg := &config.Config{
		Source: config.Source{
			Engine: "mock", Host: "localhost", Database: "db", User: "u", Password: "p",
		},
		Dump: config.Dump{Path: destPath},
	}
	return New(cfg, eng, store.NewEventStore(db), nil)
}

func TestAtomicSwap(t *testing.T) {
	dir := t.TempDir()
	destPath := filepath.Join(dir, "latest.gz")

	sched := newTestScheduler(t, destPath, &dumpMock{content: []byte("fake dump data")})

	if err := sched.RunOnce(t.Context()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read destPath: %v", err)
	}
	if string(data) != "fake dump data" {
		t.Errorf("content: got %q, want %q", data, "fake dump data")
	}
	if _, err := os.Stat(destPath + ".tmp"); !os.IsNotExist(err) {
		t.Error("tmp file should not exist after successful run")
	}
}

func TestAtomicSwapFailedDumpPreservesOld(t *testing.T) {
	dir := t.TempDir()
	destPath := filepath.Join(dir, "latest.gz")

	if err := os.WriteFile(destPath, []byte("old good dump"), 0o600); err != nil {
		t.Fatal(err)
	}

	sched := newTestScheduler(t, destPath, &dumpMock{dumpErr: os.ErrPermission})

	if err := sched.RunOnce(t.Context()); err == nil {
		t.Fatal("expected error from failed dump, got nil")
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read destPath: %v", err)
	}
	if string(data) != "old good dump" {
		t.Errorf("old dump corrupted: got %q", data)
	}
}

func TestSchemaOnlyDump(t *testing.T) {
	dir := t.TempDir()
	destPath := filepath.Join(dir, "latest.gz")
	schemaPath := filepath.Join(dir, "schema.gz")

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := &config.Config{
		Source: config.Source{
			Engine: "mock", Host: "localhost", Database: "db", User: "u", Password: "p",
		},
		Dump: config.Dump{
			Path:       destPath,
			SchemaPath: schemaPath,
		},
	}
	sched := New(cfg, &dumpMock{content: []byte("fake dump data")}, store.NewEventStore(db), nil)

	if err := sched.RunOnce(t.Context()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Full dump written.
	if data, err := os.ReadFile(destPath); err != nil {
		t.Fatalf("read full dump: %v", err)
	} else if string(data) != "fake dump data" {
		t.Errorf("full dump content: got %q, want %q", data, "fake dump data")
	}

	// Schema-only dump written.
	if data, err := os.ReadFile(schemaPath); err != nil {
		t.Fatalf("read schema dump: %v", err)
	} else if string(data) != "fake dump data" {
		t.Errorf("schema dump content: got %q, want %q", data, "fake dump data")
	}

	// No temp files left behind.
	if _, err := os.Stat(schemaPath + ".tmp"); !os.IsNotExist(err) {
		t.Error("schema .tmp file should not exist after successful run")
	}
}
