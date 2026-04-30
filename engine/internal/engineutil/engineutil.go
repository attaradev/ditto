// Package engineutil contains shared helpers for database engine implementations.
package engineutil

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"path/filepath"
	"time"

	"github.com/attaradev/ditto/engine"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// RequireDocker preserves each engine's existing nil-runtime validation.
func RequireDocker(prefix string, docker *client.Client) error {
	if docker == nil {
		return fmt.Errorf("%s: docker runtime is required", prefix)
	}
	return nil
}

// ClientImage returns the configured dump helper image or the engine fallback.
func ClientImage(configured, fallback string) string {
	if configured != "" {
		return configured
	}
	return fallback
}

// NetworkConfig attaches helper containers to the source network when needed.
func NetworkConfig(name string) *network.NetworkingConfig {
	if name == "" {
		return nil
	}
	return &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			name: {},
		},
	}
}

// DumpHostConfig mounts the dump directory at /dump for helper containers.
func DumpHostConfig(destPath string) *container.HostConfig {
	return &container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: filepath.Dir(destPath),
				Target: "/dump",
			},
		},
	}
}

// ReadyRequest contains the engine-specific details needed by WaitReady.
type ReadyRequest struct {
	Prefix     string
	DriverName string
	Connection engine.ConnectionConfig
	Timeout    time.Duration
	DSN        string
}

// WaitReady polls TCP first, then SELECT 1, matching the engine-specific
// behavior that used to live in each implementation.
func WaitReady(req ReadyRequest) error {
	deadline := time.Now().Add(req.Timeout)
	addr := net.JoinHostPort(req.Connection.Host, fmt.Sprintf("%d", req.Connection.Port))

	if !pollUntil(deadline, func() bool {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}) {
		return fmt.Errorf("%s: timed out waiting for TCP on port %d", req.Prefix, req.Connection.Port)
	}

	db, err := sql.Open(req.DriverName, req.DSN)
	if err != nil {
		return fmt.Errorf("%s: open readiness probe DB: %w", req.Prefix, err)
	}
	defer func() { _ = db.Close() }()

	if !pollUntil(deadline, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := db.ExecContext(ctx, "SELECT 1")
		return err == nil
	}) {
		return fmt.Errorf("%s: timed out waiting for SELECT 1 on port %d", req.Prefix, req.Connection.Port)
	}
	return nil
}

func pollUntil(deadline time.Time, probe func() bool) bool {
	for time.Now().Before(deadline) {
		if probe() {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}
