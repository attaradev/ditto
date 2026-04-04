package copy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/config"
	"github.com/attaradev/ditto/internal/dockerutil"
	"github.com/attaradev/ditto/internal/obfuscation"
	"github.com/attaradev/ditto/internal/store"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/oklog/ulid/v2"
)

const (
	labelManaged = "ditto.managed"
	labelCopyID  = "ditto.copy_id"
	actor        = "ditto-cli"
)

// Manager orchestrates the full lifecycle of ephemeral database copies.
type Manager struct {
	cfg      *config.Config
	eng      engine.Engine
	copies   *store.CopyStore
	events   *store.EventStore
	ports    *PortPool
	docker   *client.Client
	refiller *WarmPoolRefiller
}

// NewManager creates a Manager from an already-resolved Docker-compatible
// runtime client.
func NewManager(
	cfg *config.Config,
	eng engine.Engine,
	copies *store.CopyStore,
	events *store.EventStore,
	ports *PortPool,
	docker *client.Client,
) (*Manager, error) {
	if docker == nil {
		return nil, fmt.Errorf("copy: docker runtime is required")
	}
	m := &Manager{
		cfg:    cfg,
		eng:    eng,
		copies: copies,
		events: events,
		ports:  ports,
		docker: docker,
	}
	m.refiller = NewWarmPoolRefiller(m, cfg.WarmPoolSize)
	return m, nil
}

// StartPool starts the warm copy pool refiller as a background goroutine.
// It is a no-op when warm_pool_size is 0.
func (m *Manager) StartPool(ctx context.Context) {
	if m.cfg.WarmPoolSize == 0 {
		return
	}
	go m.refiller.Run(ctx)
}

// CreateOptions configures a copy creation request.
type CreateOptions struct {
	TTLSeconds int
	RunID      string // optional: identifies the run/session that created this copy
	JobName    string // optional: identifies the job/task within the run
	DumpPath   string // optional: override dump path (local, s3://, http://); empty = use cfg.Dump.Path
	Obfuscate  bool   // apply post-restore obfuscation rules (explicit opt-in)
}

// Create provisions a new ephemeral database copy. When a pre-warmed copy is
// available in the pool it is claimed instantly. Otherwise the full slow path
// (container start + dump restore) is taken.
func (m *Manager) Create(ctx context.Context, opts CreateOptions) (*store.Copy, error) {
	ttl := opts.TTLSeconds
	if ttl == 0 {
		ttl = m.cfg.CopyTTLSeconds
	}

	// Fast path: claim a pre-warmed copy from the pool (sub-millisecond).
	if m.cfg.WarmPoolSize > 0 {
		if c, err := m.copies.ClaimWarm(ttl); err == nil {
			_ = m.copies.UpdateStatus(c.ID, store.StatusReady,
				store.WithRunID(opts.RunID),
				store.WithJobName(opts.JobName),
			)
			_ = m.events.Append("copy", c.ID, "claimed", actor,
				map[string]any{"warm": true, "ttl": ttl})
			m.refiller.Signal()
			c.RunID = opts.RunID
			c.JobName = opts.JobName
			return c, nil
		}
		slog.Warn("pool: warm pool empty, provisioning fresh copy")
	}

	// Slow path: full dump-and-restore provisioning.
	return m.provision(ctx, opts, ttl, false)
}

// List returns all copies regardless of status, newest first.
func (m *Manager) List(ctx context.Context) ([]*store.Copy, error) {
	return m.copies.List(store.ListFilter{})
}

// Destroy stops and removes a copy's container and marks it destroyed.
func (m *Manager) Destroy(ctx context.Context, id string) error {
	c, err := m.copies.Get(id)
	if err != nil {
		return fmt.Errorf("copy.Destroy get %s: %w", id, err)
	}
	if c.Status == store.StatusDestroyed || c.Status == store.StatusFailed {
		return nil // already gone
	}

	if err := m.copies.UpdateStatus(id, store.StatusDestroying); err != nil {
		return err
	}
	_ = m.events.Append("copy", id, "destroying", actor, nil)

	stopErr := m.docker.ContainerStop(ctx, containerName(id), container.StopOptions{Timeout: new(10)})
	rmErr := m.docker.ContainerRemove(ctx, containerName(id), container.RemoveOptions{Force: true})

	if stopErr != nil {
		slog.Warn("copy: container stop failed", "id", id, "err", stopErr)
	}
	if rmErr != nil {
		slog.Warn("copy: container remove failed", "id", id, "err", rmErr)
	}

	m.ports.Release(c.Port)

	now := time.Now()
	if err := m.copies.UpdateStatus(id, store.StatusDestroyed, store.WithDestroyedAt(now)); err != nil {
		return err
	}
	_ = m.events.Append("copy", id, "destroyed", actor, nil)
	return nil
}

// ExpireOldCopies destroys all copies whose TTL has elapsed.
func (m *Manager) ExpireOldCopies(ctx context.Context) error {
	expired, err := m.copies.ListExpired()
	if err != nil {
		return err
	}
	for _, c := range expired {
		slog.Info("copy: expiring", "id", c.ID, "age", time.Since(c.CreatedAt).Round(time.Second))
		if err := m.Destroy(ctx, c.ID); err != nil {
			slog.Error("copy: expire failed", "id", c.ID, "err", err)
		}
	}
	return nil
}

// RecoverOrphans is called at daemon startup. It heals mid-transition records
// left by a crashed process and removes Docker containers not tracked in SQLite.
func (m *Manager) RecoverOrphans(ctx context.Context) error {
	stuck, err := m.copies.ListStuck()
	if err != nil {
		return err
	}
	for _, c := range stuck {
		slog.Warn("copy: recovering stuck copy", "id", c.ID, "status", c.Status)
		// Best-effort stop and remove.
		_ = m.docker.ContainerStop(ctx, containerName(c.ID), container.StopOptions{Timeout: new(5)})
		_ = m.docker.ContainerRemove(ctx, containerName(c.ID), container.RemoveOptions{Force: true})
		m.ports.Release(c.Port)
		_ = m.copies.UpdateStatus(c.ID, store.StatusFailed,
			store.WithErrorMessage("recovered from crash: was "+string(c.Status)))
		_ = m.events.Append("copy", c.ID, "failed", "system",
			map[string]any{"reason": "crash recovery", "previous_status": c.Status})
	}

	// Find Docker containers with our labels that are not in SQLite.
	containerList, err := m.docker.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", labelManaged+"=true")),
	})
	if err != nil {
		return fmt.Errorf("copy: list containers: %w", err)
	}

	for _, ct := range containerList {
		copyID := ct.Labels[labelCopyID]
		if copyID == "" {
			continue
		}
		if _, err := m.copies.Get(copyID); err != nil {
			slog.Warn("copy: removing orphan container", "container_id", ct.ID[:12], "copy_id", copyID)
			_ = m.docker.ContainerStop(ctx, ct.ID, container.StopOptions{Timeout: new(5)})
			_ = m.docker.ContainerRemove(ctx, ct.ID, container.RemoveOptions{Force: true})
		}
	}
	return nil
}

// provisionWarm creates a warm copy for the pool. It sets Warm=true and uses
// TTLSeconds=0 (TTL clock starts at claim time, not provision time).
func (m *Manager) provisionWarm(ctx context.Context) (*store.Copy, error) {
	return m.provision(ctx, CreateOptions{}, 0, true)
}

// provision is the shared slow-path provisioning logic used by Create and
// provisionWarm. warm=true marks the copy for pool pre-warming.
func (m *Manager) provision(ctx context.Context, opts CreateOptions, ttl int, warm bool) (*store.Copy, error) {
	dumpPath := m.cfg.Dump.Path
	if opts.DumpPath != "" {
		dumpPath = opts.DumpPath
	}
	if err := checkDump(dumpPath, m.cfg.Dump.StaleThreshold); err != nil {
		return nil, err
	}

	port, err := m.ports.Allocate()
	if err != nil {
		return nil, fmt.Errorf("copy.Create: %w", err)
	}

	id := ulid.Make().String()
	c := &store.Copy{
		ID:         id,
		Status:     store.StatusPending,
		Port:       port,
		RunID:      opts.RunID,
		JobName:    opts.JobName,
		TTLSeconds: ttl,
		Warm:       warm,
	}

	// Cleanup on any error after port allocation.
	var containerStarted bool
	cleanup := func(cause error) {
		if containerStarted {
			_ = m.docker.ContainerStop(context.Background(), containerName(id), container.StopOptions{Timeout: new(10)})
			_ = m.docker.ContainerRemove(context.Background(), containerName(id), container.RemoveOptions{Force: true})
		}
		m.ports.Release(port)
		if c.ID != "" {
			_ = m.copies.UpdateStatus(id, store.StatusFailed, store.WithErrorMessage(cause.Error()))
			_ = m.events.Append("copy", id, "failed", actor, map[string]any{"error": cause.Error()})
		}
	}

	if err := m.copies.Create(c); err != nil {
		m.ports.Release(port)
		return nil, fmt.Errorf("copy.Create record: %w", err)
	}
	_ = m.events.Append("copy", id, "created", actor, map[string]any{"port": port, "warm": warm})

	// Start the container.
	containerID, err := m.startContainer(ctx, id, port, dumpPath)
	if err != nil {
		cleanup(err)
		return nil, fmt.Errorf("copy.Create start container: %w", err)
	}
	containerStarted = true

	if err := m.copies.UpdateStatus(id, store.StatusCreating, store.WithContainerID(containerID)); err != nil {
		cleanup(err)
		return nil, err
	}
	_ = m.events.Append("copy", id, "started", actor, map[string]any{"container_id": containerID})

	// Wait for the engine.
	if err := m.eng.WaitReady(port, 2*time.Minute); err != nil {
		cleanup(err)
		return nil, fmt.Errorf("copy.Create wait ready: %w", err)
	}

	// Restore the dump.
	if err := m.eng.Restore(ctx, m.docker, dumpPath, containerName(id)); err != nil {
		cleanup(err)
		return nil, fmt.Errorf("copy.Create restore: %w", err)
	}

	// Apply post-restore obfuscation only when explicitly requested via --obfuscate.
	// Standard copies skip this because ditto reseed already bakes rules into the dump.
	if opts.Obfuscate && len(m.cfg.Obfuscation.Rules) > 0 {
		connStr := m.eng.ConnectionString("localhost", port)
		obf := obfuscation.New(m.eng.Name(), connStr, m.cfg.Obfuscation.Rules)
		if err := obf.Apply(ctx); err != nil {
			cleanup(err)
			return nil, fmt.Errorf("copy.Create obfuscate: %w", err)
		}
	}

	now := time.Now()
	connStr := m.eng.ConnectionString("localhost", port)
	if err := m.copies.UpdateStatus(id, store.StatusReady,
		store.WithConnectionString(connStr),
		store.WithReadyAt(now),
	); err != nil {
		cleanup(err)
		return nil, err
	}
	_ = m.events.Append("copy", id, "ready", actor, map[string]any{"connection_string": connStr, "warm": warm})

	c.Status = store.StatusReady
	c.ContainerID = containerID
	c.ConnectionString = connStr
	c.ReadyAt = &now
	return c, nil
}

// startContainer creates and starts a Docker container for the copy.
// It bind-mounts the dump directory read-only at /dump.
func (m *Manager) startContainer(ctx context.Context, id string, port int, dumpPath string) (string, error) {
	dumpDir := filepath.Dir(dumpPath)
	portStr := fmt.Sprintf("%d", port)
	exposedPort := nat.Port(portStr + "/tcp")

	image := m.cfg.CopyImage
	if image == "" {
		image = m.eng.ContainerImage()
	}
	if err := dockerutil.EnsureImage(ctx, m.docker, image); err != nil {
		return "", fmt.Errorf("container image %s: %w", image, err)
	}

	resp, err := m.docker.ContainerCreate(ctx,
		&container.Config{
			Image: image,
			Env:   m.eng.ContainerEnv(),
			ExposedPorts: nat.PortSet{
				exposedPort: struct{}{},
			},
			Labels: map[string]string{
				labelManaged: "true",
				labelCopyID:  id,
			},
		},
		&container.HostConfig{
			PortBindings: nat.PortMap{
				exposedPort: []nat.PortBinding{
					{HostIP: "127.0.0.1", HostPort: portStr},
				},
			},
			Mounts: []mount.Mount{
				{
					Type:     mount.TypeBind,
					Source:   dumpDir,
					Target:   "/dump",
					ReadOnly: true,
				},
			},
		},
		nil, nil,
		containerName(id),
	)
	if err != nil {
		return "", fmt.Errorf("container create: %w", err)
	}

	if err := m.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = m.docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("container start: %w", err)
	}
	return resp.ID, nil
}

func containerName(id string) string { return "ditto-" + id }

func checkDump(path string, staleThreshold int) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("dump file not found at %s — run 'ditto reseed' first", path)
		}
		return fmt.Errorf("stat dump file: %w", err)
	}
	age := time.Since(info.ModTime())
	if staleThreshold > 0 && int(age.Seconds()) > staleThreshold*2 {
		slog.Warn("dump file is stale", "age", age.Round(time.Second), "path", path)
	}
	return nil
}

