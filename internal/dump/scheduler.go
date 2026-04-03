// Package dump manages the scheduled dump of the source database.
package dump

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/config"
	"github.com/attaradev/ditto/internal/store"
	"github.com/robfig/cron/v3"
)

// Scheduler runs engine.Dump on a cron schedule and atomically replaces the
// local dump file on success.
type Scheduler struct {
	cfg    *config.Config
	eng    engine.Engine
	events *store.EventStore
	cron   *cron.Cron
}

// New creates a Scheduler. Call Start() to begin the cron loop.
func New(cfg *config.Config, eng engine.Engine, events *store.EventStore) *Scheduler {
	return &Scheduler{
		cfg:    cfg,
		eng:    eng,
		events: events,
		cron:   cron.New(),
	}
}

// Start registers the dump job with the cron scheduler and starts it.
func (s *Scheduler) Start() error {
	_, err := s.cron.AddFunc(s.cfg.Dump.Schedule, func() {
		ctx := context.Background()
		if err := s.RunOnce(ctx); err != nil {
			slog.Error("dump: scheduled run failed", "err", err)
		}
	})
	if err != nil {
		return fmt.Errorf("dump: invalid schedule %q: %w", s.cfg.Dump.Schedule, err)
	}
	s.cron.Start()
	return nil
}

// Stop halts the cron scheduler, waiting for any in-progress run to complete.
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
}

// RunOnce executes a single dump cycle:
//  1. Dump to <destPath>.tmp
//  2. Verify the tmp file is non-empty
//  3. Atomically rename to destPath
//
// On failure, the tmp file is removed and the existing destPath is untouched.
func (s *Scheduler) RunOnce(ctx context.Context) error {
	destPath := s.cfg.Dump.Path
	tmpPath := destPath + ".tmp"

	// Ensure the destination directory exists.
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("dump: mkdir %s: %w", filepath.Dir(destPath), err)
	}

	slog.Info("dump: starting", "dest", destPath)

	src := engine.SourceConfig{
		Host:           s.cfg.Source.Host,
		Port:           s.cfg.Source.Port,
		Database:       s.cfg.Source.Database,
		User:           s.cfg.Source.User,
		Password:       s.cfg.Source.Password,
		PasswordSecret: s.cfg.Source.PasswordSecret,
	}

	// Remove any leftover tmp file from a previous failed run.
	_ = os.Remove(tmpPath)

	if err := s.eng.Dump(ctx, src, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("dump: engine dump: %w", err)
	}

	// Verify the file has content.
	info, err := os.Stat(tmpPath)
	if err != nil || info.Size() == 0 {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("dump: tmp file missing or empty after dump")
	}

	// Atomic rename — src and dst share the same directory, so this is a
	// POSIX atomic operation. Readers always see a complete file.
	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("dump: rename %s -> %s: %w", tmpPath, destPath, err)
	}

	slog.Info("dump: complete", "dest", destPath, "size_bytes", info.Size())
	_ = s.events.Append("dump", "latest", "completed", "scheduler",
		map[string]any{"dest": destPath, "size_bytes": info.Size()})
	return nil
}
