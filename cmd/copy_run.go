package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	copypkg "github.com/attaradev/ditto/internal/copy"
	"github.com/attaradev/ditto/internal/dumpfetch"
	"github.com/attaradev/ditto/internal/store"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

// isLocalFilePath returns true when s looks like a filesystem path rather than
// a URI. A path starts with /, ./, ../, ~/, or contains no URI scheme at all.
func isLocalFilePath(s string) bool {
	for _, p := range []string{"/", "./", "../", "~/"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return !strings.Contains(s, "://")
}

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
		return resolveServerURL(url)
	}
	return resolveServerURL("")
}

type copyRunOptions struct {
	TTL       string
	Label     string
	DumpURI   string
	Obfuscate bool
}

type copyCreateOptions struct {
	copyRunOptions
	Format string
}

type copyExecOptions struct {
	copyRunOptions
	Command []string
}

type ciEnvCandidate struct {
	runID   string
	jobName string
}

var ciEnvCandidates = []ciEnvCandidate{
	{runID: "GITHUB_RUN_ID", jobName: "GITHUB_JOB"},
	{runID: "CI_PIPELINE_ID", jobName: "CI_JOB_NAME"},
	{runID: "CIRCLE_WORKFLOW_ID", jobName: "CIRCLE_JOB"},
	{runID: "TRAVIS_BUILD_ID", jobName: "TRAVIS_JOB_NAME"},
	{runID: "BUILDKITE_BUILD_ID", jobName: "BUILDKITE_STEP_KEY"},
	{runID: "BUILD_ID", jobName: "JOB_NAME"},
}

func detectRunMetadata() (string, string) {
	runID := os.Getenv("DITTO_RUN_ID")
	jobName := os.Getenv("DITTO_JOB_NAME")
	for _, candidate := range ciEnvCandidates {
		if runID == "" {
			runID = os.Getenv(candidate.runID)
		}
		if jobName == "" {
			jobName = os.Getenv(candidate.jobName)
		}
		if runID != "" && jobName != "" {
			break
		}
	}
	return runID, jobName
}

func buildCreateOptions(cmd *cobra.Command, in copyRunOptions) (copypkg.CreateOptions, func(), error) {
	opts := newCreateOptionsBase(in)
	if err := applyCreateTTL(&opts, in.TTL); err != nil {
		return opts, func() {}, err
	}
	cleanup, err := applyCreateDump(cmd, &opts, in.DumpURI)
	if err != nil {
		return opts, func() {}, err
	}
	return opts, cleanup, nil
}

func newCreateOptionsBase(in copyRunOptions) copypkg.CreateOptions {
	detectedRunID, detectedJobName := detectRunMetadata()
	runID := in.Label
	if runID == "" {
		runID = detectedRunID
	}
	return copypkg.CreateOptions{
		RunID:     runID,
		JobName:   detectedJobName,
		Obfuscate: in.Obfuscate,
	}
}

func applyCreateTTL(opts *copypkg.CreateOptions, ttl string) error {
	if ttl == "" {
		return nil
	}
	ttlSeconds, err := parseTTL(ttl)
	if err != nil {
		return err
	}
	opts.TTLSeconds = ttlSeconds
	return nil
}

func applyCreateDump(cmd *cobra.Command, opts *copypkg.CreateOptions, dumpURI string) (func(), error) {
	cleanup := func() {}
	if dumpURI == "" {
		return cleanup, nil
	}
	if serverURLFromContext(cmd) != "" {
		if isLocalFilePath(dumpURI) {
			return cleanup, fmt.Errorf("--dump with a local path is not supported in remote mode; use a URI (s3://, https://) or omit the flag to use the host's configured dump")
		}
		opts.DumpURI = dumpURI
		return cleanup, nil
	}

	localPath, cl, err := dumpfetch.Fetch(cmd.Context(), dumpURI)
	if err != nil {
		return cleanup, fmt.Errorf("--dump: %w", err)
	}
	opts.DumpPath = localPath
	return cl, nil
}

func runCopyCreate(cmd *cobra.Command, in copyCreateOptions) error {
	client := copyClientFromContext(cmd)

	opts, cleanup, err := buildCreateOptions(cmd, in.copyRunOptions)
	if err != nil {
		return err
	}
	defer cleanup()

	c, err := client.Create(cmd.Context(), opts)
	if err != nil {
		return err
	}

	if isPipe() || in.Format == "pipe" {
		fmt.Println(c.ConnectionString)
		return nil
	}
	if in.Format == "json" {
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
		entries := make([]eventEntry, len(events))
		for i, e := range events {
			entries[i] = eventEntry{e.CreatedAt, e.Action, e.Actor, e.Metadata}
		}
		return writeEventsTable(entries)
	}

	es := eventStoreFromContext(cmd)
	events, err := es.List(id)
	if err != nil {
		return err
	}
	if isPipe() {
		return json.NewEncoder(os.Stdout).Encode(events)
	}
	entries := make([]eventEntry, len(events))
	for i, e := range events {
		entries[i] = eventEntry{e.CreatedAt, e.Action, e.Actor, e.Metadata}
	}
	return writeEventsTable(entries)
}

type eventEntry struct {
	CreatedAt time.Time
	Action    string
	Actor     string
	Metadata  map[string]any
}

func writeEventsTable(events []eventEntry) error {
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
func runCopyExec(cmd *cobra.Command, in copyExecOptions) error {
	client := copyClientFromContext(cmd)
	ctx := cmd.Context()

	opts, cleanup, err := buildCreateOptions(cmd, in.copyRunOptions)
	if err != nil {
		return err
	}
	defer cleanup()

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
	proc := exec.CommandContext(ctx, in.Command[0], in.Command[1:]...)
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
