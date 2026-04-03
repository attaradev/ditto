// Package postgres implements the ditto Engine interface for PostgreSQL.
// It registers itself via init() so that a blank import in main.go is
// sufficient to make the engine available.
package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/secret"
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

func (e *Engine) ConnectionString(host string, port int) string {
	return fmt.Sprintf("postgres://ditto:ditto@%s:%d/ditto?sslmode=disable", host, port)
}

// Dump runs pg_dump against src and writes a custom-format compressed dump
// to destPath. Uses exec.CommandContext so context cancellation kills the
// subprocess. The password is passed via PGPASSWORD to avoid it appearing
// in process listings.
func (e *Engine) Dump(ctx context.Context, src engine.SourceConfig, destPath string) error {
	password, err := e.secretCache.Resolve(ctx, src.PasswordSecret, src.Password)
	if err != nil {
		return fmt.Errorf("postgres: resolve password: %w", err)
	}

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("postgres: create dump file: %w", err)
	}

	dsn := fmt.Sprintf("host=%s port=%d dbname=%s user=%s sslmode=require",
		src.Host, src.Port, src.Database, src.User)

	// #nosec G204 -- pg_dump is invoked without a shell and config values are passed as argv.
	cmd := exec.CommandContext(ctx,
		"pg_dump",
		"--format=custom",
		"--compress=9",
		"--no-owner",
		"--no-acl",
		dsn,
	)
	cmd.Stdout = f
	cmd.Env = append(os.Environ(), "PGPASSWORD="+password)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = f.Close()
		return fmt.Errorf("postgres: pg_dump failed: %w\n%s", err, stderr.Bytes())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("postgres: close dump file: %w", err)
	}
	return nil
}

// Restore calls pg_restore inside the running container by exec-ing into it.
// The dump directory is expected to be bind-mounted at /dump inside the
// container. This avoids network overhead and eliminates the need for
// pg_restore on the host.
//
// The actual Docker exec is performed by the copy manager, which calls this
// method after the container is started. The implementation here shells out
// directly to the Docker CLI for simplicity; Phase 2 will migrate to the
// Docker SDK exec API.
func (e *Engine) Restore(ctx context.Context, dumpPath string, port int) error {
	// Wait for the container to accept connections before restoring.
	if err := e.WaitReady(port, 2*time.Minute); err != nil {
		return fmt.Errorf("postgres: container not ready before restore: %w", err)
	}

	// pg_restore reads from /dump/latest.gz inside the container.
	// The container name is ditto-<port> by convention set by the manager.
	containerName := fmt.Sprintf("ditto-%d", port)
	// #nosec G204 -- docker is invoked without a shell and the container name is internally generated.
	cmd := exec.CommandContext(ctx,
		"docker", "exec", containerName,
		"pg_restore",
		"--username=ditto",
		"--dbname=ditto",
		"--no-owner",
		"--no-acl",
		"/dump/latest.gz",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("postgres: pg_restore failed: %w\n%s", err, out)
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
