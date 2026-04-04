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
		Short: "Keep dumps fresh and clean up expired copies",
		Long: `daemon keeps ditto ready for CI:

  - Refreshes the source dump on the configured cron schedule
  - Deletes isolated database copies after their TTL expires
  - Recovers copies left mid-transition after a previous crash

Run it on the same host that owns the Docker-compatible runtime and the local
dump file. The process blocks until SIGTERM or SIGINT.`,
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

	// Start the warm copy pool refiller (no-op when warm_pool_size=0).
	mgr.StartPool(ctx)

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
