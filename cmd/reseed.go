package cmd

import (
	"github.com/spf13/cobra"
)

func newReseedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reseed",
		Short: "Rebuild dump from RDS source",
		Long: `Run an immediate dump of the source database, atomically replacing
the local dump file on success. Progress is written to stderr.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReseed(cmd)
		},
	}
}

func runReseed(cmd *cobra.Command) error {
	sched := schedulerFromContext(cmd)
	return sched.RunOnce(cmd.Context())
}
