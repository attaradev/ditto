// Package mysql implements the ditto Engine interface for MySQL and MariaDB.
// It registers itself via init() so that a blank import in main.go is
// sufficient to make the engine available.
package mysql

import (
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/dockerutil"
	"github.com/attaradev/ditto/internal/secret"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	_ "github.com/go-sql-driver/mysql"
)

func init() {
	engine.Register(&Engine{})
}

// Engine implements engine.Engine for MySQL / MariaDB.
type Engine struct {
	secretCache secret.Cache
}

func (e *Engine) Name() string { return "mysql" }

func (e *Engine) ContainerImage() string { return "mysql:8.4" }

func (e *Engine) ContainerEnv() []string {
	return []string{
		"MYSQL_USER=ditto",
		"MYSQL_PASSWORD=ditto",
		"MYSQL_DATABASE=ditto",
		"MYSQL_ROOT_PASSWORD=ditto-root",
	}
}

func (e *Engine) ConnectionString(host string, port int) string {
	return fmt.Sprintf("ditto:ditto@tcp(%s:%d)/ditto", host, port)
}

// Dump runs mysqldump inside a short-lived helper container, then compresses
// the resulting SQL dump to destPath.
func (e *Engine) Dump(
	ctx context.Context,
	docker *client.Client,
	clientImage string,
	src engine.SourceConfig,
	destPath string,
) error {
	if docker == nil {
		return fmt.Errorf("mysql: docker runtime is required")
	}
	if err := validateSourceHost(src.Host); err != nil {
		return fmt.Errorf("mysql: %w", err)
	}
	password, err := e.secretCache.Resolve(ctx, src.PasswordSecret, src.Password)
	if err != nil {
		return fmt.Errorf("mysql: resolve password: %w", err)
	}
	if clientImage == "" {
		clientImage = e.ContainerImage()
	}

	sqlDumpPath := destPath + ".sql"
	if err := dockerutil.RunContainer(ctx, docker,
		&container.Config{
			Image:      clientImage,
			Entrypoint: []string{"mysqldump"},
			Cmd: []string{
				"--single-transaction",
				"--routines",
				"--triggers",
				"--compress",
				"--result-file=" + path.Join("/dump", filepath.Base(sqlDumpPath)),
				"-h", src.Host,
				"-P", fmt.Sprint(src.Port),
				"-u", src.User,
				src.Database,
			},
			Env: []string{"MYSQL_PWD=" + password},
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
		_ = os.Remove(sqlDumpPath)
		return fmt.Errorf("mysql: dump helper failed: %w", err)
	}
	defer func() {
		_ = os.Remove(sqlDumpPath)
	}()

	if err := gzipFile(sqlDumpPath, destPath); err != nil {
		_ = os.Remove(destPath)
		return err
	}
	return nil
}

// Restore calls mysql inside the container to load the dump file.
// containerName is the Docker container name (e.g. "ditto-<id>") as set by the
// copy manager. The manager calls WaitReady before Restore, so readiness is
// already guaranteed when this method is invoked.
func (e *Engine) Restore(ctx context.Context, docker *client.Client, dumpPath string, containerName string) error {
	if docker == nil {
		return fmt.Errorf("mysql: docker runtime is required")
	}
	f, err := os.Open(dumpPath)
	if err != nil {
		return fmt.Errorf("mysql: open dump file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("mysql: open gzip reader: %w", err)
	}
	defer func() {
		_ = gz.Close()
	}()

	if err := dockerutil.Exec(ctx, docker, containerName, []string{
		"mysql", "-u", "ditto", "-pditto", "ditto",
	}, gz); err != nil {
		return fmt.Errorf("mysql: restore failed: %w", err)
	}
	return nil
}

// DumpFromContainer creates a gzip-compressed mysqldump of the ditto database
// running inside containerName and writes it to destPath on the host.
// The container must have its dump directory mounted at /dump (read-write).
func (e *Engine) DumpFromContainer(ctx context.Context, docker *client.Client, containerName string, destPath string) error {
	if docker == nil {
		return fmt.Errorf("mysql: docker runtime is required")
	}
	sqlFile := filepath.Base(destPath) + ".sql"
	if err := dockerutil.Exec(ctx, docker, containerName, []string{
		"mysqldump", "-uditto", "-pditto",
		"--single-transaction", "--routines", "--triggers",
		"--result-file=/dump/" + sqlFile,
		"ditto",
	}, nil); err != nil {
		return fmt.Errorf("mysql: dump from container failed: %w", err)
	}

	hostSQLPath := filepath.Join(filepath.Dir(destPath), sqlFile)
	defer func() { _ = os.Remove(hostSQLPath) }()

	if err := gzipFile(hostSQLPath, destPath); err != nil {
		_ = os.Remove(destPath)
		return err
	}
	return nil
}

// WaitReady polls port until MySQL is accepting connections.
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
		return fmt.Errorf("mysql: timed out waiting for TCP on port %d", port)
	}

	dsn := fmt.Sprintf("ditto:ditto@tcp(localhost:%d)/ditto", port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("mysql: open readiness probe DB: %w", err)
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
	return fmt.Errorf("mysql: timed out waiting for SELECT 1 on port %d", port)
}

var _ engine.Engine = (*Engine)(nil)

func validateSourceHost(host string) error {
	trimmed := strings.TrimSpace(strings.ToLower(host))
	switch trimmed {
	case "", "localhost", "127.0.0.1", "::1":
		return fmt.Errorf("source host %q is not reachable from dump helper containers; use a network-reachable hostname or service address", host)
	default:
		return nil
	}
}

func gzipFile(srcPath string, destPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("mysql: open sql dump: %w", err)
	}
	defer func() {
		_ = src.Close()
	}()

	dest, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("mysql: create gzip dump: %w", err)
	}

	gz := gzip.NewWriter(dest)
	if _, err := io.Copy(gz, src); err != nil {
		_ = gz.Close()
		_ = dest.Close()
		return fmt.Errorf("mysql: compress dump: %w", err)
	}
	if err := gz.Close(); err != nil {
		_ = dest.Close()
		return fmt.Errorf("mysql: finalize gzip dump: %w", err)
	}
	if err := dest.Close(); err != nil {
		return fmt.Errorf("mysql: close gzip dump: %w", err)
	}
	return nil
}
