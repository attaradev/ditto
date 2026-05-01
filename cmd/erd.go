package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/attaradev/ditto/engine"
	copypkg "github.com/attaradev/ditto/internal/copy"
	"github.com/attaradev/ditto/internal/config"
	"github.com/attaradev/ditto/internal/erd"
	"github.com/attaradev/ditto/internal/secret"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/spf13/cobra"
)

func newErdCmd() *cobra.Command {
	var (
		format    string
		output    string
		useSource bool
		serverURL string
	)

	cmd := &cobra.Command{
		Use:   "erd",
		Short: "Generate an Entity-Relationship Diagram from the database schema",
		Long: `Generate an ERD by introspecting the live database schema.

By default ditto creates a temporary copy, introspects its schema, then
destroys it — so your source database is never accessed at query time.
Use --source to connect directly to the configured source database instead.

Supported output formats:
  mermaid  Mermaid erDiagram syntax (default) — paste into any Mermaid renderer,
           GitHub markdown fences, Notion, or VS Code with the Mermaid extension.
  dbml     DBML syntax — compatible with dbdiagram.io.

Examples:
  ditto erd                             # Mermaid ERD to stdout via a copy
  ditto erd --format=dbml               # DBML to stdout
  ditto erd --output=schema.md          # Write Mermaid to file
  ditto erd --source                    # Connect to source DB directly (no copy)
  ditto erd --server http://ditto:8080  # Use a shared ditto host for the copy`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runERD(cmd, erdOptions{format: format, output: output}, useSource)
		},
	}

	cmd.Flags().StringVar(&format, "format", "mermaid", "Output format: mermaid, dbml")
	cmd.Flags().StringVar(&output, "output", "", "Output file path (default: stdout)")
	cmd.Flags().BoolVar(&useSource, "source", false, "Connect directly to source DB instead of creating a copy")
	cmd.Flags().StringVar(&serverURL, "server", "", "Shared ditto host URL (e.g. http://ditto.internal:8080)")
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if serverURL != "" {
			ctx := context.WithValue(cmd.Context(), keyServerURL, serverURL)
			cmd.SetContext(ctx)
		}
		return nil
	}

	return cmd
}

type erdOptions struct {
	format string
	output string
}

func runERD(cmd *cobra.Command, opts erdOptions, useSource bool) error {
	cfg := configFromContext(cmd)

	dsn, cleanup, err := resolveERDDSN(cmd, cfg, useSource)
	if err != nil {
		return err
	}
	defer cleanup()

	target, err := resolveERDTarget(cfg, useSource, dsn)
	if err != nil {
		return err
	}

	schema, err := introspectERDSchema(cmd.Context(), target)
	if err != nil {
		return err
	}

	w, closeOutput, err := openERDOutput(opts.output)
	if err != nil {
		return err
	}
	defer closeOutput()

	return renderERD(opts.format, schema, w)
}

// resolveERDDSN returns the DSN to introspect and a cleanup function that must
// be deferred by the caller. When useSource is false a temporary copy is
// created; cleanup destroys it (non-fatal on error — the copy expires via TTL).
func resolveERDDSN(cmd *cobra.Command, cfg *config.Config, useSource bool) (dsn string, cleanup func(), err error) {
	if useSource {
		var sc secret.Cache
		pwd, err := sc.Resolve(cmd.Context(), cfg.Source.PasswordSecret, cfg.Source.Password)
		if err != nil {
			return "", nil, fmt.Errorf("erd: resolve source password: %w", err)
		}
		dsn = buildERDSourceDSN(cfg.Source, pwd)
		return dsn, func() {}, nil
	}

	client := copyClientFromContext(cmd)
	c, err := client.Create(cmd.Context(), copypkg.CreateOptions{RunID: "erd"})
	if err != nil {
		return "", nil, fmt.Errorf("erd: create copy: %w", err)
	}
	return c.ConnectionString, func() {
		if err := client.Destroy(cmd.Context(), c.ID); err != nil {
			_ = err
		}
	}, nil
}

// erdTarget bundles the connection string and metadata needed for introspection.
type erdTarget struct {
	dsn          string
	engineName   string
	databaseName string
}

// resolveERDTarget resolves the engine and database names for introspection.
// When useSource is false, missing values are inferred from the copy DSN.
func resolveERDTarget(cfg *config.Config, useSource bool, dsn string) (erdTarget, error) {
	engineName := cfg.Source.Engine
	databaseName := cfg.Source.Database
	if !useSource {
		if inferredEngine, inferredDatabase, ok := inferERDTargetFromDSN(dsn); ok {
			if engineName == "" {
				engineName = inferredEngine
			}
			if databaseName == "" {
				databaseName = inferredDatabase
			}
		}
	}
	if engineName == "" {
		return erdTarget{}, fmt.Errorf("erd: database engine is unknown; configure source.engine or use a copy DSN with a recognizable format")
	}
	return erdTarget{dsn: dsn, engineName: engineName, databaseName: databaseName}, nil
}

// introspectERDSchema opens a DB connection, fetches the engine, and returns
// the introspected schema. The connection is closed before returning.
func introspectERDSchema(ctx context.Context, t erdTarget) (*erd.Schema, error) {
	db, err := sql.Open(erdDriverName(t.engineName), t.dsn)
	if err != nil {
		return nil, fmt.Errorf("erd: open db: %w", err)
	}
	defer func() { _ = db.Close() }()

	eng, err := engine.Get(t.engineName)
	if err != nil {
		return nil, fmt.Errorf("erd: %w", err)
	}

	schema, err := erd.Introspect(ctx, db, eng.Name(), t.databaseName)
	if err != nil {
		return nil, fmt.Errorf("erd: introspect: %w", err)
	}
	return schema, nil
}

// openERDOutput returns the writer to render into and a close function to defer.
// When path is empty it returns os.Stdout with a no-op closer.
func openERDOutput(path string) (io.Writer, func(), error) {
	if path == "" {
		return os.Stdout, func() {}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, fmt.Errorf("erd: create output file: %w", err)
	}
	return f, func() { _ = f.Close() }, nil
}

// renderERD writes the schema to w in the requested format.
func renderERD(format string, schema *erd.Schema, w io.Writer) error {
	switch format {
	case "mermaid":
		return erd.RenderMermaid(schema, w)
	case "dbml":
		return erd.RenderDBML(schema, w)
	default:
		return fmt.Errorf("erd: unknown format %q — use mermaid or dbml", format)
	}
}

// buildERDSourceDSN builds a DSN for direct connection to the source database.
// Unlike copy container DSNs, this uses sslmode=require for Postgres (the
// source is a real server, not a local container).
func buildERDSourceDSN(src config.Source, password string) string {
	switch src.Engine {
	case "mysql":
		return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", src.User, password, src.Host, src.Port, src.Database)
	default: // postgres
		return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=require", src.User, password, src.Host, src.Port, src.Database)
	}
}

// erdDriverName returns the database/sql driver name for the given engine.
// Both drivers are registered via blank imports in cmd/ditto/main.go.
func erdDriverName(eng string) string {
	if eng == "mysql" {
		return "mysql"
	}
	return "pgx"
}

func inferERDTargetFromDSN(dsn string) (engineName, database string, ok bool) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", "", false
		}
		return "postgres", strings.TrimPrefix(u.Path, "/"), true
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil || cfg.Net == "" {
		return "", "", false
	}
	return "mysql", cfg.DBName, true
}
