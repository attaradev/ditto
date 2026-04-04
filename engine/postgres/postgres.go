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

func (e *Engine) ContainerEnv() []string {
	return []string{
		"POSTGRES_USER=ditto",
		"POSTGRES_PASSWORD=ditto",
		"POSTGRES_DB=ditto",
	}
}

func (e *Engine) ConnectionString(host string, port int) string {
	return fmt.Sprintf("postgres://ditto:ditto@%s:%d/ditto?sslmode=disable", host, port)
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

	if err := dockerutil.RunContainer(ctx, docker,
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
func (e *Engine) Restore(ctx context.Context, docker *client.Client, dumpPath string, containerName string) error {
	if docker == nil {
		return fmt.Errorf("postgres: docker runtime is required")
	}
	if err := dockerutil.Exec(ctx, docker, containerName, []string{
		"pg_restore",
		"--username=ditto",
		"--dbname=ditto",
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
func (e *Engine) DumpFromContainer(ctx context.Context, docker *client.Client, containerName string, destPath string, opts engine.DumpOptions) error {
	if docker == nil {
		return fmt.Errorf("postgres: docker runtime is required")
	}
	cmd := []string{
		"pg_dump",
		"--username=ditto",
		"--dbname=ditto",
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
func (e *Engine) WaitReady(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("localhost:%d", port)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if time.Now().After(deadline) {
		return fmt.Errorf("postgres: timed out waiting for TCP on port %d", port)
	}

	// Open one connection pool and reuse it across iterations.
	dsn := fmt.Sprintf("postgres://ditto:ditto@localhost:%d/ditto?sslmode=disable", port)
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
	return fmt.Errorf("postgres: timed out waiting for SELECT 1 on port %d", port)
}

// Ensure Engine satisfies the interface at compile time.
var _ engine.Engine = (*Engine)(nil)
