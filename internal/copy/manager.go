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
	cerrdefs "github.com/containerd/errdefs"
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
	runtime  RemoteRuntimeConfig
}

// ManagerDeps contains the resolved dependencies needed by Manager.
type ManagerDeps struct {
	Config     *config.Config
	Engine     engine.Engine
	CopyStore  *store.CopyStore
	EventStore *store.EventStore
	PortPool   *PortPool
	Docker     *client.Client
}

// NewManager creates a Manager from an already-resolved Docker-compatible
// runtime client.
func NewManager(deps ManagerDeps) (*Manager, error) {
	if deps.Docker == nil {
		return nil, fmt.Errorf("copy: docker runtime is required")
	}
	m := &Manager{
		cfg:    deps.Config,
		eng:    deps.Engine,
		copies: deps.CopyStore,
		events: deps.EventStore,
		ports:  deps.PortPool,
		docker: deps.Docker,
		runtime: RemoteRuntimeConfig{
			Mode: AccessModeLocal,
		},
	}
	m.refiller = NewWarmPoolRefiller(m, deps.Config.WarmPoolSize)
	return m, nil
}

// NewRemoteManager creates a Manager configured for shared-host mode.
func NewRemoteManager(deps ManagerDeps, runtime RemoteRuntimeConfig) (*Manager, error) {
	m, err := NewManager(deps)
	if err != nil {
		return nil, err
	}
	runtime.Mode = AccessModeRemote
	m.runtime = runtime
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
	TTLSeconds   int
	RunID        string // optional: identifies the run/session that created this copy
	JobName      string // optional: identifies the job/task within the run
	OwnerSubject string // optional: authenticated caller identity for shared-host mode
	DumpPath     string // optional: override dump path (local, s3://, http://); empty = use cfg.Dump.Path
	DumpURI      string // optional: remote API hint; the local manager ignores this field
	Obfuscate    bool   // apply post-restore obfuscation rules (explicit opt-in)
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
		if c, err := m.copies.ClaimWarm(ttl, opts.OwnerSubject); err == nil {
			_ = m.copies.UpdateStatus(c.ID, store.StatusReady,
				store.WithOwnerSubject(opts.OwnerSubject),
				store.WithRunID(opts.RunID),
				store.WithJobName(opts.JobName),
			)
			_ = m.events.Append("copy", c.ID, "claimed", actor,
				map[string]any{"warm": true, "ttl": ttl})
			m.refiller.Signal()
			c.OwnerSubject = opts.OwnerSubject
			c.RunID = opts.RunID
			c.JobName = opts.JobName
			return c, nil
		}
		slog.Warn("pool: warm pool empty, provisioning fresh copy")
	}

	// Slow path: full dump-and-restore provisioning.
	return m.provision(ctx, provisionRequest{opts: opts, ttl: ttl})
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

	stopErr := m.docker.ContainerStop(ctx, containerName(id), container.StopOptions{Timeout: intPtr(10)})
	rmErr := m.docker.ContainerRemove(ctx, containerName(id), container.RemoveOptions{Force: true})

	if stopErr != nil && !cerrdefs.IsNotFound(stopErr) {
		slog.Warn("copy: container stop failed", "id", id, "err", stopErr)
	}
	if rmErr != nil && !cerrdefs.IsNotFound(rmErr) {
		slog.Warn("copy: container remove failed", "id", id, "err", rmErr)
		_ = m.copies.UpdateStatus(id, store.StatusFailed, store.WithErrorMessage(rmErr.Error()))
		return fmt.Errorf("copy.Destroy remove %s: %w", id, rmErr)
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
		_ = m.docker.ContainerStop(ctx, containerName(c.ID), container.StopOptions{Timeout: intPtr(5)})
		_ = m.docker.ContainerRemove(ctx, containerName(c.ID), container.RemoveOptions{Force: true})
		m.ports.Release(c.Port)
		_ = m.copies.UpdateStatus(c.ID, store.StatusFailed,
			store.WithErrorMessage("recovered from crash: was "+string(c.Status)))
		_ = m.events.Append("copy", c.ID, "failed", "system",
			map[string]any{"reason": "crash recovery", "previous_status": c.Status})
	}

	// Find Docker containers with our labels that are not in SQLite.
	containerList, err := m.docker.ContainerList(ctx, container.ListOptions{
		All:     true,
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
			_ = m.docker.ContainerStop(ctx, ct.ID, container.StopOptions{Timeout: intPtr(5)})
			_ = m.docker.ContainerRemove(ctx, ct.ID, container.RemoveOptions{Force: true})
		}
	}
	return nil
}

// provisionWarm creates a warm copy for the pool. It sets Warm=true and uses
// TTLSeconds=0 (TTL clock starts at claim time, not provision time).
func (m *Manager) provisionWarm(ctx context.Context) (*store.Copy, error) {
	return m.provision(ctx, provisionRequest{warm: true})
}

type provisionRequest struct {
	opts CreateOptions
	ttl  int
	warm bool
}

// provision is the shared slow-path provisioning logic used by Create and
// provisionWarm. warm=true marks the copy for pool pre-warming.
func (m *Manager) provision(ctx context.Context, req provisionRequest) (*store.Copy, error) {
	dumpPath := m.resolveDumpPath(req.opts)
	if err := checkDump(dumpPath, m.cfg.Dump.StaleThreshold); err != nil {
		return nil, err
	}

	port, err := m.ports.AllocateWithTimeout(ctx, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("copy.Create: %w", err)
	}

	id := ulid.Make().String()
	c := newCopyRecord(id, port, req)

	copyRuntime, err := m.copyRuntime(id, port)
	if err != nil {
		m.ports.Release(port)
		return nil, err
	}

	if err := m.copies.Create(c); err != nil {
		m.ports.Release(port)
		return nil, fmt.Errorf("copy.Create record: %w", err)
	}
	_ = m.events.Append("copy", id, "created", actor, map[string]any{"port": port, "warm": req.warm})

	cleanup := provisionCleanup{manager: m, id: id, port: port}
	containerID, err := m.startContainer(ctx, startContainerRequest{
		id:          id,
		dumpPath:    dumpPath,
		copyRuntime: copyRuntime,
	})
	if err != nil {
		cleanup.run(err)
		return nil, fmt.Errorf("copy.Create start container: %w", err)
	}
	cleanup.containerStarted = true

	if err := m.markCreating(id, containerID); err != nil {
		cleanup.run(err)
		return nil, err
	}

	if err := m.eng.WaitReady(copyRuntime.Internal, 2*time.Minute); err != nil {
		cleanup.run(err)
		return nil, fmt.Errorf("copy.Create wait ready: %w", err)
	}

	if err := m.restoreDump(ctx, id, dumpPath, copyRuntime); err != nil {
		cleanup.run(err)
		return nil, fmt.Errorf("copy.Create restore: %w", err)
	}

	if err := m.applyPostRestoreObfuscation(ctx, req.opts, copyRuntime); err != nil {
		cleanup.run(err)
		return nil, fmt.Errorf("copy.Create obfuscate: %w", err)
	}

	if err := m.markReady(c, readyCopy{
		containerID: containerID,
		copyRuntime: copyRuntime,
		warm:        req.warm,
	}); err != nil {
		cleanup.run(err)
		return nil, err
	}
	return c, nil
}

func (m *Manager) resolveDumpPath(opts CreateOptions) string {
	if opts.DumpPath != "" {
		return opts.DumpPath
	}
	return m.cfg.Dump.Path
}

func newCopyRecord(id string, port int, req provisionRequest) *store.Copy {
	return &store.Copy{
		ID:           id,
		Status:       store.StatusPending,
		Port:         port,
		OwnerSubject: req.opts.OwnerSubject,
		RunID:        req.opts.RunID,
		JobName:      req.opts.JobName,
		TTLSeconds:   req.ttl,
		Warm:         req.warm,
	}
}

type provisionCleanup struct {
	manager          *Manager
	id               string
	port             int
	containerStarted bool
}

func (c provisionCleanup) run(cause error) {
	if c.containerStarted {
		_ = c.manager.docker.ContainerStop(context.Background(), containerName(c.id), container.StopOptions{Timeout: intPtr(10)})
		_ = c.manager.docker.ContainerRemove(context.Background(), containerName(c.id), container.RemoveOptions{Force: true})
	}
	c.manager.ports.Release(c.port)
	_ = c.manager.copies.UpdateStatus(c.id, store.StatusFailed, store.WithErrorMessage(cause.Error()))
	_ = c.manager.events.Append("copy", c.id, "failed", actor, map[string]any{"error": cause.Error()})
}

func (m *Manager) markCreating(id, containerID string) error {
	if err := m.copies.UpdateStatus(id, store.StatusCreating, store.WithContainerID(containerID)); err != nil {
		return err
	}
	_ = m.events.Append("copy", id, "started", actor, map[string]any{"container_id": containerID})
	return nil
}

func (m *Manager) restoreDump(ctx context.Context, id, dumpPath string, copyRuntime CopyRuntime) error {
	return m.eng.Restore(ctx, engine.RestoreRequest{
		Docker:        m.docker,
		DumpPath:      dumpPath,
		ContainerName: containerName(id),
		Copy:          copyRuntime.Bootstrap,
	})
}

func (m *Manager) applyPostRestoreObfuscation(ctx context.Context, opts CreateOptions, copyRuntime CopyRuntime) error {
	if !opts.Obfuscate || len(m.cfg.Obfuscation.Rules) == 0 {
		return nil
	}
	connStr := m.eng.ConnectionString(copyRuntime.Internal)
	return obfuscation.New(m.eng.Name(), connStr, m.cfg.Obfuscation.Rules).Apply(ctx)
}

type readyCopy struct {
	containerID string
	copyRuntime CopyRuntime
	warm        bool
}

func (m *Manager) markReady(c *store.Copy, ready readyCopy) error {
	now := time.Now()
	connStr := m.eng.ConnectionString(ready.copyRuntime.External)
	if err := m.copies.UpdateStatus(c.ID, store.StatusReady,
		store.WithConnectionString(connStr),
		store.WithReadyAt(now),
	); err != nil {
		return err
	}
	_ = m.events.Append("copy", c.ID, "ready", actor, map[string]any{"warm": ready.warm})

	c.Status = store.StatusReady
	c.ContainerID = ready.containerID
	c.ConnectionString = connStr
	c.ReadyAt = &now
	return nil
}

// startContainer creates and starts a Docker container for the copy.
// It bind-mounts the dump directory read-only at /dump.
type startContainerRequest struct {
	id          string
	dumpPath    string
	copyRuntime CopyRuntime
}

func (m *Manager) startContainer(ctx context.Context, req startContainerRequest) (string, error) {
	exposedPort := nat.Port(fmt.Sprintf("%d/tcp", m.eng.ContainerPort()))
	image := m.copyImage()
	if err := dockerutil.EnsureImage(ctx, m.docker, image); err != nil {
		return "", fmt.Errorf("container image %s: %w", image, err)
	}

	resp, err := m.docker.ContainerCreate(ctx,
		m.containerConfig(req, image, exposedPort),
		m.hostConfig(req, exposedPort),
		nil, nil,
		containerName(req.id),
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

func (m *Manager) copyImage() string {
	if m.cfg.CopyImage != "" {
		return m.cfg.CopyImage
	}
	return m.eng.ContainerImage()
}

func (m *Manager) containerConfig(req startContainerRequest, image string, exposedPort nat.Port) *container.Config {
	spec := m.eng.ContainerSpec(req.copyRuntime.Bootstrap)
	return &container.Config{
		Image: image,
		Env:   spec.Env,
		Cmd:   spec.Cmd,
		ExposedPorts: nat.PortSet{
			exposedPort: struct{}{},
		},
		Labels: map[string]string{
			labelManaged: "true",
			labelCopyID:  req.id,
		},
	}
}

func (m *Manager) hostConfig(req startContainerRequest, exposedPort nat.Port) *container.HostConfig {
	return &container.HostConfig{
		PortBindings: nat.PortMap{
			exposedPort: []nat.PortBinding{
				{HostIP: req.copyRuntime.BindHost, HostPort: fmt.Sprintf("%d", req.copyRuntime.External.Port)},
			},
		},
		Mounts: m.copyMounts(req),
	}
}

func (m *Manager) copyMounts(req startContainerRequest) []mount.Mount {
	mounts := []mount.Mount{
		{
			Type:     mount.TypeBind,
			Source:   filepath.Dir(req.dumpPath),
			Target:   "/dump",
			ReadOnly: true,
		},
	}
	if !req.copyRuntime.Bootstrap.TLSEnabled {
		return mounts
	}
	return append(mounts,
		mount.Mount{
			Type:     mount.TypeBind,
			Source:   m.runtime.CertFile,
			Target:   "/run/ditto/tls/server.crt",
			ReadOnly: true,
		},
		mount.Mount{
			Type:     mount.TypeBind,
			Source:   m.runtime.KeyFile,
			Target:   "/run/ditto/tls/server.key",
			ReadOnly: true,
		},
	)
}

func containerName(id string) string { return "ditto-" + id }

func intPtr(n int) *int { return &n }

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

func (m *Manager) copyRuntime(id string, port int) (CopyRuntime, error) {
	if m.runtime.Mode == AccessModeRemote {
		return remoteRuntime(port, m.runtime, id)
	}
	return localRuntime(port), nil
}
