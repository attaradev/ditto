package cmd

import (
	"github.com/spf13/cobra"
)

func newCopyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "copy",
		Short: "Provision and manage isolated database copies",
	}
	cmd.AddCommand(
		newCopyCreateCmd(),
		newCopyListCmd(),
		newCopyDeleteCmd(),
		newCopyLogsCmd(),
	)
	return cmd
}

func newCopyCreateCmd() *cobra.Command {
	var (
		ttl    string
		label  string
		format string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a clean test database copy",
		Long: `Create a clean, isolated database copy from the latest local dump.

When stdout is a terminal, ditto prints a summary table. When stdout is a pipe
or --format=pipe, it prints only the connection string so scripts can capture it.
Use --format=json when automation needs both the copy ID and the connection string:

    export DATABASE_URL=$(ditto copy create)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCopyCreate(cmd, ttl, label, format)
		},
	}
	cmd.Flags().StringVar(&ttl, "ttl", "", "Override copy lifetime (for example: 1h, 30m)")
	cmd.Flags().StringVar(&label, "label", "", "Optional automation label (for example: gha_run_id=12345)")
	cmd.Flags().StringVar(&format, "format", "auto", "Output format: auto, pipe, json")
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
