package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/attaradev/ditto/internal/config"
	copypkg "github.com/attaradev/ditto/internal/copy"
	"github.com/attaradev/ditto/internal/dockerutil"
	dumppkg "github.com/attaradev/ditto/internal/dump"
	"github.com/attaradev/ditto/internal/oidc"
	"github.com/attaradev/ditto/internal/refresh"
	"github.com/attaradev/ditto/internal/secret"
	"github.com/attaradev/ditto/internal/server"
	dittostore "github.com/attaradev/ditto/internal/store"
	"github.com/attaradev/ditto/internal/version"
	"github.com/spf13/cobra"
)

func newHostCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "host",
		Short: "Run the ditto shared-host controller",
		Long: `host runs the full shared-host control plane in one process:

  - recovers stuck copies and orphan containers on startup
  - keeps the warm pool filled
  - refreshes dumps on the configured schedule
  - expires copies whose TTL has elapsed
  - serves the authenticated /v2 HTTP API

Use it on the machine that owns the Docker-compatible runtime and the dump cache.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHost(cmd)
		},
	}
}

func runHost(cmd *cobra.Command) error {
	cfg := configFromContext(cmd)
	if !cfg.Server.Enabled {
		return fmt.Errorf("host: server.enabled must be true in ditto.yaml")
	}

	cs := copyStoreFromContext(cmd)
	es := eventStoreFromContext(cmd)
	ctx := cmd.Context()

	mgr, sched, refresher, err := buildHostServices(ctx, cfg, cs, es)
	if err != nil {
		return err
	}

	authn, err := buildAuthenticator(ctx, cfg)
	if err != nil {
		return err
	}

	slog.Info("host: recovering orphans")
	if err := mgr.RecoverOrphans(ctx); err != nil {
		slog.Error("host: orphan recovery failed", "err", err)
	}

	mgr.StartPool(ctx)

	if err := sched.Start(); err != nil {
		return fmt.Errorf("host: start scheduler: %w", err)
	}
	defer sched.Stop()
	slog.Info("host: dump scheduler started")

	srv := server.New(server.Config{
		Addr:       cfg.Server.Addr,
		Controller: mgr,
		Refresher:  refresher,
		Copies:     cs,
		Events:     es,
		Auth:       authn,
		StatusFn:   makeStatusFn(cs, cfg),
	})

	slog.Info("host: running", "addr", cfg.Server.Addr, "advertise_host", cfg.Server.AdvertiseHost)
	return runEventLoop(ctx, srv, mgr)
}

func runEventLoop(ctx context.Context, srv *server.Server, mgr *copypkg.Manager) error {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(stop)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := mgr.ExpireOldCopies(ctx); err != nil {
				slog.Error("host: expire copies failed", "err", err)
			}
		case sig := <-stop:
			slog.Info("host: shutting down", "signal", sig)
			return nil
		case err := <-errCh:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func buildHostServices(ctx context.Context, cfg *config.Config, cs *dittostore.CopyStore, es *dittostore.EventStore) (*copypkg.Manager, *dumppkg.Scheduler, *refresh.Service, error) {
	eng, err := engineFromConfig(cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	docker, _, err := dockerutil.NewClient(ctx, cfg.DockerHost)
	if err != nil {
		return nil, nil, nil, err
	}
	var secretCache secret.Cache
	copySecret, err := secretCache.Resolve(ctx, cfg.Server.CopySecretSecret, "")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("host: resolve copy secret: %w", err)
	}
	pool := copypkg.NewPortPool(cfg.PortPoolStart, cfg.PortPoolEnd, occupiedPorts(cs))
	runtime := copypkg.RemoteRuntimeConfig{
		Mode:          copypkg.AccessModeRemote,
		AdvertiseHost: cfg.Server.AdvertiseHost,
		BindHost:      cfg.Server.DBBindHost,
		CopySecret:    copySecret,
		TLSEnabled:    cfg.Server.DBTLS.CertFile != "" && cfg.Server.DBTLS.KeyFile != "",
		CertFile:      cfg.Server.DBTLS.CertFile,
		KeyFile:       cfg.Server.DBTLS.KeyFile,
	}
	mgr, err := copypkg.NewRemoteManager(copypkg.ManagerDeps{
		Config:     cfg,
		Engine:     eng,
		CopyStore:  cs,
		EventStore: es,
		PortPool:   pool,
		Docker:     docker,
	}, runtime)
	if err != nil {
		return nil, nil, nil, err
	}
	return mgr, dumppkg.New(cfg, eng, es, docker), refresh.New(cfg, es, docker), nil
}

func buildAuthenticator(ctx context.Context, cfg *config.Config) (server.Authenticator, error) {
	if cfg.Server.Auth.StaticToken != "" {
		var sc secret.Cache
		tok, err := sc.Resolve(ctx, cfg.Server.Auth.StaticToken, "")
		if err != nil {
			return nil, fmt.Errorf("host: resolve static token: %w", err)
		}
		slog.Warn("host: static token auth enabled — suitable for evaluation only; configure OIDC for production")
		return oidc.NewStaticToken(tok), nil
	}
	return oidc.New(oidc.Config{
		Issuer:     cfg.Server.Auth.Issuer,
		Audience:   cfg.Server.Auth.Audience,
		JWKSURL:    cfg.Server.Auth.JWKSURL,
		AdminClaim: cfg.Server.Auth.AdminClaim,
		AdminValue: cfg.Server.Auth.AdminValue,
	}), nil
}

func makeStatusFn(cs *dittostore.CopyStore, cfg *config.Config) func() server.StatusResponse {
	return func() server.StatusResponse {
		active, _ := cs.List(activeFilter())
		warm, _ := cs.CountWarm()
		total := cfg.PortPoolEnd - cfg.PortPoolStart + 1
		free := max(0, total-len(active))
		return server.StatusResponse{
			Version:       version.Version,
			ActiveCopies:  len(active),
			WarmCopies:    warm,
			PortPoolFree:  free,
			AdvertiseHost: cfg.Server.AdvertiseHost,
		}
	}
}
