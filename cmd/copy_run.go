package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/attaradev/ditto/internal/copy"
	"github.com/attaradev/ditto/internal/store"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

func runCopyCreate(cmd *cobra.Command, ttl, label, format string) error {
	mgr := managerFromContext(cmd)

	opts := copy.CreateOptions{
		GHARunID:   os.Getenv("GITHUB_RUN_ID"),
		GHAJobName: os.Getenv("GITHUB_JOB"),
	}
	if ttl != "" {
		d, err := time.ParseDuration(ttl)
		if err != nil {
			return fmt.Errorf("invalid --ttl %q: %w", ttl, err)
		}
		opts.TTLSeconds = int(d.Seconds())
	}

	c, err := mgr.Create(cmd.Context(), opts)
	if err != nil {
		return err
	}

	if isPipe() || format == "pipe" {
		fmt.Println(c.ConnectionString)
		return nil
	}
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(c)
	}

	// Terminal: pretty-print
	return printCopyTable([]*store.Copy{c})
}

func runCopyList(cmd *cobra.Command) error {
	cs := copyStoreFromContext(cmd)
	copies, err := cs.List(store.ListFilter{})
	if err != nil {
		return err
	}
	if isPipe() {
		return json.NewEncoder(os.Stdout).Encode(copies)
	}
	return printCopyTable(copies)
}

func runCopyDelete(cmd *cobra.Command, id string) error {
	mgr := managerFromContext(cmd)
	return mgr.Destroy(cmd.Context(), id)
}

func runCopyLogs(cmd *cobra.Command, id string) error {
	es := eventStoreFromContext(cmd)
	events, err := es.List(id)
	if err != nil {
		return err
	}
	if isPipe() {
		return json.NewEncoder(os.Stdout).Encode(events)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "TIME\tACTION\tACTOR\tMETADATA"); err != nil {
		return err
	}
	for _, e := range events {
		meta := ""
		if len(e.Metadata) > 0 {
			b, err := json.Marshal(e.Metadata)
			if err != nil {
				return fmt.Errorf("marshal event metadata: %w", err)
			}
			meta = string(b)
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			e.CreatedAt.Format(time.RFC3339), e.Action, e.Actor, meta); err != nil {
			return err
		}
	}
	return w.Flush()
}

var (
	styleHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleID     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleReady  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleFailed = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

func printCopyTable(copies []*store.Copy) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, styleHeader.Render("ID\tSTATUS\tPORT\tAGE\tCONNECTION")); err != nil {
		return err
	}
	for _, c := range copies {
		status := string(c.Status)
		switch c.Status {
		case store.StatusReady, store.StatusInUse:
			status = styleReady.Render(status)
		case store.StatusFailed:
			status = styleFailed.Render(status)
		}
		id := styleID.Render(c.ID)
		age := time.Since(c.CreatedAt).Round(time.Second).String()
		if _, err := fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", id, status, c.Port, age, c.ConnectionString); err != nil {
			return err
		}
	}
	return w.Flush()
}
