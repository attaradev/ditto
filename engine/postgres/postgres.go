// Package postgres implements the ditto Engine interface for PostgreSQL.
// It registers itself via init() so that a blank import in main.go is
// sufficient to make the engine available.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"sync"
	"time"

	"github.com/attaradev/ditto/engine"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx stdlib driver for database/sql
)

func init() {
	engine.Register(&Engine{})
}

// Engine implements engine.Engine for PostgreSQL.
type Engine struct {
	// secretCache caches resolved AWS Secrets Manager passwords.
	secretCache secretCache
}

func (e *Engine) Name() string { return "postgres" }

func (e *Engine) ContainerImage() string { return "postgres:16-alpine" }

func (e *Engine) ConnectionString(host string, port int) string {
	return fmt.Sprintf("postgres://ditto:ditto@%s:%d/ditto?sslmode=disable", host, port)
}

// Dump runs pg_dump against src and writes a custom-format compressed dump
// to destPath. Uses exec.CommandContext so context cancellation kills the
// subprocess.
func (e *Engine) Dump(ctx context.Context, src engine.SourceConfig, destPath string) error {
	password, err := e.resolvePassword(ctx, src)
	if err != nil {
		return fmt.Errorf("postgres: resolve password: %w", err)
	}

	dsn := fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=require",
		src.Host, src.Port, src.Database, src.User, password)

	// #nosec G204 -- pg_dump is invoked without a shell and config values are passed as argv.
	cmd := exec.CommandContext(ctx,
		"pg_dump",
		"--format=custom",
		"--compress=9",
		"--no-owner",
		"--no-acl",
		"--file="+destPath,
		dsn,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("postgres: pg_dump failed: %w\n%s", err, out)
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

	// Confirm Postgres is accepting queries, not just TCP connections.
	dsn := fmt.Sprintf("postgres://ditto:ditto@localhost:%d/ditto?sslmode=disable", port)
	for time.Now().Before(deadline) {
		db, err := sql.Open("pgx", dsn)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, pingErr := db.ExecContext(ctx, "SELECT 1")
			cancel()
			closeErr := db.Close()
			if pingErr == nil {
				if closeErr != nil {
					return fmt.Errorf("postgres: close readiness probe DB: %w", closeErr)
				}
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("postgres: timed out waiting for SELECT 1 on port %d", port)
}

// resolvePassword returns the password for src. If PasswordSecret is set it
// fetches the value from AWS Secrets Manager (with a 5-minute cache).
// Otherwise it returns Password directly.
func (e *Engine) resolvePassword(ctx context.Context, src engine.SourceConfig) (string, error) {
	if src.PasswordSecret == "" {
		return src.Password, nil
	}
	return e.secretCache.get(ctx, src.PasswordSecret)
}

// secretCache is a simple time-bounded cache for a single Secrets Manager
// secret value.
type secretCache struct {
	mu        sync.RWMutex
	arn       string
	value     string
	fetchedAt time.Time
}

func (c *secretCache) get(ctx context.Context, arn string) (string, error) {
	const ttl = 5 * time.Minute

	c.mu.RLock()
	if c.arn == arn && time.Since(c.fetchedAt) < ttl {
		v := c.value
		c.mu.RUnlock()
		return v, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	// Re-check after acquiring write lock.
	if c.arn == arn && time.Since(c.fetchedAt) < ttl {
		return c.value, nil
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("load AWS config: %w", err)
	}
	svc := secretsmanager.NewFromConfig(cfg)
	out, err := svc.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: &arn,
	})
	if err != nil {
		return "", fmt.Errorf("get secret %s: %w", arn, err)
	}

	raw := ""
	if out.SecretString != nil {
		raw = *out.SecretString
	}

	// The secret may be a raw string or a JSON object with a "password" key.
	password := raw
	var obj map[string]string
	if json.Unmarshal([]byte(raw), &obj) == nil {
		if p, ok := obj["password"]; ok {
			password = p
		}
	}

	c.arn = arn
	c.value = password
	c.fetchedAt = time.Now()
	return password, nil
}

// Ensure Engine satisfies the interface at compile time.
var _ engine.Engine = (*Engine)(nil)
