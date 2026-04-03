package cmd

import (
	"github.com/spf13/cobra"
)

func newCopyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "copy",
		Short: "Manage ephemeral database copies",
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
		Short: "Create an ephemeral database copy",
		Long: `Create a new isolated copy of the source database.

When stdout is a terminal, prints a summary table. When stdout is a pipe
(or --format=pipe), prints only the connection string so it can be captured:

    export DATABASE_URL=$(ditto copy create)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCopyCreate(cmd, ttl, label, format)
		},
	}
	cmd.Flags().StringVar(&ttl, "ttl", "", "Override copy TTL (e.g. 1h, 30m)")
	cmd.Flags().StringVar(&label, "label", "", "Arbitrary label (e.g. gha_run_id=12345)")
	cmd.Flags().StringVar(&format, "format", "auto", "Output format: auto, pipe, json")
	return cmd
}

func newCopyListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List active copies",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCopyList(cmd)
		},
	}
}

func newCopyDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Destroy a copy immediately",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCopyDelete(cmd, args[0])
		},
	}
}

func newCopyLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs <id>",
		Short: "Show event log for a copy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCopyLogs(cmd, args[0])
		},
	}
}
