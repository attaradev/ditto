package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/attaradev/ditto/engine"
	copypkg "github.com/attaradev/ditto/internal/copy"
	"github.com/attaradev/ditto/internal/erd"
	"github.com/attaradev/ditto/internal/secret"
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
			return runERD(cmd, format, output, useSource)
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

func runERD(cmd *cobra.Command, format, output string, useSource bool) error {
	cfg := configFromContext(cmd)

	var (
		dsn     string
		copyID  string
		cleanup func()
	)

	if useSource {
		var sc secret.Cache
		pwd, err := sc.Resolve(cmd.Context(), cfg.Source.PasswordSecret, cfg.Source.Password)
		if err != nil {
			return fmt.Errorf("erd: resolve source password: %w", err)
		}
		dsn = buildERDSourceDSN(cfg.Source.Engine, cfg.Source.Host, cfg.Source.Port, cfg.Source.Database, cfg.Source.User, pwd)
		cleanup = func() {}
	} else {
		client := copyClientFromContext(cmd)
		c, err := client.Create(cmd.Context(), copypkg.CreateOptions{RunID: "erd"})
		if err != nil {
			return fmt.Errorf("erd: create copy: %w", err)
		}
		dsn = c.ConnectionString
		copyID = c.ID
		cleanup = func() {
			if err := client.Destroy(cmd.Context(), copyID); err != nil {
				// Non-fatal: copy will expire via TTL.
				_ = err
			}
		}
	}
	defer cleanup()

	driver := erdDriverName(cfg.Source.Engine)
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return fmt.Errorf("erd: open db: %w", err)
	}
	defer func() { _ = db.Close() }()

	eng, err := engine.Get(cfg.Source.Engine)
	if err != nil {
		return fmt.Errorf("erd: %w", err)
	}

	schema, err := erd.Introspect(cmd.Context(), db, eng.Name(), cfg.Source.Database)
	if err != nil {
		return fmt.Errorf("erd: introspect: %w", err)
	}

	w := os.Stdout
	if output != "" {
		f, err := os.Create(output)
		if err != nil {
			return fmt.Errorf("erd: create output file: %w", err)
		}
		defer func() { _ = f.Close() }()
		w = f
	}

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
func buildERDSourceDSN(eng, host string, port int, database, user, password string) string {
	switch eng {
	case "mysql":
		return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", user, password, host, port, database)
	default: // postgres
		return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=require", user, password, host, port, database)
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
