package engine

import (
	"context"
	"time"
)

// SourceConfig holds the connection parameters for the source database.
type SourceConfig struct {
	Host           string
	Port           int
	Database       string
	User           string
	Password       string // resolved from PasswordSecret at runtime
	PasswordSecret string // secret reference: env:VAR, file:/path, or arn:aws:...
}

// Engine is the interface that each database engine must implement.
// All engine-specific behaviour (dump, restore, readiness, connection strings)
// lives behind this interface; the copy manager never imports engine packages
// directly.
type Engine interface {
	// Name returns the identifier used in ditto.yaml (e.g. "postgres", "mysql").
	Name() string

	// Dump writes a compressed full dump of src to destPath.
	Dump(ctx context.Context, src SourceConfig, destPath string) error

	// Restore loads a dump file into a running container.
	// containerName is the Docker container name (e.g. "ditto-<id>") as set by the
	// copy manager. The manager calls WaitReady before Restore, so the container is
	// guaranteed to be accepting connections when this method is invoked.
	Restore(ctx context.Context, dumpPath string, containerName string) error

	// ContainerImage returns the Docker image (with pinned tag) for copies.
	ContainerImage() string

	// WaitReady blocks until the database in the container on port is ready
	// to accept connections, or until timeout elapses.
	WaitReady(port int, timeout time.Duration) error

	// ConnectionString returns a DSN for connecting to the copy on port.
	ConnectionString(host string, port int) string
}
