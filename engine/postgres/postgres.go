// Package postgres implements the ditto Engine interface for PostgreSQL.
// It registers itself via init() so that a blank import in main.go is
// sufficient to make the engine available.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"path"
	"path/filepath"
	"time"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/dockerutil"
	"github.com/attaradev/ditto/internal/secret"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx stdlib driver for database/sql
)

func init() {
	engine.Register(&Engine{})
}

// Engine implements engine.Engine for PostgreSQL.
type Engine struct {
	secretCache secret.Cache
}

func (e *Engine) Name() string { return "postgres" }

func (e *Engine) ContainerImage() string { return "postgres:16-alpine" }

func (e *Engine) ContainerPort() int { return 5432 }

func (e *Engine) ContainerSpec(copy engine.CopyBootstrap) engine.ContainerSpec {
	spec := engine.ContainerSpec{
		Env: []string{
			"POSTGRES_USER=" + copy.User,
			"POSTGRES_PASSWORD=" + copy.Password,
			"POSTGRES_DB=" + copy.Database,
		},
	}
	if copy.TLSEnabled {
		spec.Cmd = []string{
			"-c", "ssl=on",
			"-c", "ssl_cert_file=/run/ditto/tls/server.crt",
			"-c", "ssl_key_file=/run/ditto/tls/server.key",
		}
	}
	return spec
}

func (e *Engine) ConnectionString(conn engine.ConnectionConfig) string {
	sslMode := "disable"
	if conn.TLSEnabled {
		sslMode = "verify-full"
	}
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		conn.User,
		conn.Password,
		conn.Host,
		conn.Port,
		conn.Database,
		sslMode,
	)
}

// Dump runs pg_dump inside a short-lived helper container and writes a
// custom-format compressed dump to destPath. When opts.SchemaOnly is true,
// --schema-only is passed so only DDL is captured (no row data).
func (e *Engine) Dump(
	ctx context.Context,
	docker *client.Client,
	clientImage string,
	src engine.SourceConfig,
	destPath string,
	opts engine.DumpOptions,
) error {
	if docker == nil {
		return fmt.Errorf("postgres: docker runtime is required")
	}
	if err := engine.ValidateSourceHost(src.Host); err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	password, err := e.secretCache.Resolve(ctx, src.PasswordSecret, src.Password)
	if err != nil {
		return fmt.Errorf("postgres: resolve password: %w", err)
	}
	if clientImage == "" {
		clientImage = e.ContainerImage()
	}

	cmd := []string{
		"--format=custom",
		"--compress=9",
		"--no-owner",
		"--no-acl",
		"--file=" + path.Join("/dump", filepath.Base(destPath)),
		"--host=" + src.Host,
		fmt.Sprintf("--port=%d", src.Port),
		"--dbname=" + src.Database,
		"--username=" + src.User,
	}
	if opts.SchemaOnly {
		cmd = append(cmd, "--schema-only")
	}

	var netCfg *network.NetworkingConfig
	if src.NetworkName != "" {
		netCfg = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				src.NetworkName: {},
			},
		}
	}
	if err := dockerutil.RunContainerOnNetwork(ctx, docker,
		&container.Config{
			Image:      clientImage,
			Entrypoint: []string{"pg_dump"},
			Cmd:        cmd,
			Env:        []string{"PGPASSWORD=" + password},
		},
		&container.HostConfig{
			Mounts: []mount.Mount{
				{
					Type:   mount.TypeBind,
					Source: filepath.Dir(destPath),
					Target: "/dump",
				},
			},
		},
		netCfg,
		"",
	); err != nil {
		return fmt.Errorf("postgres: dump helper failed: %w", err)
	}
	return nil
}

// Restore calls pg_restore inside the running container by exec-ing into it.
// containerName is the Docker container name (e.g. "ditto-<id>") as set by the
// copy manager. The manager calls WaitReady before Restore, so readiness is
// already guaranteed when this method is invoked.
func (e *Engine) Restore(ctx context.Context, docker *client.Client, dumpPath string, containerName string, copy engine.CopyBootstrap) error {
	if docker == nil {
		return fmt.Errorf("postgres: docker runtime is required")
	}
	if err := dockerutil.Exec(ctx, docker, containerName, []string{
		"pg_restore",
		"--username=" + copy.User,
		"--dbname=" + copy.Database,
		"--no-owner",
		"--no-acl",
		"/dump/" + filepath.Base(dumpPath),
	}, nil); err != nil {
		return fmt.Errorf("postgres: restore failed: %w", err)
	}
	return nil
}

// DumpFromContainer creates a compressed dump of the ditto database running
// inside containerName and writes it to destPath on the host.
// The container must have its dump directory mounted at /dump (read-write).
// When opts.SchemaOnly is true, --schema-only is passed to pg_dump.
func (e *Engine) DumpFromContainer(ctx context.Context, docker *client.Client, containerName string, destPath string, copy engine.CopyBootstrap, opts engine.DumpOptions) error {
	if docker == nil {
		return fmt.Errorf("postgres: docker runtime is required")
	}
	cmd := []string{
		"pg_dump",
		"--username=" + copy.User,
		"--dbname=" + copy.Database,
		"--format=custom",
		"--compress=9",
		"--no-owner",
		"--no-acl",
		"--file=/dump/" + filepath.Base(destPath),
	}
	if opts.SchemaOnly {
		cmd = append(cmd, "--schema-only")
	}
	if err := dockerutil.Exec(ctx, docker, containerName, cmd, nil); err != nil {
		return fmt.Errorf("postgres: dump from container failed: %w", err)
	}
	return nil
}

// WaitReady polls port until Postgres is accepting connections or timeout
// elapses. It first waits for TCP connectivity, then confirms with SELECT 1.
func (e *Engine) WaitReady(conn engine.ConnectionConfig, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("%s:%d", conn.Host, conn.Port)

	for time.Now().Before(deadline) {
		tcpConn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = tcpConn.Close()
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if time.Now().After(deadline) {
		return fmt.Errorf("postgres: timed out waiting for TCP on port %d", conn.Port)
	}

	// Open one connection pool and reuse it across iterations.
	dsn := e.ConnectionString(conn)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("postgres: open readiness probe DB: %w", err)
	}
	defer func() { _ = db.Close() }()

	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := db.ExecContext(ctx, "SELECT 1")
		cancel()
		if err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("postgres: timed out waiting for SELECT 1 on port %d", conn.Port)
}

// Ensure Engine satisfies the interface at compile time.
var _ engine.Engine = (*Engine)(nil)
