// Package cmd contains all cobra commands for the ditto CLI.
package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	isatty "github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/config"
	copypkg "github.com/attaradev/ditto/internal/copy"
	dumppkg "github.com/attaradev/ditto/internal/dump"
	dittostore "github.com/attaradev/ditto/internal/store"
)

// contextKey is the type for values stored in cobra command contexts.
type contextKey int

const (
	keyDB          contextKey = iota
	keyCopyStore
	keyEventStore
	keyManager
	keyScheduler
	keyConfig
)

// NewRootCmd constructs the root cobra command. dbPath and cfgPath are
// resolved from flags in main.go's PersistentPreRunE.
func NewRootCmd() *cobra.Command {
	var (
		dbPath  string
		cfgPath string
	)

	root := &cobra.Command{
		Use:   "ditto",
		Short: "Ephemeral database copies for CI",
		Long: `ditto provisions isolated copies of a real database for each test run.

Each copy is a fresh Docker container, restored from a local dump of the source
database. Copies are created on demand and destroyed automatically.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVar(&dbPath, "db", defaultDBPath(), "Path to SQLite metadata database")
	root.PersistentFlags().StringVar(&cfgPath, "config", "", "Path to ditto.yaml (default: ./ditto.yaml)")

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// Skip for help/completion.
		if cmd.Name() == "help" || cmd.Name() == "__complete" {
			return nil
		}

		// Open SQLite.
		db, err := dittostore.Open(dbPath)
		if err != nil {
			return fmt.Errorf("open store: %w", err)
		}

		cs := dittostore.NewCopyStore(db)
		es := dittostore.NewEventStore(db)

		ctx := context.WithValue(cmd.Context(), keyDB, db)
		ctx = context.WithValue(ctx, keyCopyStore, cs)
		ctx = context.WithValue(ctx, keyEventStore, es)

		// Load config (best-effort — some commands don't need it).
		cfg, _ := config.Load(cfgPath)
		if cfg != nil {
			ctx = context.WithValue(ctx, keyConfig, cfg)

			// Initialise copy manager and dump scheduler when config is available.
			eng, engErr := engineFromConfig(cfg)
			if engErr == nil {
				// Load occupied ports from SQLite.
				occupied := occupiedPorts(cs)
				pool := copypkg.NewPortPool(cfg.PortPoolStart, cfg.PortPoolEnd, occupied)

				mgr, mgrErr := copypkg.NewManager(cfg, eng, cs, es, pool)
				if mgrErr == nil {
					ctx = context.WithValue(ctx, keyManager, mgr)
				}

				sched := dumppkg.New(cfg, eng, es)
				ctx = context.WithValue(ctx, keyScheduler, sched)
			}
		}

		cmd.SetContext(ctx)
		return nil
	}

	root.AddCommand(
		newCopyCmd(),
		newReseedCmd(),
		newStatusCmd(),
		newDaemonCmd(),
	)
	return root
}

// --- context accessors ---

func dbFromContext(cmd *cobra.Command) *sql.DB {
	return cmd.Context().Value(keyDB).(*sql.DB)
}

func copyStoreFromContext(cmd *cobra.Command) *dittostore.CopyStore {
	return cmd.Context().Value(keyCopyStore).(*dittostore.CopyStore)
}

func eventStoreFromContext(cmd *cobra.Command) *dittostore.EventStore {
	return cmd.Context().Value(keyEventStore).(*dittostore.EventStore)
}

func managerFromContext(cmd *cobra.Command) *copypkg.Manager {
	v := cmd.Context().Value(keyManager)
	if v == nil {
		fmt.Fprintln(os.Stderr, "error: ditto.yaml not found or missing required fields — run with a valid config")
		os.Exit(1)
	}
	return v.(*copypkg.Manager)
}

func schedulerFromContext(cmd *cobra.Command) *dumppkg.Scheduler {
	v := cmd.Context().Value(keyScheduler)
	if v == nil {
		fmt.Fprintln(os.Stderr, "error: ditto.yaml not found or missing required fields — run with a valid config")
		os.Exit(1)
	}
	return v.(*dumppkg.Scheduler)
}

func configFromContext(cmd *cobra.Command) *config.Config {
	v := cmd.Context().Value(keyConfig)
	if v == nil {
		return &config.Config{
			PortPoolStart: 5433,
			PortPoolEnd:   5600,
		}
	}
	return v.(*config.Config)
}

// --- helpers ---

func activeFilter() dittostore.ListFilter {
	return dittostore.ListFilter{
		Statuses: []dittostore.CopyStatus{
			dittostore.StatusPending,
			dittostore.StatusCreating,
			dittostore.StatusReady,
			dittostore.StatusInUse,
			dittostore.StatusDestroying,
		},
	}
}

func occupiedPorts(cs *dittostore.CopyStore) []int {
	copies, err := cs.List(activeFilter())
	if err != nil {
		return nil
	}
	ports := make([]int, 0, len(copies))
	for _, c := range copies {
		if c.Port > 0 {
			ports = append(ports, c.Port)
		}
	}
	return ports
}

func engineFromConfig(cfg *config.Config) (engine.Engine, error) {
	return engine.Get(cfg.Source.Engine)
}

func isPipe() bool {
	return !isatty.IsTerminal(os.Stdout.Fd())
}

func defaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".ditto/ditto.db"
	}
	return home + "/.ditto/ditto.db"
}
