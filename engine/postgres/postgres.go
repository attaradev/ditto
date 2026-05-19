// Package postgres implements the ditto Engine interface for PostgreSQL.
// It registers itself via init() so that a blank import in main.go is
// sufficient to make the engine available.
package postgres

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"time"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/engine/internal/engineutil"
	"github.com/attaradev/ditto/internal/dockerutil"
	"github.com/attaradev/ditto/internal/secret"
	"github.com/docker/docker/api/types/container"
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
	req engine.DumpRequest,
) error {
	if err := engineutil.RequireDocker("postgres", req.Docker); err != nil {
		return err
	}
	src := req.Source
	if err := engine.ValidateSourceHost(src.Host); err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	password, err := e.secretCache.Resolve(ctx, src.PasswordSecret, src.Password)
	if err != nil {
		return fmt.Errorf("postgres: resolve password: %w", err)
	}
	clientImage := engineutil.ClientImage(req.ClientImage, e.ContainerImage())

	cmd := []string{
		"--format=custom",
		"--compress=9",
		"--no-owner",
		"--no-acl",
		"--file=" + path.Join("/dump", filepath.Base(req.DestPath)),
		"--host=" + src.Host,
		fmt.Sprintf("--port=%d", src.Port),
		"--dbname=" + src.Database,
		"--username=" + src.User,
	}
	if req.Options.SchemaOnly {
		cmd = append(cmd, "--schema-only")
	}
	for _, t := range req.Options.ExcludeTableData {
		cmd = append(cmd, "--exclude-table-data="+t)
	}

	if err := dockerutil.RunContainer(ctx, req.Docker, dockerutil.RunRequest{
		Config: &container.Config{
			Image:      clientImage,
			Entrypoint: []string{"pg_dump"},
			Cmd:        cmd,
			Env:        []string{"PGPASSWORD=" + password},
		},
		HostConfig:    engineutil.DumpHostConfig(req.DestPath),
		NetworkConfig: engineutil.NetworkConfig(src.NetworkName),
	}); err != nil {
		return fmt.Errorf("postgres: dump helper failed: %w", err)
	}
	return nil
}

// Restore calls pg_restore inside the running container by exec-ing into it.
// containerName is the Docker container name (e.g. "ditto-<id>") as set by the
// copy manager. The manager calls WaitReady before Restore, so readiness is
// already guaranteed when this method is invoked.
func (e *Engine) Restore(ctx context.Context, req engine.RestoreRequest) error {
	if err := engineutil.RequireDocker("postgres", req.Docker); err != nil {
		return err
	}
	if err := dockerutil.Exec(ctx, req.Docker, dockerutil.ExecRequest{
		ContainerID: req.ContainerName,
		Command: []string{
			"pg_restore",
			"--username=" + req.Copy.User,
			"--dbname=" + req.Copy.Database,
			"--no-owner",
			"--no-acl",
			"/dump/" + filepath.Base(req.DumpPath),
		},
	}); err != nil {
		return fmt.Errorf("postgres: restore failed: %w", err)
	}
	return nil
}

// DumpFromContainer creates a compressed dump of the ditto database running
// inside containerName and writes it to destPath on the host.
// The container must have its dump directory mounted at /dump (read-write).
// When opts.SchemaOnly is true, --schema-only is passed to pg_dump.
func (e *Engine) DumpFromContainer(ctx context.Context, req engine.DumpFromContainerRequest) error {
	if err := engineutil.RequireDocker("postgres", req.Docker); err != nil {
		return err
	}
	cmd := []string{
		"pg_dump",
		"--username=" + req.Copy.User,
		"--dbname=" + req.Copy.Database,
		"--format=custom",
		"--compress=9",
		"--no-owner",
		"--no-acl",
		"--file=/dump/" + filepath.Base(req.DestPath),
	}
	if req.Options.SchemaOnly {
		cmd = append(cmd, "--schema-only")
	}
	for _, t := range req.Options.ExcludeTableData {
		cmd = append(cmd, "--exclude-table-data="+t)
	}
	if err := dockerutil.Exec(ctx, req.Docker, dockerutil.ExecRequest{ContainerID: req.ContainerName, Command: cmd}); err != nil {
		return fmt.Errorf("postgres: dump from container failed: %w", err)
	}
	return nil
}

// WaitReady polls port until Postgres is accepting connections or timeout
// elapses. It first waits for TCP connectivity, then confirms with SELECT 1.
func (e *Engine) WaitReady(conn engine.ConnectionConfig, timeout time.Duration) error {
	return engineutil.WaitReady(engineutil.ReadyRequest{
		Prefix:     "postgres",
		DriverName: "pgx",
		Connection: conn,
		Timeout:    timeout,
		DSN:        e.ConnectionString(conn),
	})
}

// Ensure Engine satisfies the interface at compile time.
var _ engine.Engine = (*Engine)(nil)
