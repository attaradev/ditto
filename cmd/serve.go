package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/attaradev/ditto/internal/config"
	"github.com/attaradev/ditto/internal/secret"
	"github.com/attaradev/ditto/internal/server"
	dittostore "github.com/attaradev/ditto/internal/store"
	"github.com/attaradev/ditto/internal/version"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the ditto HTTP API server",
		Long: `serve exposes the ditto copy lifecycle over HTTP so any automation
environment can request copies from a centralised ditto server.

Configure the listen address and auth token in ditto.yaml:

  server:
    addr: ":8080"
    token: "my-secret-token"                   # dev only
    token_secret: env:DITTO_TOKEN              # env var (any platform)
    token_secret: file:/run/secrets/token      # mounted secret file
    token_secret: arn:aws:secretsmanager:...   # AWS Secrets Manager

Clients pass the token as 'Authorization: Bearer <token>' and may use the
--server flag on ditto copy commands or set DITTO_TOKEN in the environment.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd)
		},
	}
}

func runServe(cmd *cobra.Command) error {
	mgr := managerFromContext(cmd)
	cfg := configFromContext(cmd)
	cs := copyStoreFromContext(cmd)
	sched := schedulerFromContext(cmd)
	ctx := cmd.Context()

	// Resolve the Bearer token (supports both inline and Secrets Manager).
	var cache secret.Cache
	token, err := cache.Resolve(ctx, cfg.Server.TokenSecret, cfg.Server.Token)
	if err != nil {
		return fmt.Errorf("serve: resolve token: %w", err)
	}
	if token == "" {
		slog.Warn("serve: no token configured — all requests are unauthenticated")
	}

	// Start the warm pool refiller and dump scheduler (same as daemon).
	mgr.StartPool(ctx)
	if err := sched.Start(); err != nil {
		return fmt.Errorf("serve: start scheduler: %w", err)
	}
	slog.Info("serve: dump scheduler started")

	statusFn := makeStatusFn(cs, cfg)
	srv := server.New(cfg.Server.Addr, mgr, token, statusFn)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()

	select {
	case sig := <-stop:
		slog.Info("serve: shutting down", "signal", sig)
		sched.Stop()
		return nil
	case err := <-errCh:
		sched.Stop()
		return err
	case <-ctx.Done():
		sched.Stop()
		return ctx.Err()
	}
}

// makeStatusFn returns a function that produces a server.StatusResponse from
// live store and config state. Called by GET /v1/status on each request.
func makeStatusFn(cs *dittostore.CopyStore, cfg *config.Config) func() server.StatusResponse {
	return func() server.StatusResponse {
		active, _ := cs.List(activeFilter())
		warm, _ := cs.CountWarm()
		total := cfg.PortPoolEnd - cfg.PortPoolStart + 1
		free := total - len(active)
		if free < 0 {
			free = 0
		}
		return server.StatusResponse{
			Version:      version.Version,
			ActiveCopies: len(active),
			WarmCopies:   warm,
			PortPoolFree: free,
		}
	}
}
