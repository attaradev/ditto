package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"github.com/attaradev/ditto/internal/copy"
	"github.com/attaradev/ditto/internal/store"
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
	printCopyTable([]*store.Copy{c})
	return nil
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
	printCopyTable(copies)
	return nil
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
	fmt.Fprintln(w, "TIME\tACTION\tACTOR\tMETADATA")
	for _, e := range events {
		meta := ""
		if len(e.Metadata) > 0 {
			b, _ := json.Marshal(e.Metadata)
			meta = string(b)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			e.CreatedAt.Format(time.RFC3339), e.Action, e.Actor, meta)
	}
	return w.Flush()
}

var (
	styleHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleID     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleReady  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleFailed = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

func printCopyTable(copies []*store.Copy) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, styleHeader.Render("ID\tSTATUS\tPORT\tAGE\tCONNECTION"))
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
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", id, status, c.Port, age, c.ConnectionString)
	}
	w.Flush()
}
