package copy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/config"
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
	cfg    *config.Config
	eng    engine.Engine
	copies *store.CopyStore
	events *store.EventStore
	ports  *PortPool
	docker *client.Client
}

// NewManager creates a Manager. The Docker client is created with API version
// negotiation so it works against different daemon versions.
func NewManager(
	cfg *config.Config,
	eng engine.Engine,
	copies *store.CopyStore,
	events *store.EventStore,
	ports *PortPool,
) (*Manager, error) {
	docker, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("copy: create docker client: %w", err)
	}
	return &Manager{
		cfg:    cfg,
		eng:    eng,
		copies: copies,
		events: events,
		ports:  ports,
		docker: docker,
	}, nil
}

// CreateOptions configures a copy creation request.
type CreateOptions struct {
	TTLSeconds int
	GHARunID   string
	GHAJobName string
}

// Create provisions a new ephemeral database copy. It:
//  1. Checks the dump file exists and warns if stale
//  2. Allocates a free port
//  3. Starts a fresh Docker container
//  4. Waits for the engine to be ready
//  5. Restores the dump into the container
//  6. Records the copy in SQLite and returns it
func (m *Manager) Create(ctx context.Context, opts CreateOptions) (*store.Copy, error) {
	dumpPath := m.cfg.Dump.Path
	if err := checkDump(dumpPath, m.cfg.Dump.StaleThreshold); err != nil {
		return nil, err
	}

	port, err := m.ports.Allocate()
	if err != nil {
		return nil, fmt.Errorf("copy.Create: %w", err)
	}

	id := ulid.Make().String()
	ttl := opts.TTLSeconds
	if ttl == 0 {
		ttl = m.cfg.CopyTTLSeconds
	}

	c := &store.Copy{
		ID:         id,
		Status:     store.StatusPending,
		Port:       port,
		GHARunID:   opts.GHARunID,
		GHAJobName: opts.GHAJobName,
		TTLSeconds: ttl,
	}

	// Cleanup on any error after port allocation.
	var containerStarted bool
	cleanup := func(cause error) {
		if containerStarted {
			_ = m.docker.ContainerStop(context.Background(), containerName(id), container.StopOptions{Timeout: intPtr(10)})
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
	_ = m.events.Append("copy", id, "created", actor, map[string]any{"port": port})

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
	if err := m.eng.Restore(ctx, dumpPath, port); err != nil {
		cleanup(err)
		return nil, fmt.Errorf("copy.Create restore: %w", err)
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
	_ = m.events.Append("copy", id, "ready", actor, map[string]any{"connection_string": connStr})

	c.Status = store.StatusReady
	c.ContainerID = containerID
	c.ConnectionString = connStr
	c.ReadyAt = &now
	return c, nil
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

// startContainer creates and starts a Docker container for the copy.
// It bind-mounts the dump directory read-only at /dump.
func (m *Manager) startContainer(ctx context.Context, id string, port int, dumpPath string) (string, error) {
	dumpDir := dumpDir(dumpPath)
	portStr := fmt.Sprintf("%d", port)
	exposedPort := nat.Port(portStr + "/tcp")

	resp, err := m.docker.ContainerCreate(ctx,
		&container.Config{
			Image: m.eng.ContainerImage(),
			Env:   containerEnv(m.eng.Name()),
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

// containerEnv returns the Docker env vars that configure the database user
// and password inside the container. The copy always uses a fixed
// user/password/dbname so the connection string is predictable.
func containerEnv(engineName string) []string {
	switch engineName {
	case "postgres":
		return []string{
			"POSTGRES_USER=ditto",
			"POSTGRES_PASSWORD=ditto",
			"POSTGRES_DB=ditto",
		}
	case "mariadb":
		return []string{
			"MARIADB_USER=ditto",
			"MARIADB_PASSWORD=ditto",
			"MARIADB_DATABASE=ditto",
			"MARIADB_ROOT_PASSWORD=ditto-root",
		}
	default:
		return nil
	}
}

func containerName(id string) string { return "ditto-" + id }

func dumpDir(dumpPath string) string {
	for i := len(dumpPath) - 1; i >= 0; i-- {
		if dumpPath[i] == '/' || dumpPath[i] == os.PathSeparator {
			return dumpPath[:i]
		}
	}
	return "."
}

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

func intPtr(i int) *int { return &i }
