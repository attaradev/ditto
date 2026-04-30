// Package refresh restores a configured dump into a named target database.
package refresh

import (
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/config"
	"github.com/attaradev/ditto/internal/dockerutil"
	"github.com/attaradev/ditto/internal/dumpfetch"
	"github.com/attaradev/ditto/internal/obfuscation"
	"github.com/attaradev/ditto/internal/secret"
	"github.com/attaradev/ditto/internal/store"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	mysqldriver "github.com/go-sql-driver/mysql"
)

const actor = "ditto-refresh"

// Options controls one target refresh request.
type Options struct {
	DumpURI   string
	Confirm   string
	DryRun    bool
	Obfuscate bool
}

// Result summarizes a target refresh.
type Result struct {
	Target     string `json:"target"`
	Engine     string `json:"engine"`
	DumpPath   string `json:"dump_path,omitempty"`
	DryRun     bool   `json:"dry_run"`
	Cleaned    bool   `json:"cleaned"`
	Restored   bool   `json:"restored"`
	Obfuscated bool   `json:"obfuscated"`
}

// Service refreshes configured target databases.
type Service struct {
	cfg     *config.Config
	events  *store.EventStore
	docker  *client.Client
	secrets secret.Cache
}

func New(cfg *config.Config, events *store.EventStore, docker *client.Client) *Service {
	return &Service{
		cfg:    cfg,
		events: events,
		docker: docker,
	}
}

func (s *Service) Refresh(ctx context.Context, name string, opts Options) (*Result, error) {
	if s.cfg == nil {
		return nil, fmt.Errorf("refresh: config is required")
	}
	target, ok := s.cfg.Targets[name]
	if !ok {
		return nil, fmt.Errorf("refresh: target %q is not configured", name)
	}
	target = withDefaultPort(target)

	if err := validateRequest(name, target, s.cfg.Source, opts); err != nil {
		return nil, err
	}

	dumpRef := opts.DumpURI
	if dumpRef == "" {
		dumpRef = s.cfg.Dump.Path
	}
	if dumpRef == "" {
		return nil, fmt.Errorf("refresh: no dump configured; set dump.path or pass --dump")
	}

	result := &Result{
		Target:   name,
		Engine:   target.Engine,
		DumpPath: dumpRef,
		DryRun:   opts.DryRun,
	}
	if opts.DryRun {
		s.appendEvent(name, "dry-run", map[string]any{"dump": dumpRef, "obfuscate": opts.Obfuscate})
		return result, nil
	}

	if s.docker == nil {
		return nil, fmt.Errorf("refresh: docker runtime is required")
	}

	localDump, cleanup, err := resolveDump(ctx, dumpRef)
	if err != nil {
		s.appendEvent(name, "failed", map[string]any{"error": err.Error()})
		return nil, err
	}
	defer cleanup()
	result.DumpPath = localDump

	password, err := s.secrets.Resolve(ctx, target.PasswordSecret, target.Password)
	if err != nil {
		s.appendEvent(name, "failed", map[string]any{"error": err.Error()})
		return nil, fmt.Errorf("refresh: resolve target password: %w", err)
	}
	dsn := targetDSN(target, password)

	s.appendEvent(name, "started", map[string]any{"dump": dumpRef, "obfuscate": opts.Obfuscate})
	if err := cleanTarget(ctx, target, dsn); err != nil {
		s.appendEvent(name, "failed", map[string]any{"error": err.Error()})
		return nil, err
	}
	result.Cleaned = true

	if err := restoreTarget(ctx, s.docker, target, password, localDump); err != nil {
		s.appendEvent(name, "failed", map[string]any{"error": err.Error()})
		return nil, err
	}
	result.Restored = true

	if opts.Obfuscate && len(s.cfg.Obfuscation.Rules) > 0 {
		if err := obfuscation.New(target.Engine, dsn, s.cfg.Obfuscation.Rules).Apply(ctx); err != nil {
			s.appendEvent(name, "failed", map[string]any{"error": err.Error()})
			return nil, fmt.Errorf("refresh: obfuscate target: %w", err)
		}
		result.Obfuscated = true
	}

	s.appendEvent(name, "completed", map[string]any{
		"dump":       dumpRef,
		"cleaned":    result.Cleaned,
		"restored":   result.Restored,
		"obfuscated": result.Obfuscated,
	})
	return result, nil
}

func validateRequest(name string, target config.Target, source config.Source, opts Options) error {
	if !target.AllowDestructiveRefresh {
		return fmt.Errorf("refresh: target %q does not allow destructive refresh", name)
	}
	if opts.Confirm != name {
		return fmt.Errorf("refresh: confirmation mismatch; pass --confirm %s", name)
	}
	if sameDatabase(source, target) {
		return fmt.Errorf("refresh: target %q matches the configured source database; refusing destructive refresh", name)
	}
	switch target.Engine {
	case "postgres", "mysql":
		return nil
	default:
		return fmt.Errorf("refresh: unsupported target engine %q", target.Engine)
	}
}

func sameDatabase(source config.Source, target config.Target) bool {
	if source.Engine == "" || source.Host == "" || source.Database == "" || target.Engine == "" || target.Host == "" || target.Database == "" {
		return false
	}
	return strings.EqualFold(source.Engine, target.Engine) &&
		strings.EqualFold(source.Host, target.Host) &&
		sourcePort(source) == targetPort(target) &&
		strings.EqualFold(source.Database, target.Database)
}

func sourcePort(source config.Source) int {
	if source.Port != 0 {
		return source.Port
	}
	switch source.Engine {
	case "postgres":
		return 5432
	case "mysql":
		return 3306
	default:
		return 0
	}
}

func targetPort(target config.Target) int {
	if target.Port != 0 {
		return target.Port
	}
	switch target.Engine {
	case "postgres":
		return 5432
	case "mysql":
		return 3306
	default:
		return 0
	}
}

func withDefaultPort(target config.Target) config.Target {
	if target.Port == 0 {
		target.Port = targetPort(target)
	}
	return target
}

func resolveDump(ctx context.Context, ref string) (string, func(), error) {
	localPath, cleanup, err := dumpfetch.Fetch(ctx, ref)
	if err != nil {
		return "", nil, fmt.Errorf("refresh: resolve dump: %w", err)
	}
	localPath = filepath.Clean(localPath)
	info, err := os.Stat(localPath)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("refresh: stat dump %s: %w", localPath, err)
	}
	if info.Size() == 0 {
		cleanup()
		return "", nil, fmt.Errorf("refresh: dump %s is empty", localPath)
	}
	return localPath, cleanup, nil
}

func targetDSN(target config.Target, password string) string {
	switch target.Engine {
	case "mysql":
		cfg := mysqldriver.Config{
			User:      target.User,
			Passwd:    password,
			Net:       "tcp",
			Addr:      net.JoinHostPort(target.Host, fmt.Sprint(target.Port)),
			DBName:    target.Database,
			Timeout:   5 * time.Second,
			ParseTime: true,
		}
		return cfg.FormatDSN()
	default:
		u := url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(target.User, password),
			Host:   net.JoinHostPort(target.Host, fmt.Sprint(target.Port)),
			Path:   "/" + target.Database,
		}
		q := u.Query()
		q.Set("connect_timeout", "5")
		q.Set("sslmode", "prefer")
		u.RawQuery = q.Encode()
		return u.String()
	}
}

func cleanTarget(ctx context.Context, target config.Target, dsn string) error {
	switch target.Engine {
	case "mysql":
		return cleanMySQL(ctx, dsn)
	default:
		return cleanPostgres(ctx, dsn)
	}
}

func cleanPostgres(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("refresh: open postgres target: %w", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.ExecContext(ctx, `
DO $$
DECLARE
  r record;
BEGIN
  FOR r IN
    SELECT nspname
    FROM pg_namespace
    WHERE nspname NOT LIKE 'pg_%'
      AND nspname <> 'information_schema'
  LOOP
    EXECUTE format('DROP SCHEMA IF EXISTS %I CASCADE', r.nspname);
  END LOOP;
  EXECUTE 'CREATE SCHEMA IF NOT EXISTS public';
END $$;`)
	if err != nil {
		return fmt.Errorf("refresh: clean postgres target: %w", err)
	}
	return nil
}

func cleanMySQL(ctx context.Context, dsn string) error {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("refresh: open mysql target: %w", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=0"); err != nil {
		return fmt.Errorf("refresh: disable mysql foreign key checks: %w", err)
	}
	defer func() { _, _ = db.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS=1") }()

	rows, err := db.QueryContext(ctx, `
SELECT table_name, table_type
FROM information_schema.tables
WHERE table_schema = DATABASE()
ORDER BY CASE WHEN table_type = 'VIEW' THEN 0 ELSE 1 END`)
	if err != nil {
		return fmt.Errorf("refresh: list mysql tables: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type table struct {
		name      string
		tableType string
	}
	var tables []table
	for rows.Next() {
		var tbl table
		if err := rows.Scan(&tbl.name, &tbl.tableType); err != nil {
			return fmt.Errorf("refresh: scan mysql table: %w", err)
		}
		tables = append(tables, tbl)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("refresh: iterate mysql tables: %w", err)
	}
	for _, tbl := range tables {
		kind := "TABLE"
		if tbl.tableType == "VIEW" {
			kind = "VIEW"
		}
		if _, err := db.ExecContext(ctx, "DROP "+kind+" IF EXISTS "+quoteMySQLIdent(tbl.name)); err != nil {
			return fmt.Errorf("refresh: drop mysql %s %s: %w", strings.ToLower(kind), tbl.name, err)
		}
	}

	if err := dropMySQLRoutines(ctx, db); err != nil {
		return err
	}
	if err := dropMySQLEvents(ctx, db); err != nil {
		return err
	}
	return nil
}

func dropMySQLRoutines(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `
SELECT routine_name, routine_type
FROM information_schema.routines
WHERE routine_schema = DATABASE()`)
	if err != nil {
		return fmt.Errorf("refresh: list mysql routines: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name, routineType string
		if err := rows.Scan(&name, &routineType); err != nil {
			return fmt.Errorf("refresh: scan mysql routine: %w", err)
		}
		if _, err := db.ExecContext(ctx, "DROP "+routineType+" IF EXISTS "+quoteMySQLIdent(name)); err != nil {
			return fmt.Errorf("refresh: drop mysql routine %s: %w", name, err)
		}
	}
	return rows.Err()
}

func dropMySQLEvents(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `
SELECT event_name
FROM information_schema.events
WHERE event_schema = DATABASE()`)
	if err != nil {
		return fmt.Errorf("refresh: list mysql events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("refresh: scan mysql event: %w", err)
		}
		if _, err := db.ExecContext(ctx, "DROP EVENT IF EXISTS "+quoteMySQLIdent(name)); err != nil {
			return fmt.Errorf("refresh: drop mysql event %s: %w", name, err)
		}
	}
	return rows.Err()
}

func quoteMySQLIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

func restoreTarget(ctx context.Context, docker *client.Client, target config.Target, password, dumpPath string) error {
	eng, err := engine.Get(target.Engine)
	if err != nil {
		return fmt.Errorf("refresh: %w", err)
	}
	switch target.Engine {
	case "mysql":
		return restoreMySQL(ctx, docker, eng.ContainerImage(), target, password, dumpPath)
	default:
		return restorePostgres(ctx, docker, eng.ContainerImage(), target, password, dumpPath)
	}
}

func restorePostgres(ctx context.Context, docker *client.Client, image string, target config.Target, password, dumpPath string) error {
	if err := dockerutil.RunContainer(ctx, docker,
		&container.Config{
			Image:      image,
			Entrypoint: []string{"pg_restore"},
			Cmd: []string{
				"--no-owner",
				"--no-acl",
				"--host=" + target.Host,
				"--port=" + fmt.Sprint(target.Port),
				"--username=" + target.User,
				"--dbname=" + target.Database,
				"/dump/" + filepath.Base(dumpPath),
			},
			Env: []string{"PGPASSWORD=" + password},
		},
		&container.HostConfig{
			Mounts: []mount.Mount{{
				Type:     mount.TypeBind,
				Source:   filepath.Dir(dumpPath),
				Target:   "/dump",
				ReadOnly: true,
			}},
		},
		"",
	); err != nil {
		return fmt.Errorf("refresh: restore postgres target: %w", err)
	}
	return nil
}

func restoreMySQL(ctx context.Context, docker *client.Client, image string, target config.Target, password, dumpPath string) error {
	f, err := os.Open(dumpPath)
	if err != nil {
		return fmt.Errorf("refresh: open mysql dump: %w", err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("refresh: open mysql gzip dump: %w", err)
	}
	defer func() { _ = gz.Close() }()

	if err := dockerutil.RunContainerWithInput(ctx, docker,
		&container.Config{
			Image:      image,
			Entrypoint: []string{"mysql"},
			Cmd: []string{
				"--host=" + target.Host,
				"--port=" + fmt.Sprint(target.Port),
				"--user=" + target.User,
				"--database=" + target.Database,
			},
			Env: []string{"MYSQL_PWD=" + password},
		},
		&container.HostConfig{},
		"",
		gz,
	); err != nil {
		return fmt.Errorf("refresh: restore mysql target: %w", err)
	}
	return nil
}

func (s *Service) appendEvent(target, action string, metadata map[string]any) {
	if s.events == nil {
		return
	}
	if err := s.events.Append("target", target, action, actor, metadata); err != nil {
		// Event logging must not mask the refresh result.
		return
	}
}
