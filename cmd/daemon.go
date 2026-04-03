package cmd

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func newDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the dump scheduler and copy expiry loop",
		Long: `daemon starts the background services:

  - Dump scheduler: runs engine.Dump on the configured cron schedule
  - Copy expiry:    destroys copies older than copy_ttl_seconds (checked every 60s)
  - Orphan recovery: heals mid-transition copies left by a previous crash

The process blocks until SIGTERM or SIGINT.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDaemon(cmd)
		},
	}
}

func runDaemon(cmd *cobra.Command) error {
	mgr := managerFromContext(cmd)
	sched := schedulerFromContext(cmd)
	ctx := cmd.Context()

	// Recover orphans from any previous crash before starting the scheduler.
	slog.Info("daemon: recovering orphans")
	if err := mgr.RecoverOrphans(ctx); err != nil {
		slog.Error("daemon: orphan recovery failed", "err", err)
	}

	// Start the cron-based dump scheduler.
	if err := sched.Start(); err != nil {
		return err
	}
	slog.Info("daemon: dump scheduler started")

	// Expiry ticker — destroy copies whose TTL has elapsed.
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	// Graceful shutdown on SIGTERM / SIGINT.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	slog.Info("daemon: running")
	for {
		select {
		case <-ticker.C:
			if err := mgr.ExpireOldCopies(ctx); err != nil {
				slog.Error("daemon: expire copies failed", "err", err)
			}
		case sig := <-stop:
			slog.Info("daemon: shutting down", "signal", sig)
			sched.Stop()
			return nil
		case <-ctx.Done():
			sched.Stop()
			return ctx.Err()
		}
	}
}
