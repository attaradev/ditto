package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show replication health and active copies",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd)
		},
	}
}

func runStatus(cmd *cobra.Command) error {
	cs := copyStoreFromContext(cmd)
	cfg := configFromContext(cmd)

	active, err := cs.List(activeFilter())
	if err != nil {
		return err
	}

	// Determine dump file age.
	dumpAge := "unknown"
	dumpStale := false
	if info, err := os.Stat(cfg.Dump.Path); err == nil {
		age := time.Since(info.ModTime())
		dumpAge = age.Round(time.Second).String()
		if int(age.Seconds()) > cfg.Dump.StaleThreshold*2 {
			dumpStale = true
		}
	}

	portTotal := cfg.PortPoolEnd - cfg.PortPoolStart + 1
	portsFree := portTotal - len(active)

	if isPipe() {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"active_copies": len(active),
			"ports_free":    portsFree,
			"dump_age":      dumpAge,
			"dump_stale":    dumpStale,
		})
	}

	// Terminal output.
	staleWarning := ""
	if dumpStale {
		staleWarning = styleFailed.Render("  STALE")
	}
	fmt.Printf("Dump age:     %s%s\n", dumpAge, staleWarning)
	fmt.Printf("Active copies: %d / %d ports used\n", len(active), portTotal)

	if len(active) > 0 {
		fmt.Println()
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, styleHeader.Render("ID\tSTATUS\tPORT\tAGE"))
		for _, c := range active {
			age := time.Since(c.CreatedAt).Round(time.Second).String()
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\n",
				styleID.Render(c.ID), c.Status, c.Port, age)
		}
		w.Flush()
	}
	return nil
}

// dockerVersion returns the running Docker version string, or "unavailable".
func dockerVersion() string {
	out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Output()
	if err != nil {
		return "unavailable"
	}
	return string(out)
}
