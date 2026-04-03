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
)

// dumpMock is a minimal engine.Engine whose Dump writes fixed content to the
// requested path (or returns dumpErr if set).
type dumpMock struct {
	content []byte
	dumpErr error
}

func (d *dumpMock) Name() string                                                       { return "mock" }
func (d *dumpMock) ContainerImage() string                                             { return "mock:latest" }
func (d *dumpMock) ConnectionString(host string, port int) string                      { return "mock://" }
func (d *dumpMock) WaitReady(_ int, _ time.Duration) error                             { return nil }
func (d *dumpMock) Restore(_ context.Context, _ string, _ int) error                  { return nil }
func (d *dumpMock) Dump(_ context.Context, _ engine.SourceConfig, dest string) error {
	if d.dumpErr != nil {
		return d.dumpErr
	}
	return os.WriteFile(dest, d.content, 0644)
}

var _ engine.Engine = (*dumpMock)(nil)

func newTestScheduler(t *testing.T, destPath string, eng engine.Engine) *Scheduler {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	cfg := &config.Config{
		Source: config.Source{
			Engine: "mock", Host: "localhost", Database: "db", User: "u", Password: "p",
		},
		Dump: config.Dump{Path: destPath},
	}
	return New(cfg, eng, store.NewEventStore(db))
}

func TestAtomicSwap(t *testing.T) {
	dir := t.TempDir()
	destPath := filepath.Join(dir, "latest.gz")

	sched := newTestScheduler(t, destPath, &dumpMock{content: []byte("fake dump data")})

	if err := sched.RunOnce(context.Background()); err != nil {
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

	if err := os.WriteFile(destPath, []byte("old good dump"), 0644); err != nil {
		t.Fatal(err)
	}

	sched := newTestScheduler(t, destPath, &dumpMock{dumpErr: os.ErrPermission})

	if err := sched.RunOnce(context.Background()); err == nil {
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
