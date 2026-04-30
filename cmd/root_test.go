package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestInitializeLocalContextPreservesConfigLoadError(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ditto.yaml")
	if err := os.WriteFile(cfgPath, []byte("source:\n  host: localhost\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, err := initializeLocalContext(context.Background(), filepath.Join(dir, "ditto.db"), cfgPath)
	if err != nil {
		t.Fatalf("initializeLocalContext: %v", err)
	}
	db := ctx.Value(keyDB).(interface{ Close() error })
	t.Cleanup(func() { _ = db.Close() })

	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	err = localInitError(cmd)
	if !strings.Contains(err.Error(), "config: missing required fields") {
		t.Fatalf("local init error: got %q, want config validation error", err)
	}
	if strings.Contains(err.Error(), "unknown engine") {
		t.Fatalf("local init error was overwritten by engine lookup: %q", err)
	}
}
