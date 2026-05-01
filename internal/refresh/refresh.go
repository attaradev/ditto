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
	target, dumpRef, err := s.prepareRefreshRequest(name, opts)
	if err != nil {
		return nil, err
	}

	result := newRefreshResult(name, target.Engine, dumpRef, opts.DryRun)
	if opts.DryRun {
		s.appendEvent(name, "dry-run", map[string]any{"dump": dumpRef, "obfuscate": opts.Obfuscate})
		return result, nil
	}
	if err := requireDockerRuntime(s.docker); err != nil {
		return nil, err
	}

	runtime, err := s.prepareRuntime(ctx, target, dumpRef)
	if err != nil {
		return s.failRefresh(name, err)
	}
	defer runtime.cleanup()
	result.DumpPath = runtime.localDump

	if err := s.executeRefresh(refreshExecution{
		ctx:     ctx,
		name:    name,
		target:  target,
		opts:    opts,
		runtime: runtime,
		dumpRef: dumpRef,
		result:  result,
	}); err != nil {
		return s.failRefresh(name, err)
	}
	return result, nil
}

type refreshRuntime struct {
	localDump string
	password  string
	dsn       string
	cleanup   func()
}

func (s *Service) prepareRefreshRequest(name string, opts Options) (config.Target, string, error) {
	if s.cfg == nil {
		return config.Target{}, "", fmt.Errorf("refresh: config is required")
	}
	target, ok := s.cfg.Targets[name]
	if !ok {
		return config.Target{}, "", fmt.Errorf("refresh: target %q is not configured", name)
	}
	target = withDefaultPort(target)
	if err := validateRequest(name, target, s.cfg.Source, opts); err != nil {
		return config.Target{}, "", err
	}
	dumpRef := resolveDumpRef(opts.DumpURI, s.cfg.Dump.Path)
	if dumpRef == "" {
		return config.Target{}, "", fmt.Errorf("refresh: no dump configured; set dump.path or pass --dump")
	}
	return target, dumpRef, nil
}

func resolveDumpRef(requested, configured string) string {
	if requested != "" {
		return requested
	}
	return configured
}

func newRefreshResult(name, engine, dumpRef string, dryRun bool) *Result {
	return &Result{
		Target:   name,
		Engine:   engine,
		DumpPath: dumpRef,
		DryRun:   dryRun,
	}
}

func requireDockerRuntime(docker *client.Client) error {
	if docker != nil {
		return nil
	}
	return fmt.Errorf("refresh: docker runtime is required")
}

func (s *Service) prepareRuntime(ctx context.Context, target config.Target, dumpRef string) (refreshRuntime, error) {
	localDump, cleanup, err := resolveDump(ctx, dumpRef)
	if err != nil {
		return refreshRuntime{}, err
	}
	password, err := s.secrets.Resolve(ctx, target.PasswordSecret, target.Password)
	if err != nil {
		cleanup()
		return refreshRuntime{}, fmt.Errorf("refresh: resolve target password: %w", err)
	}
	return refreshRuntime{
		localDump: localDump,
		password:  password,
		dsn:       targetDSN(target, password),
		cleanup:   cleanup,
	}, nil
}

type refreshExecution struct {
	ctx     context.Context
	name    string
	target  config.Target
	opts    Options
	runtime refreshRuntime
	dumpRef string
	result  *Result
}

func (s *Service) executeRefresh(in refreshExecution) error {
	s.appendEvent(in.name, "started", map[string]any{"dump": in.dumpRef, "obfuscate": in.opts.Obfuscate})
	if err := cleanTarget(in.ctx, in.target, in.runtime.dsn); err != nil {
		return err
	}
	in.result.Cleaned = true

	if err := restoreTarget(restoreRequest{
		ctx:      in.ctx,
		docker:   s.docker,
		target:   in.target,
		password: in.runtime.password,
		dumpPath: in.runtime.localDump,
	}); err != nil {
		return err
	}
	in.result.Restored = true

	if in.opts.Obfuscate && len(s.cfg.Obfuscation.Rules) > 0 {
		if err := obfuscation.New(in.target.Engine, in.runtime.dsn, s.cfg.Obfuscation.Rules).Apply(in.ctx); err != nil {
			return fmt.Errorf("refresh: obfuscate target: %w", err)
		}
		in.result.Obfuscated = true
	}

	s.appendEvent(in.name, "completed", map[string]any{
		"dump":       in.dumpRef,
		"cleaned":    in.result.Cleaned,
		"restored":   in.result.Restored,
		"obfuscated": in.result.Obfuscated,
	})
	return nil
}

func (s *Service) failRefresh(name string, err error) (*Result, error) {
	s.appendEvent(name, "failed", map[string]any{"error": err.Error()})
	return nil, err
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
	return sameDatabaseIdentity(sourceIdentity(source), targetIdentity(target))
}

type dbIdentity struct {
	engine   string
	host     string
	port     int
	database string
}

func sourceIdentity(source config.Source) dbIdentity {
	return dbIdentity{
		engine:   source.Engine,
		host:     source.Host,
		port:     sourcePort(source),
		database: source.Database,
	}
}

func targetIdentity(target config.Target) dbIdentity {
	return dbIdentity{
		engine:   target.Engine,
		host:     target.Host,
		port:     targetPort(target),
		database: target.Database,
	}
}

func (d dbIdentity) isComplete() bool {
	return d.engine != "" && d.host != "" && d.database != ""
}

func (d dbIdentity) sameAs(other dbIdentity) bool {
	return strings.EqualFold(d.engine, other.engine) &&
		strings.EqualFold(d.host, other.host) &&
		d.port == other.port &&
		strings.EqualFold(d.database, other.database)
}

func sameDatabaseIdentity(source dbIdentity, target dbIdentity) bool {
	if !source.isComplete() || !target.isComplete() {
		return false
	}
	return source.sameAs(target)
}

func sourcePort(source config.Source) int {
	if source.Port != 0 {
		return source.Port
	}
	return defaultPortForEngine(source.Engine)
}

func targetPort(target config.Target) int {
	if target.Port != 0 {
		return target.Port
	}
	return defaultPortForEngine(target.Engine)
}

func defaultPortForEngine(engine string) int {
	switch engine {
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
	return withMySQLTarget(ctx, dsn, cleanMySQLDatabase)
}

func withMySQLTarget(ctx context.Context, dsn string, fn func(context.Context, *sql.DB) error) error {
	db, err := openMySQLTarget(dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return fn(ctx, db)
}

func cleanMySQLDatabase(ctx context.Context, db *sql.DB) error {
	restoreFKChecks, err := disableMySQLForeignKeyChecks(ctx, db)
	if err != nil {
		return err
	}
	defer restoreFKChecks()

	if err := dropMySQLTablesAndViews(ctx, db); err != nil {
		return err
	}
	return dropMySQLProgrammableObjects(ctx, db)
}

func openMySQLTarget(dsn string) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("refresh: open mysql target: %w", err)
	}
	return db, nil
}

func disableMySQLForeignKeyChecks(ctx context.Context, db *sql.DB) (func(), error) {
	if _, err := db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=0"); err != nil {
		return nil, fmt.Errorf("refresh: disable mysql foreign key checks: %w", err)
	}
	return func() { _, _ = db.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS=1") }, nil
}

type mysqlTable struct {
	name      string
	tableType string
}

func dropMySQLTablesAndViews(ctx context.Context, db *sql.DB) error {
	tables, err := listMySQLTablesAndViews(ctx, db)
	if err != nil {
		return err
	}
	for _, tbl := range tables {
		if err := dropMySQLTableOrView(ctx, db, tbl); err != nil {
			return err
		}
	}
	return nil
}

func listMySQLTablesAndViews(ctx context.Context, db *sql.DB) ([]mysqlTable, error) {

	rows, err := db.QueryContext(ctx, `
SELECT table_name, table_type
FROM information_schema.tables
WHERE table_schema = DATABASE()
ORDER BY CASE WHEN table_type = 'VIEW' THEN 0 ELSE 1 END`)
	if err != nil {
		return nil, fmt.Errorf("refresh: list mysql tables: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var tables []mysqlTable
	for rows.Next() {
		var tbl mysqlTable
		if err := rows.Scan(&tbl.name, &tbl.tableType); err != nil {
			return nil, fmt.Errorf("refresh: scan mysql table: %w", err)
		}
		tables = append(tables, tbl)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("refresh: iterate mysql tables: %w", err)
	}
	return tables, nil
}

func dropMySQLTableOrView(ctx context.Context, db *sql.DB, tbl mysqlTable) error {
	kind := "TABLE"
	if tbl.tableType == "VIEW" {
		kind = "VIEW"
	}
	if _, err := db.ExecContext(ctx, "DROP "+kind+" IF EXISTS "+quoteMySQLIdent(tbl.name)); err != nil {
		return fmt.Errorf("refresh: drop mysql %s %s: %w", strings.ToLower(kind), tbl.name, err)
	}
	return nil
}

func dropMySQLProgrammableObjects(ctx context.Context, db *sql.DB) error {
	if err := dropMySQLRoutines(ctx, db); err != nil {
		return err
	}
	return dropMySQLEvents(ctx, db)
}

func dropMySQLRoutines(ctx context.Context, db *sql.DB) error {
	return dropMySQLObjects(ctx, db, mysqlDropSpec{
		query: `
SELECT routine_name, routine_type
FROM information_schema.routines
WHERE routine_schema = DATABASE()`,
		listErr: "refresh: list mysql routines",
		scanErr: "refresh: scan mysql routine",
		dropErr: "refresh: drop mysql routine %s",
		scan: func(rows *sql.Rows) (mysqlDropObject, error) {
			var name, routineType string
			if err := rows.Scan(&name, &routineType); err != nil {
				return mysqlDropObject{}, err
			}
			return mysqlDropObject{name: name, stmt: "DROP " + routineType + " IF EXISTS " + quoteMySQLIdent(name)}, nil
		},
	})
}

func dropMySQLEvents(ctx context.Context, db *sql.DB) error {
	return dropMySQLObjects(ctx, db, mysqlDropSpec{
		query: `
SELECT event_name
FROM information_schema.events
WHERE event_schema = DATABASE()`,
		listErr: "refresh: list mysql events",
		scanErr: "refresh: scan mysql event",
		dropErr: "refresh: drop mysql event %s",
		scan: func(rows *sql.Rows) (mysqlDropObject, error) {
			var name string
			if err := rows.Scan(&name); err != nil {
				return mysqlDropObject{}, err
			}
			return mysqlDropObject{name: name, stmt: "DROP EVENT IF EXISTS " + quoteMySQLIdent(name)}, nil
		},
	})
}

type mysqlDropObject struct {
	name string
	stmt string
}

type mysqlDropSpec struct {
	query   string
	listErr string
	scanErr string
	dropErr string
	scan    func(rows *sql.Rows) (mysqlDropObject, error)
}

func dropMySQLObjects(ctx context.Context, db *sql.DB, spec mysqlDropSpec) error {
	rows, err := db.QueryContext(ctx, spec.query)
	if err != nil {
		return fmt.Errorf("%s: %w", spec.listErr, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		obj, err := spec.scan(rows)
		if err != nil {
			return fmt.Errorf("%s: %w", spec.scanErr, err)
		}
		if _, err := db.ExecContext(ctx, obj.stmt); err != nil {
			return fmt.Errorf(spec.dropErr+": %w", obj.name, err)
		}
	}
	return rows.Err()
}

func quoteMySQLIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

type restoreRequest struct {
	ctx      context.Context
	docker   *client.Client
	target   config.Target
	password string
	dumpPath string
}

func restoreTarget(in restoreRequest) error {
	eng, err := engine.Get(in.target.Engine)
	if err != nil {
		return fmt.Errorf("refresh: %w", err)
	}
	withImage := restoreWithImageRequest{
		restoreRequest: in,
		image:          eng.ContainerImage(),
	}
	switch in.target.Engine {
	case "mysql":
		return restoreMySQL(withImage)
	default:
		return restorePostgres(withImage)
	}
}

type restoreWithImageRequest struct {
	restoreRequest
	image string
}

func restorePostgres(in restoreWithImageRequest) error {
	if err := dockerutil.RunContainer(in.ctx, in.docker, dockerutil.RunRequest{
		Config: &container.Config{
			Image:      in.image,
			Entrypoint: []string{"pg_restore"},
			Cmd: []string{
				"--no-owner",
				"--no-acl",
				"--host=" + in.target.Host,
				"--port=" + fmt.Sprint(in.target.Port),
				"--username=" + in.target.User,
				"--dbname=" + in.target.Database,
				"/dump/" + filepath.Base(in.dumpPath),
			},
			Env: []string{"PGPASSWORD=" + in.password},
		},
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{{
				Type:     mount.TypeBind,
				Source:   filepath.Dir(in.dumpPath),
				Target:   "/dump",
				ReadOnly: true,
			}},
		},
	}); err != nil {
		return fmt.Errorf("refresh: restore postgres target: %w", err)
	}
	return nil
}

func restoreMySQL(in restoreWithImageRequest) error {
	f, err := os.Open(in.dumpPath)
	if err != nil {
		return fmt.Errorf("refresh: open mysql dump: %w", err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("refresh: open mysql gzip dump: %w", err)
	}
	defer func() { _ = gz.Close() }()

	if err := dockerutil.RunContainer(in.ctx, in.docker, dockerutil.RunRequest{
		Config: &container.Config{
			Image:      in.image,
			Entrypoint: []string{"mysql"},
			Cmd: []string{
				"--host=" + in.target.Host,
				"--port=" + fmt.Sprint(in.target.Port),
				"--user=" + in.target.User,
				"--database=" + in.target.Database,
			},
			Env: []string{"MYSQL_PWD=" + in.password},
		},
		HostConfig: &container.HostConfig{},
		Stdin:      gz,
	}); err != nil {
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
