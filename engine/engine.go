package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/client"
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

	// Dump writes a compressed full dump of src to destPath through the
	// configured Docker-compatible runtime. clientImage overrides the helper
	// image used for the dump when non-empty.
	Dump(ctx context.Context, docker *client.Client, clientImage string, src SourceConfig, destPath string) error

	// Restore loads a dump file into a running container through the
	// configured Docker-compatible runtime.
	// containerName is the Docker container name (e.g. "ditto-<id>") as set by the
	// copy manager. The manager calls WaitReady before Restore, so the container is
	// guaranteed to be accepting connections when this method is invoked.
	Restore(ctx context.Context, docker *client.Client, dumpPath string, containerName string) error

	// DumpFromContainer creates a compressed dump of the database running inside
	// containerName and writes it to destPath on the host. The container must
	// have its dump directory mounted at /dump (as copy and staging containers do).
	// Used by the dump scheduler to bake obfuscation into the dump file.
	DumpFromContainer(ctx context.Context, docker *client.Client, containerName string, destPath string) error

	// ContainerEnv returns the environment variables needed to initialise the
	// database inside a copy or staging container.
	ContainerEnv() []string

	// ContainerImage returns the Docker image (with pinned tag) for copies.
	ContainerImage() string

	// WaitReady blocks until the database in the container on port is ready
	// to accept connections, or until timeout elapses.
	WaitReady(port int, timeout time.Duration) error

	// ConnectionString returns a DSN for connecting to the copy on port.
	ConnectionString(host string, port int) string
}

// ValidateSourceHost rejects loopback addresses that are unreachable from
// dump helper containers. Both the postgres and mysql engines call this.
func ValidateSourceHost(host string) error {
	trimmed := strings.TrimSpace(strings.ToLower(host))
	switch trimmed {
	case "", "localhost", "127.0.0.1", "::1":
		return fmt.Errorf("source host %q is not reachable from dump helper containers; use a network-reachable hostname or service address", host)
	default:
		return nil
	}
}
