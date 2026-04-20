package cmd

import (
	"context"

	"github.com/spf13/cobra"
)

func newCopyCmd() *cobra.Command {
	var serverURL string

	cmd := &cobra.Command{
		Use:   "copy",
		Short: "Provision and manage isolated database copies",
	}

	cmd.PersistentFlags().StringVar(&serverURL, "server", "",
		"Shared ditto host URL for copy operations (e.g. http://ditto.internal:8080). "+
			"Bearer token from DITTO_TOKEN.")

	// Store the server URL in context so sub-commands can access it via
	// copyClientFromContext without reading the flag directly.
	cmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if serverURL != "" {
			ctx := context.WithValue(cmd.Context(), keyServerURL, serverURL)
			cmd.SetContext(ctx)
		}
		return nil
	}

	cmd.AddCommand(
		newCopyCreateCmd(),
		newCopyRunCmd(),
		newCopyListCmd(),
		newCopyDeleteCmd(),
		newCopyLogsCmd(),
	)
	return cmd
}

func newCopyCreateCmd() *cobra.Command {
	var (
		ttl       string
		label     string
		format    string
		dump      string
		obfuscate bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a clean test database copy",
		Long: `Create a clean, isolated database copy from the latest local dump.

When stdout is a terminal, ditto prints a summary table. When stdout is a pipe
or --format=pipe, it prints only the connection string so scripts can capture it.
Use --format=json when automation needs both the copy ID and the connection string:

    export DATABASE_URL=$(ditto copy create)

Use --dump to restore from a specific file instead of the default dump path:

    ditto copy create --dump /path/to/backup.gz
    ditto copy create --dump s3://my-bucket/backups/latest.gz
    ditto copy create --dump https://example.com/dump.gz`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCopyCreate(cmd, ttl, label, format, dump, obfuscate)
		},
	}
	cmd.Flags().StringVar(&ttl, "ttl", "", "Override copy lifetime (for example: 1h, 30m)")
	cmd.Flags().StringVar(&label, "label", "", "Run identifier to tag this copy (overrides auto-detected DITTO_RUN_ID / CI env vars)")
	cmd.Flags().StringVar(&format, "format", "auto", "Output format: auto, pipe, json")
	cmd.Flags().StringVar(&dump, "dump", "", "Dump source: local path, s3://bucket/key, or https:// URL")
	cmd.Flags().BoolVar(&obfuscate, "obfuscate", false, "Apply obfuscation rules post-restore (use with --dump when source is not pre-obfuscated)")
	return cmd
}

func newCopyRunCmd() *cobra.Command {
	var (
		ttl       string
		label     string
		dump      string
		obfuscate bool
	)
	cmd := &cobra.Command{
		Use:   "run [flags] -- <command> [args...]",
		Short: "Run a command with a one-time database copy",
		Long: `Create an isolated database copy, run a command with DATABASE_URL set,
then destroy the copy when the command exits — regardless of exit code.

The copy lifecycle is fully automatic:

  ditto copy run -- go test ./...
  ditto copy run --ttl 30m -- sh -c 'migrate -database "$DATABASE_URL" up'
  ditto copy run --server=http://ditto.internal:8080 -- pytest tests/
  ditto copy run --dump s3://my-bucket/latest.gz -- go test ./...

Two environment variables are available to the command:
  DATABASE_URL    — connection string for the copy
  DITTO_COPY_ID   — copy ID (for debugging)

The command's exit code is preserved.`,
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCopyExec(cmd, ttl, label, dump, obfuscate, args)
		},
	}
	cmd.Flags().StringVar(&ttl, "ttl", "", "Copy lifetime (e.g. 1h, 30m); defaults to copy_ttl_seconds in config")
	cmd.Flags().StringVar(&label, "label", "", "Run identifier tag (overrides auto-detected DITTO_RUN_ID)")
	cmd.Flags().StringVar(&dump, "dump", "", "Dump source: local path, s3://bucket/key, or https:// URL")
	cmd.Flags().BoolVar(&obfuscate, "obfuscate", false, "Apply obfuscation rules post-restore (use with --dump when source is not pre-obfuscated)")
	return cmd
}

func newCopyListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List database copies",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCopyList(cmd)
		},
	}
}

func newCopyDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete an isolated database copy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCopyDelete(cmd, args[0])
		},
	}
}

func newCopyLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs <id>",
		Short: "Show lifecycle events for a database copy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCopyLogs(cmd, args[0])
		},
	}
}
