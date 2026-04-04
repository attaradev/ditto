// Package dump manages the scheduled dump of the source database.
package dump

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/config"
	"github.com/attaradev/ditto/internal/dockerutil"
	"github.com/attaradev/ditto/internal/obfuscation"
	"github.com/attaradev/ditto/internal/store"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/robfig/cron/v3"
)

// Scheduler runs engine.Dump on a cron schedule and atomically replaces the
// local dump file on success.
type Scheduler struct {
	cfg     *config.Config
	eng     engine.Engine
	events  *store.EventStore
	docker  *client.Client
	cron    *cron.Cron
	running atomic.Bool
}

// New creates a Scheduler. Call Start() to begin the cron loop.
func New(cfg *config.Config, eng engine.Engine, events *store.EventStore, docker *client.Client) *Scheduler {
	return &Scheduler{
		cfg:    cfg,
		eng:    eng,
		events: events,
		docker: docker,
		cron:   cron.New(),
	}
}

// Start registers the dump job with the cron scheduler and starts it.
func (s *Scheduler) Start() error {
	_, err := s.cron.AddFunc(s.cfg.Dump.Schedule, func() {
		if !s.running.CompareAndSwap(false, true) {
			slog.Warn("dump: skipping scheduled run, previous run still in progress")
			return
		}
		defer s.running.Store(false)
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

// RunOnce executes a single dump cycle. When obfuscation rules are configured,
// the raw dump is restored into a staging container, scrubbed, and re-dumped so
// the final dump file has PII already removed before any copy restores it.
//
//  1. Dump source → <destPath>.raw (or .tmp when no obfuscation rules)
//  2. If obfuscation rules: restore into staging, apply rules, re-dump → <destPath>.tmp
//  3. Atomically rename .tmp → destPath
func (s *Scheduler) RunOnce(ctx context.Context) error {
	destPath := s.cfg.Dump.Path
	tmpPath := destPath + ".tmp"
	rawPath := destPath + ".raw"

	if err := os.MkdirAll(filepath.Dir(destPath), 0o750); err != nil {
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

	hasRules := len(s.cfg.Obfuscation.Rules) > 0
	dumpDest := tmpPath
	if hasRules {
		dumpDest = rawPath
		_ = os.Remove(rawPath)
	} else {
		_ = os.Remove(tmpPath)
	}

	if err := s.eng.Dump(ctx, s.docker, s.cfg.Dump.ClientImage, src, dumpDest); err != nil {
		_ = os.Remove(dumpDest)
		return fmt.Errorf("dump: engine dump: %w", err)
	}

	info, err := os.Stat(dumpDest)
	if err != nil || info.Size() == 0 {
		_ = os.Remove(dumpDest)
		return fmt.Errorf("dump: file missing or empty after dump")
	}

	if hasRules {
		slog.Info("dump: baking obfuscation", "rules", len(s.cfg.Obfuscation.Rules))
		if err := s.bakeObfuscation(ctx, rawPath, tmpPath); err != nil {
			_ = os.Remove(rawPath)
			_ = os.Remove(tmpPath)
			return fmt.Errorf("dump: bake obfuscation: %w", err)
		}
		_ = os.Remove(rawPath)

		if info, err = os.Stat(tmpPath); err != nil || info.Size() == 0 {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("dump: obfuscated file missing or empty")
		}
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("dump: rename %s -> %s: %w", tmpPath, destPath, err)
	}

	slog.Info("dump: complete", "dest", destPath, "size_bytes", info.Size(), "obfuscated", hasRules)
	_ = s.events.Append("dump", "latest", "completed", "scheduler",
		map[string]any{"dest": destPath, "size_bytes": info.Size(), "obfuscated": hasRules})
	return nil
}

// bakeObfuscation restores rawPath into a temporary staging container, applies
// obfuscation rules, then re-dumps the scrubbed database to outPath.
func (s *Scheduler) bakeObfuscation(ctx context.Context, rawPath, outPath string) error {
	port, err := freePort()
	if err != nil {
		return fmt.Errorf("allocate staging port: %w", err)
	}

	image := s.cfg.CopyImage
	if image == "" {
		image = s.eng.ContainerImage()
	}

	ctrName := fmt.Sprintf("ditto-bake-%d", port)
	ctrID, err := s.startStagingContainer(ctx, ctrName, port, rawPath, image)
	if err != nil {
		return fmt.Errorf("start staging container: %w", err)
	}
	defer func() {
		_ = s.docker.ContainerStop(context.Background(), ctrName, container.StopOptions{Timeout: intPtr(10)})
		_ = s.docker.ContainerRemove(context.Background(), ctrID, container.RemoveOptions{Force: true})
	}()

	if err := s.eng.WaitReady(port, 3*time.Minute); err != nil {
		return fmt.Errorf("staging ready: %w", err)
	}

	if err := s.eng.Restore(ctx, s.docker, rawPath, ctrName); err != nil {
		return fmt.Errorf("staging restore: %w", err)
	}

	connStr := s.eng.ConnectionString("localhost", port)
	if err := obfuscation.New(s.eng.Name(), connStr, s.cfg.Obfuscation.Rules).Apply(ctx); err != nil {
		return fmt.Errorf("staging obfuscate: %w", err)
	}

	if err := s.eng.DumpFromContainer(ctx, s.docker, ctrName, outPath); err != nil {
		return fmt.Errorf("staging re-dump: %w", err)
	}

	return nil
}

// startStagingContainer creates and starts a short-lived container for the
// obfuscation bake step. The dump directory is mounted read-write at /dump so
// DumpFromContainer can write its output there.
func (s *Scheduler) startStagingContainer(ctx context.Context, name string, port int, dumpPath, image string) (containerID string, err error) {
	if err := dockerutil.EnsureImage(ctx, s.docker, image); err != nil {
		return "", err
	}

	portStr := fmt.Sprintf("%d", port)
	exposedPort := nat.Port(portStr + "/tcp")

	resp, err := s.docker.ContainerCreate(ctx,
		&container.Config{
			Image:        image,
			Env:          s.eng.ContainerEnv(),
			ExposedPorts: nat.PortSet{exposedPort: struct{}{}},
		},
		&container.HostConfig{
			PortBindings: nat.PortMap{
				exposedPort: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: portStr}},
			},
			Mounts: []mount.Mount{{
				Type:     mount.TypeBind,
				Source:   filepath.Dir(dumpPath),
				Target:   "/dump",
				ReadOnly: false,
			}},
		},
		nil, nil, name,
	)
	if err != nil {
		return "", fmt.Errorf("create staging container: %w", err)
	}

	if err := s.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = s.docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("start staging container: %w", err)
	}
	return resp.ID, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("find free port: %w", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

func intPtr(i int) *int { return &i }
