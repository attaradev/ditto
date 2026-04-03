// Package mariadb implements the ditto Engine interface for MariaDB/MySQL.
// It registers itself via init() so that a blank import in main.go is
// sufficient to make the engine available.
package mariadb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	_ "github.com/go-sql-driver/mysql"
	"github.com/attaradev/ditto/engine"
)

func init() {
	engine.Register(&Engine{})
}

// Engine implements engine.Engine for MariaDB/MySQL.
type Engine struct {
	secretCache secretCache
}

func (e *Engine) Name() string { return "mariadb" }

func (e *Engine) ContainerImage() string { return "mariadb:11.4" }

func (e *Engine) ConnectionString(host string, port int) string {
	return fmt.Sprintf("ditto:ditto@tcp(%s:%d)/ditto", host, port)
}

// Dump runs mysqldump against src and writes a compressed dump to destPath.
func (e *Engine) Dump(ctx context.Context, src engine.SourceConfig, destPath string) error {
	password, err := e.resolvePassword(ctx, src)
	if err != nil {
		return fmt.Errorf("mariadb: resolve password: %w", err)
	}

	cmd := exec.CommandContext(ctx,
		"sh", "-c",
		fmt.Sprintf(
			"mysqldump --single-transaction --routines --triggers --compress"+
				" -h %s -P %d -u %s -p%s %s | gzip > %s",
			src.Host, src.Port, src.User, password, src.Database, destPath,
		),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mariadb: mysqldump failed: %w\n%s", err, out)
	}
	return nil
}

// Restore calls mysql inside the container to load the dump file.
// The dump directory is bind-mounted at /dump inside the container.
func (e *Engine) Restore(ctx context.Context, dumpPath string, port int) error {
	if err := e.WaitReady(port, 2*time.Minute); err != nil {
		return fmt.Errorf("mariadb: container not ready before restore: %w", err)
	}

	containerName := fmt.Sprintf("ditto-%d", port)
	cmd := exec.CommandContext(ctx,
		"docker", "exec", containerName,
		"sh", "-c",
		"zcat /dump/latest.gz | mysql -u ditto -pditto ditto",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mariadb: mysql restore failed: %w\n%s", err, out)
	}
	return nil
}

// WaitReady polls port until MariaDB is accepting connections.
func (e *Engine) WaitReady(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("localhost:%d", port)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if time.Now().After(deadline) {
		return fmt.Errorf("mariadb: timed out waiting for TCP on port %d", port)
	}

	dsn := fmt.Sprintf("ditto:ditto@tcp(localhost:%d)/ditto", port)
	for time.Now().Before(deadline) {
		db, err := sql.Open("mysql", dsn)
		if err == nil {
			ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, pingErr := db.ExecContext(ctx2, "SELECT 1")
			cancel()
			db.Close()
			if pingErr == nil {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("mariadb: timed out waiting for SELECT 1 on port %d", port)
}

func (e *Engine) resolvePassword(ctx context.Context, src engine.SourceConfig) (string, error) {
	if src.PasswordSecret == "" {
		return src.Password, nil
	}
	return e.secretCache.get(ctx, src.PasswordSecret)
}

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

var _ engine.Engine = (*Engine)(nil)
