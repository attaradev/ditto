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
	// NetworkName, when non-empty, attaches the dump helper container to this
	// Docker network so it can reach the source by container hostname. Leave
	// empty when the source is a real network-addressable host.
	NetworkName string
}

// DumpOptions controls optional dump behaviour. The zero value produces a
// full dump (schema + data), which is the default for all call sites.
type DumpOptions struct {
	SchemaOnly bool // when true, dump DDL only — no row data
}

// CopyBootstrap describes the database users and TLS settings used to
// initialize a copy or staging container.
type CopyBootstrap struct {
	Database     string
	User         string
	Password     string
	RootPassword string
	TLSEnabled   bool
}

// ConnectionConfig describes how callers connect to a running copy.
type ConnectionConfig struct {
	Host       string
	Port       int
	Database   string
	User       string
	Password   string
	TLSEnabled bool
}

// ContainerSpec captures the engine-specific environment variables and command
// line needed to bootstrap a container.
type ContainerSpec struct {
	Env []string
	Cmd []string
}

// Engine is the interface that each database engine must implement.
// All engine-specific behaviour (dump, restore, readiness, connection strings)
// lives behind this interface; the copy manager never imports engine packages
// directly.
type Engine interface {
	// Name returns the identifier used in ditto.yaml (e.g. "postgres", "mysql").
	Name() string

	// Dump writes a compressed dump of src to destPath through the configured
	// Docker-compatible runtime. clientImage overrides the helper image used for
	// the dump when non-empty. opts controls whether the dump includes row data.
	Dump(ctx context.Context, docker *client.Client, clientImage string, src SourceConfig, destPath string, opts DumpOptions) error

	// DumpFromContainer creates a compressed dump of the database running inside
	// containerName and writes it to destPath on the host. The container must
	// have its dump directory mounted at /dump (as copy and staging containers do).
	// Used by the dump scheduler to bake obfuscation into the dump file.
	// opts controls whether the dump includes row data.
	DumpFromContainer(ctx context.Context, docker *client.Client, containerName string, destPath string, copy CopyBootstrap, opts DumpOptions) error

	// ContainerSpec returns the environment variables and optional command line
	// used to initialize the database inside a copy or staging container.
	ContainerSpec(copy CopyBootstrap) ContainerSpec

	// ContainerImage returns the Docker image (with pinned tag) for copies.
	ContainerImage() string

	// ContainerPort returns the TCP port the database listens on inside the container.
	ContainerPort() int

	// WaitReady blocks until the database in the container on port is ready
	// to accept connections, or until timeout elapses.
	WaitReady(conn ConnectionConfig, timeout time.Duration) error

	// ConnectionString returns a DSN for connecting to the copy on port.
	ConnectionString(conn ConnectionConfig) string

	// Restore loads a dump file into a running container through the configured
	// Docker-compatible runtime. containerName is the Docker container name
	// (e.g. "ditto-<id>") as set by the copy manager.
	Restore(ctx context.Context, docker *client.Client, dumpPath string, containerName string, copy CopyBootstrap) error
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
