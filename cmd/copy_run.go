package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	copypkg "github.com/attaradev/ditto/internal/copy"
	"github.com/attaradev/ditto/internal/dumpfetch"
	"github.com/attaradev/ditto/internal/store"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

// copyClientFromContext returns an HTTPClient when --server is set, or the
// local Manager otherwise. This makes copy commands transparent to whether
// they operate locally or against a shared ditto host.
func copyClientFromContext(cmd *cobra.Command) copypkg.CopyClient {
	if url := serverURLFromContext(cmd); url != "" {
		token := os.Getenv("DITTO_TOKEN")
		return copypkg.NewHTTPClient(url, token)
	}
	return managerFromContext(cmd)
}

func serverURLFromContext(cmd *cobra.Command) string {
	if url, ok := cmd.Context().Value(keyServerURL).(string); ok {
		return url
	}
	return ""
}

// detectRunID returns the first non-empty value from a standard set of
// automation run-identifier env vars, across common CI systems and local
// dev tools. DITTO_RUN_ID always takes precedence.
func detectRunID() string {
	candidates := []string{
		"DITTO_RUN_ID",       // explicit override (any environment)
		"GITHUB_RUN_ID",      // GitHub Actions
		"CI_PIPELINE_ID",     // GitLab CI
		"CIRCLE_WORKFLOW_ID", // CircleCI
		"TRAVIS_BUILD_ID",    // Travis CI
		"BUILDKITE_BUILD_ID", // Buildkite
		"BUILD_ID",           // Jenkins / generic
	}
	for _, k := range candidates {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// detectJobName returns the first non-empty value from a standard set of
// job/step name env vars. DITTO_JOB_NAME always takes precedence.
func detectJobName() string {
	candidates := []string{
		"DITTO_JOB_NAME",     // explicit override (any environment)
		"GITHUB_JOB",         // GitHub Actions
		"CI_JOB_NAME",        // GitLab CI
		"CIRCLE_JOB",         // CircleCI
		"TRAVIS_JOB_NAME",    // Travis CI
		"BUILDKITE_STEP_KEY", // Buildkite
		"JOB_NAME",           // Jenkins / generic
	}
	for _, k := range candidates {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func runCopyCreate(cmd *cobra.Command, ttl, label, format, dumpURI string, obfuscate bool) error {
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
	if dumpURI != "" {
		if serverURLFromContext(cmd) != "" {
			opts.DumpURI = dumpURI
		} else {
			localPath, cleanup, err := dumpfetch.Fetch(cmd.Context(), dumpURI)
			if err != nil {
				return fmt.Errorf("--dump: %w", err)
			}
			defer cleanup()
			opts.DumpPath = localPath
		}
	}
	opts.Obfuscate = obfuscate

	c, err := client.Create(cmd.Context(), opts)
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
	client := copyClientFromContext(cmd)
	copies, err := client.List(cmd.Context())
	if err != nil {
		return err
	}
	if isPipe() {
		return json.NewEncoder(os.Stdout).Encode(copies)
	}
	return printCopyTable(copies)
}

func runCopyDelete(cmd *cobra.Command, id string) error {
	client := copyClientFromContext(cmd)
	return client.Destroy(cmd.Context(), id)
}

func runCopyLogs(cmd *cobra.Command, id string) error {
	if url := serverURLFromContext(cmd); url != "" {
		client := copypkg.NewHTTPClient(url, os.Getenv("DITTO_TOKEN"))
		events, err := client.Events(cmd.Context(), id)
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

// runCopyExec creates a copy, runs command with DATABASE_URL set, then destroys
// the copy regardless of how the command exits. The command's exit code is
// propagated: callers can inspect it via ExitError.
func runCopyExec(cmd *cobra.Command, ttl, label, dumpURI string, obfuscate bool, command []string) error {
	client := copyClientFromContext(cmd)
	ctx := cmd.Context()

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
	if dumpURI != "" {
		if serverURLFromContext(cmd) != "" {
			opts.DumpURI = dumpURI
		} else {
			localPath, cleanup, err := dumpfetch.Fetch(ctx, dumpURI)
			if err != nil {
				return fmt.Errorf("--dump: %w", err)
			}
			defer cleanup()
			opts.DumpPath = localPath
		}
	}
	opts.Obfuscate = obfuscate

	c, err := client.Create(ctx, opts)
	if err != nil {
		return fmt.Errorf("copy run: create: %w", err)
	}

	// Best-effort destroy on any exit path.
	defer func() {
		if err := client.Destroy(ctx, c.ID); err != nil {
			fmt.Fprintf(os.Stderr, "copy run: cleanup warning: %v\n", err)
		}
	}()

	// #nosec G204 -- command is supplied by the operator, not end-user input.
	proc := exec.CommandContext(ctx, command[0], command[1:]...)
	proc.Stdin = os.Stdin
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr
	proc.Env = append(os.Environ(),
		"DATABASE_URL="+c.ConnectionString,
		"DITTO_COPY_ID="+c.ID,
	)

	// Forward SIGTERM and SIGINT to the child process so it can shut down
	// gracefully before the defer cleanup runs.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		if sig, ok := <-sigs; ok && proc.Process != nil {
			_ = proc.Process.Signal(sig)
		}
	}()

	runErr := proc.Run()
	signal.Stop(sigs)
	close(sigs)
	return runErr
}

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
		conn := c.ConnectionString
		if conn == "" {
			conn = "-"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", id, status, c.Port, age, conn); err != nil {
			return err
		}
	}
	return w.Flush()
}

func parseTTL(ttl string) (int, error) {
	d, err := time.ParseDuration(ttl)
	if err != nil {
		return 0, fmt.Errorf("invalid --ttl %q: %w", ttl, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid --ttl %q: duration must be greater than zero", ttl)
	}
	return int(d.Seconds()), nil
}
