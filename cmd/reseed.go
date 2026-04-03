package cmd

import (
	"github.com/spf13/cobra"
)

func newReseedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reseed",
		Short: "Refresh the local source database dump",
		Long: `Run an immediate dump of the configured source database and replace the
local dump only after the new dump completes successfully. Progress is written
to stderr.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReseed(cmd)
		},
	}
}

func runReseed(cmd *cobra.Command) error {
	sched := schedulerFromContext(cmd)
	return sched.RunOnce(cmd.Context())
}
