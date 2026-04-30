// Package cmd contains all cobra commands for the ditto CLI.
package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/config"
	copypkg "github.com/attaradev/ditto/internal/copy"
	"github.com/attaradev/ditto/internal/dockerutil"
	dumppkg "github.com/attaradev/ditto/internal/dump"
	dittostore "github.com/attaradev/ditto/internal/store"
	isatty "github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

// contextKey is the type for values stored in cobra command contexts.
type contextKey int

const (
	keyDB contextKey = iota
	keyCopyStore
	keyEventStore
	keyManager
	keyScheduler
	keyConfig
	keyServerURL
	keyLocalInitErr
)

// NewRootCmd constructs the root cobra command. dbPath and cfgPath are
// resolved from flags in main.go's PersistentPreRunE.
func NewRootCmd() *cobra.Command {
	cobra.EnableTraverseRunHooks = true

	var (
		dbPath  string
		cfgPath string
	)

	root := &cobra.Command{
		Use:   "ditto",
		Short: "Provision reliable isolated database copies for CI",
		Long: `ditto provisions clean, isolated database copies for CI and other automated tests.

Use it when shared staging databases make test runs flaky, schema fidelity matters, and you want production-like database behavior on a self-hosted runner without adding a separate control plane.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVar(&dbPath, "db", defaultDBPath(), "Path to the ditto SQLite metadata database")
	root.PersistentFlags().StringVar(&cfgPath, "config", "", "Path to ditto.yaml (otherwise search ./ditto.yaml, ~/.ditto/ditto.yaml, /etc/ditto/ditto.yaml)")

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if skipLocalInit(cmd) {
			return nil
		}

		ctx, err := initializeLocalContext(cmd.Context(), dbPath, cfgPath)
		if err != nil {
			return err
		}
		cmd.SetContext(ctx)
		return nil
	}

	root.AddCommand(
		newCopyCmd(),
		newReseedCmd(),
		newStatusCmd(),
		newHostCmd(),
		newErdCmd(),
		newEnvCmd(),
		newTargetCmd(),
		newDoctorCmd(),
		newInitCmd(),
	)
	return root
}

func skipLocalInit(cmd *cobra.Command) bool {
	return cmd.Name() == "help" || cmd.Name() == "__complete"
}

func initializeLocalContext(ctx context.Context, dbPath, cfgPath string) (context.Context, error) {
	db, err := dittostore.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	cs := dittostore.NewCopyStore(db)
	es := dittostore.NewEventStore(db)
	ctx = context.WithValue(ctx, keyDB, db)
	ctx = context.WithValue(ctx, keyCopyStore, cs)
	ctx = context.WithValue(ctx, keyEventStore, es)

	cfg, cfgErr := config.Load(cfgPath)
	if cfgErr != nil {
		ctx = storeLocalInitError(ctx, cfgErr)
	}
	if cfg == nil {
		return ctx, nil
	}

	ctx = context.WithValue(ctx, keyConfig, cfg)
	if cfgErr != nil {
		return ctx, nil
	}
	return initializeLocalServices(ctx, cfg, localStores{
		copies: cs,
		events: es,
	}), nil
}

type localStores struct {
	copies *dittostore.CopyStore
	events *dittostore.EventStore
}

func initializeLocalServices(ctx context.Context, cfg *config.Config, stores localStores) context.Context {
	eng, err := engineFromConfig(cfg)
	if err != nil {
		return storeLocalInitError(ctx, err)
	}

	docker, _, err := dockerutil.NewClient(ctx, cfg.DockerHost)
	if err != nil {
		return storeLocalInitError(ctx, err)
	}

	pool := copypkg.NewPortPool(cfg.PortPoolStart, cfg.PortPoolEnd, occupiedPorts(stores.copies))
	mgr, err := copypkg.NewManager(copypkg.ManagerDeps{
		Config:     cfg,
		Engine:     eng,
		CopyStore:  stores.copies,
		EventStore: stores.events,
		PortPool:   pool,
		Docker:     docker,
	})
	if err != nil {
		ctx = storeLocalInitError(ctx, err)
	} else {
		ctx = context.WithValue(ctx, keyManager, mgr)
	}

	sched := dumppkg.New(cfg, eng, stores.events, docker)
	return context.WithValue(ctx, keyScheduler, sched)
}

func storeLocalInitError(ctx context.Context, err error) context.Context {
	if err == nil {
		return ctx
	}
	if existing, ok := ctx.Value(keyLocalInitErr).(error); ok && existing != nil {
		return ctx
	}
	return context.WithValue(ctx, keyLocalInitErr, err)
}

// --- context accessors ---

func copyStoreFromContext(cmd *cobra.Command) *dittostore.CopyStore {
	return cmd.Context().Value(keyCopyStore).(*dittostore.CopyStore)
}

func eventStoreFromContext(cmd *cobra.Command) *dittostore.EventStore {
	return cmd.Context().Value(keyEventStore).(*dittostore.EventStore)
}

func managerFromContext(cmd *cobra.Command) *copypkg.Manager {
	v := cmd.Context().Value(keyManager)
	if v == nil {
		exitWithLocalInitError(cmd)
	}
	return v.(*copypkg.Manager)
}

func schedulerFromContext(cmd *cobra.Command) *dumppkg.Scheduler {
	v := cmd.Context().Value(keyScheduler)
	if v == nil {
		exitWithLocalInitError(cmd)
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

func localInitError(cmd *cobra.Command) error {
	if err, ok := cmd.Context().Value(keyLocalInitErr).(error); ok && err != nil {
		return err
	}
	return fmt.Errorf("ditto.yaml not found or missing required fields — run with a valid config")
}

func exitWithLocalInitError(cmd *cobra.Command) {
	fmt.Fprintln(os.Stderr, "error:", localInitError(cmd))
	fmt.Fprintln(os.Stderr, "\nQuick fixes:")
	fmt.Fprintln(os.Stderr, "  • Set DITTO_SOURCE_URL=<connection-string> (and DITTO_DUMP_PATH only if you want a non-default dump path)")
	fmt.Fprintln(os.Stderr, "  • Or run: ditto init   (generates ./ditto.yaml from your source DB)")
	fmt.Fprintln(os.Stderr, "  • Or run: ditto doctor (diagnoses config, Docker, and dump issues)")
	os.Exit(1)
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
