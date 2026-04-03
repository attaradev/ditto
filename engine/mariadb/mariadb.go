// Package mariadb implements the ditto Engine interface for MariaDB/MySQL.
// It registers itself via init() so that a blank import in main.go is
// sufficient to make the engine available.
package mariadb

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/secret"
	_ "github.com/go-sql-driver/mysql"
)

func init() {
	engine.Register(&Engine{})
}

// Engine implements engine.Engine for MariaDB/MySQL.
type Engine struct {
	secretCache secret.Cache
}

func (e *Engine) Name() string { return "mariadb" }

func (e *Engine) ContainerImage() string { return "mariadb:11.4" }

func (e *Engine) ConnectionString(host string, port int) string {
	return fmt.Sprintf("ditto:ditto@tcp(%s:%d)/ditto", host, port)
}

// Dump runs mysqldump against src and writes a compressed dump to destPath.
// The password is passed via MYSQL_PWD to avoid it appearing in process listings.
func (e *Engine) Dump(ctx context.Context, src engine.SourceConfig, destPath string) error {
	password, err := e.secretCache.Resolve(ctx, src.PasswordSecret, src.Password)
	if err != nil {
		return fmt.Errorf("mariadb: resolve password: %w", err)
	}

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("mariadb: create dump file: %w", err)
	}

	gz := gzip.NewWriter(f)

	// #nosec G204 -- mysqldump is invoked without a shell and config values are passed as argv.
	cmd := exec.CommandContext(ctx,
		"mysqldump",
		"--single-transaction",
		"--routines",
		"--triggers",
		"--compress",
		"-h", src.Host,
		"-P", fmt.Sprint(src.Port),
		"-u", src.User,
		src.Database,
	)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+password)
	cmd.Stdout = gz
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = gz.Close()
		_ = f.Close()
		return fmt.Errorf("mariadb: mysqldump failed: %w\n%s", err, stderr.Bytes())
	}
	if err := gz.Close(); err != nil {
		_ = f.Close()
		return fmt.Errorf("mariadb: finalize gzip dump: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("mariadb: close dump file: %w", err)
	}
	return nil
}

// Restore calls mysql inside the container to load the dump file.
// The dump directory is bind-mounted at /dump inside the container.
func (e *Engine) Restore(ctx context.Context, dumpPath string, port int) error {
	if err := e.WaitReady(port, 2*time.Minute); err != nil {
		return fmt.Errorf("mariadb: container not ready before restore: %w", err)
	}

	f, err := os.Open(dumpPath)
	if err != nil {
		return fmt.Errorf("mariadb: open dump file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("mariadb: open gzip reader: %w", err)
	}
	defer func() {
		_ = gz.Close()
	}()

	containerName := fmt.Sprintf("ditto-%d", port)
	// #nosec G204 -- docker is invoked without a shell and the container name is internally generated.
	cmd := exec.CommandContext(ctx,
		"docker", "exec", "-i", containerName,
		"mysql", "-u", "ditto", "-pditto", "ditto",
	)
	cmd.Stdin = gz
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
			_ = conn.Close()
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if time.Now().After(deadline) {
		return fmt.Errorf("mariadb: timed out waiting for TCP on port %d", port)
	}

	// Open one connection pool and reuse it across iterations.
	dsn := fmt.Sprintf("ditto:ditto@tcp(localhost:%d)/ditto", port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("mariadb: open readiness probe DB: %w", err)
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
	return fmt.Errorf("mariadb: timed out waiting for SELECT 1 on port %d", port)
}

var _ engine.Engine = (*Engine)(nil)
