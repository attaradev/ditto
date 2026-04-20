package cmd

import (
	"context"
	"fmt"
	"os"

	copypkg "github.com/attaradev/ditto/internal/copy"
	"github.com/spf13/cobra"
)

func newEnvCmd() *cobra.Command {
	var serverURL string

	cmd := &cobra.Command{
		Use:   "env",
		Short: "Inject DATABASE_URL into your shell or a subprocess",
		Long: `Manage DATABASE_URL injection for shell sessions and subprocesses.

Subcommands:
  ditto env -- <command>   Run a command with DATABASE_URL set (same as copy run)
  ditto env export         Create a copy and print eval-able export lines
  ditto env destroy <id>   Destroy a copy created by export

Shell session workflow:
  eval $(ditto env export)         # creates a copy; sets DATABASE_URL + DITTO_COPY_ID
  psql $DATABASE_URL               # use the copy from any tool
  ditto env destroy $DITTO_COPY_ID # clean up when done`,
	}

	cmd.PersistentFlags().StringVar(&serverURL, "server", "",
		"Shared ditto host URL (e.g. http://ditto.internal:8080)")

	// Propagate --server into context so copyClientFromContext picks it up.
	cmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if serverURL != "" {
			ctx := context.WithValue(cmd.Context(), keyServerURL, serverURL)
			cmd.SetContext(ctx)
		}
		return nil
	}

	cmd.AddCommand(
		newEnvRunCmd(),
		newEnvExportCmd(),
		newEnvDestroyCmd(),
	)

	return cmd
}

// newEnvRunCmd: ditto env -- <command> [args…]
// Thin wrapper around runCopyExec — identical lifecycle, signal handling, and
// DATABASE_URL injection as `ditto copy run`.
func newEnvRunCmd() *cobra.Command {
	var (
		ttl       string
		label     string
		dumpURI   string
		obfuscate bool
	)

	cmd := &cobra.Command{
		Use:                "-- <command> [args...]",
		Short:              "Run a command with DATABASE_URL injected",
		DisableFlagParsing: false,
		Args:               cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCopyExec(cmd, ttl, label, dumpURI, obfuscate, args)
		},
	}

	cmd.Flags().StringVar(&ttl, "ttl", "", "Copy lifetime (e.g. 1h, 30m)")
	cmd.Flags().StringVar(&label, "label", "", "Run identifier tag")
	cmd.Flags().StringVar(&dumpURI, "dump", "", "Dump source: local path, s3://..., or https://...")
	cmd.Flags().BoolVar(&obfuscate, "obfuscate", false, "Apply obfuscation rules after restore")

	return cmd
}

// newEnvExportCmd: ditto env export
// Creates a copy and prints eval-able shell export lines. Intended use:
//
//	eval $(ditto env export)
func newEnvExportCmd() *cobra.Command {
	var (
		ttl   string
		label string
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Create a copy and print eval-able export lines",
		Long: `Create an ephemeral database copy and print shell-eval-able export lines.

Usage:
  eval $(ditto env export)

After eval, DATABASE_URL and DITTO_COPY_ID are set in the current shell.
Destroy the copy when you are done:
  ditto env destroy $DITTO_COPY_ID`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvExport(cmd, ttl, label)
		},
	}

	cmd.Flags().StringVar(&ttl, "ttl", "", "Copy lifetime (e.g. 1h, 30m)")
	cmd.Flags().StringVar(&label, "label", "", "Run identifier tag")

	return cmd
}

func runEnvExport(cmd *cobra.Command, ttl, label string) error {
	client := copyClientFromContext(cmd)

	runID := label
	if runID == "" {
		runID = detectRunID()
	}
	opts := copypkg.CreateOptions{
		RunID:   runID,
		JobName: detectJobName(),
	}
	if ttl != "" {
		ttlSeconds, err := parseTTL(ttl)
		if err != nil {
			return err
		}
		opts.TTLSeconds = ttlSeconds
	}

	c, err := client.Create(cmd.Context(), opts)
	if err != nil {
		return fmt.Errorf("env export: create copy: %w", err)
	}

	// Print eval-able lines to stdout.
	if _, err := fmt.Fprintf(os.Stdout, "export DATABASE_URL=%q\nexport DITTO_COPY_ID=%q\n",
		c.ConnectionString, c.ID); err != nil {
		return fmt.Errorf("env export: write output: %w", err)
	}
	return nil
}

// newEnvDestroyCmd: ditto env destroy <id>
func newEnvDestroyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "destroy <id>",
		Short: "Destroy a database copy by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return copyClientFromContext(cmd).Destroy(cmd.Context(), args[0])
		},
	}
}
