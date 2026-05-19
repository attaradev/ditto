// Package mysql implements the ditto Engine interface for MySQL and MariaDB.
// It registers itself via init() so that a blank import in main.go is
// sufficient to make the engine available.
package mysql

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/engine/internal/engineutil"
	"github.com/attaradev/ditto/internal/dockerutil"
	"github.com/attaradev/ditto/internal/secret"
	"github.com/docker/docker/api/types/container"
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

func (e *Engine) ContainerPort() int { return 3306 }

func (e *Engine) ContainerSpec(copy engine.CopyBootstrap) engine.ContainerSpec {
	spec := engine.ContainerSpec{
		Env: []string{
			"MYSQL_USER=" + copy.User,
			"MYSQL_PASSWORD=" + copy.Password,
			"MYSQL_DATABASE=" + copy.Database,
			"MYSQL_ROOT_PASSWORD=" + copy.RootPassword,
		},
	}
	if copy.TLSEnabled {
		spec.Cmd = []string{
			"--require_secure_transport=ON",
			"--ssl-cert=/run/ditto/tls/server.crt",
			"--ssl-key=/run/ditto/tls/server.key",
		}
	}
	return spec
}

func (e *Engine) ConnectionString(conn engine.ConnectionConfig) string {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", conn.User, conn.Password, conn.Host, conn.Port, conn.Database)
	if conn.TLSEnabled {
		dsn += "?tls=true"
	}
	return dsn
}

// Dump runs mysqldump inside a short-lived helper container, then compresses
// the resulting SQL dump to destPath. When opts.SchemaOnly is true, --no-data
// is passed so only DDL is captured.
func (e *Engine) Dump(
	ctx context.Context,
	req engine.DumpRequest,
) error {
	if err := engineutil.RequireDocker("mysql", req.Docker); err != nil {
		return err
	}
	src := req.Source
	if err := engine.ValidateSourceHost(src.Host); err != nil {
		return fmt.Errorf("mysql: %w", err)
	}
	password, err := e.secretCache.Resolve(ctx, src.PasswordSecret, src.Password)
	if err != nil {
		return fmt.Errorf("mysql: resolve password: %w", err)
	}
	clientImage := engineutil.ClientImage(req.ClientImage, e.ContainerImage())

	sqlDumpPath := req.DestPath + ".sql"
	dumpFile := path.Join("/dump", filepath.Base(sqlDumpPath))
	baseFlags := []string{
		"--single-transaction", "--routines", "--triggers",
		"--quick", "--no-tablespaces", "--compress",
		"-h", src.Host, "-P", fmt.Sprint(src.Port), "-u", src.User,
	}
	entrypoint, cmd := mysqldumpCmd(baseFlags, req.Options, dumpTarget{database: src.Database, dumpFile: dumpFile})

	if err := dockerutil.RunContainer(ctx, req.Docker, dockerutil.RunRequest{
		Config: &container.Config{
			Image:      clientImage,
			Entrypoint: entrypoint,
			Cmd:        cmd,
			Env:        []string{"MYSQL_PWD=" + password},
		},
		HostConfig:    engineutil.DumpHostConfig(req.DestPath),
		NetworkConfig: engineutil.NetworkConfig(src.NetworkName),
	}); err != nil {
		_ = os.Remove(sqlDumpPath)
		return fmt.Errorf("mysql: dump helper failed: %w", err)
	}
	defer func() {
		_ = os.Remove(sqlDumpPath)
	}()

	if err := gzipFile(sqlDumpPath, req.DestPath); err != nil {
		_ = os.Remove(req.DestPath)
		return err
	}
	return nil
}

// Restore calls mysql inside the container to load the dump file.
// containerName is the Docker container name (e.g. "ditto-<id>") as set by the
// copy manager. The manager calls WaitReady before Restore, so readiness is
// already guaranteed when this method is invoked.
func (e *Engine) Restore(ctx context.Context, req engine.RestoreRequest) error {
	if err := engineutil.RequireDocker("mysql", req.Docker); err != nil {
		return err
	}
	f, err := os.Open(req.DumpPath)
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

	if err := dockerutil.Exec(ctx, req.Docker, dockerutil.ExecRequest{
		ContainerID: req.ContainerName,
		Command: []string{
			"mysql", "-u", req.Copy.User, "-p" + req.Copy.Password, req.Copy.Database,
		},
		Stdin: gz,
	}); err != nil {
		return fmt.Errorf("mysql: restore failed: %w", err)
	}
	return nil
}

// DumpFromContainer creates a gzip-compressed mysqldump of the ditto database
// running inside containerName and writes it to destPath on the host.
// The container must have its dump directory mounted at /dump (read-write).
// When opts.SchemaOnly is true, --no-data is passed to mysqldump.
func (e *Engine) DumpFromContainer(ctx context.Context, req engine.DumpFromContainerRequest) error {
	if err := engineutil.RequireDocker("mysql", req.Docker); err != nil {
		return err
	}
	sqlFile := filepath.Base(req.DestPath) + ".sql"
	dumpFile := "/dump/" + sqlFile
	baseFlags := []string{
		"-u" + req.Copy.User, "-p" + req.Copy.Password,
		"--single-transaction", "--routines", "--triggers",
		"--quick", "--no-tablespaces",
	}
	entrypoint, args := mysqldumpCmd(baseFlags, req.Options, dumpTarget{database: req.Copy.Database, dumpFile: dumpFile})
	if err := dockerutil.Exec(ctx, req.Docker, dockerutil.ExecRequest{
		ContainerID: req.ContainerName,
		Command:     append(entrypoint, args...),
	}); err != nil {
		return fmt.Errorf("mysql: dump from container failed: %w", err)
	}

	hostSQLPath := filepath.Join(filepath.Dir(req.DestPath), sqlFile)
	defer func() { _ = os.Remove(hostSQLPath) }()

	if err := gzipFile(hostSQLPath, req.DestPath); err != nil {
		_ = os.Remove(req.DestPath)
		return err
	}
	return nil
}

// WaitReady polls port until MySQL is accepting connections.
func (e *Engine) WaitReady(conn engine.ConnectionConfig, timeout time.Duration) error {
	return engineutil.WaitReady(engineutil.ReadyRequest{
		Prefix:     "mysql",
		DriverName: "mysql",
		Connection: conn,
		Timeout:    timeout,
		DSN:        e.ConnectionString(conn),
	})
}

var _ engine.Engine = (*Engine)(nil)

// dumpTarget names the database and output file for a mysqldump invocation,
// preventing the two strings from being passed in the wrong order.
type dumpTarget struct {
	database string
	dumpFile string
}

// mysqldumpCmd returns the container entrypoint and args for a mysqldump run.
// When ExcludeTableData is set (and SchemaOnly is not), it returns a two-pass
// sh -c script that preserves schema for excluded tables while skipping their rows.
func mysqldumpCmd(baseFlags []string, opts engine.DumpOptions, target dumpTarget) (entrypoint, args []string) {
	if len(opts.ExcludeTableData) > 0 && !opts.SchemaOnly {
		return []string{"sh", "-c"}, []string{excludeDataScript(baseFlags, opts.ExcludeTableData, target)}
	}
	cmd := make([]string, 0, len(baseFlags)+3)
	cmd = append(cmd, baseFlags...)
	cmd = append(cmd, "--result-file="+target.dumpFile, target.database)
	if opts.SchemaOnly {
		cmd = append(cmd, "--no-data")
	}
	return []string{"mysqldump"}, cmd
}

// excludeDataScript builds a two-pass sh script: pass 1 dumps the full schema
// (--no-data), pass 2 dumps row data while ignoring the excluded tables.
func excludeDataScript(baseFlags, excludeTables []string, target dumpTarget) string {
	schemaArgs := make([]string, 0, len(baseFlags)+3)
	schemaArgs = append(schemaArgs, baseFlags...)
	schemaArgs = append(schemaArgs, "--no-data", "--result-file="+target.dumpFile, target.database)

	dataArgs := make([]string, 0, len(baseFlags)+len(excludeTables)+2)
	dataArgs = append(dataArgs, baseFlags...)
	dataArgs = append(dataArgs, "--no-create-info")
	for _, t := range excludeTables {
		dataArgs = append(dataArgs, "--ignore-table="+target.database+"."+t)
	}
	dataArgs = append(dataArgs, target.database)
	return "mysqldump " + shellJoin(schemaArgs) + " && mysqldump " + shellJoin(dataArgs) + " >> " + target.dumpFile
}

// shellJoin joins args into a shell-safe string by single-quoting each one.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", "'\\''") + "'"
	}
	return strings.Join(quoted, " ")
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
