package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/attaradev/ditto/internal/apiv2"
	"github.com/attaradev/ditto/internal/dockerutil"
	"github.com/attaradev/ditto/internal/refresh"
	"github.com/docker/docker/client"
	"github.com/spf13/cobra"
)

func newTargetCmd() *cobra.Command {
	var serverURL string

	cmd := &cobra.Command{
		Use:   "target",
		Short: "Refresh configured target databases",
	}
	cmd.PersistentFlags().StringVar(&serverURL, "server", "",
		"Shared ditto host URL for target operations (e.g. http://ditto.internal:8080). "+
			"Bearer token from DITTO_TOKEN.")
	cmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if serverURL != "" {
			ctx := context.WithValue(cmd.Context(), keyServerURL, serverURL)
			cmd.SetContext(ctx)
		}
		return nil
	}

	cmd.AddCommand(newTargetRefreshCmd())
	return cmd
}

func newTargetRefreshCmd() *cobra.Command {
	var (
		dumpURI   string
		confirm   string
		dryRun    bool
		obfuscate bool
	)
	cmd := &cobra.Command{
		Use:   "refresh <name>",
		Short: "Refresh a configured target database from a dump",
		Long: `Refresh a configured target database from a ditto dump.

This is destructive. The target must set allow_destructive_refresh: true and
the command must pass --confirm <name>.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTargetRefresh(cmd, args[0], refresh.Options{
				DumpURI:   dumpURI,
				Confirm:   confirm,
				DryRun:    dryRun,
				Obfuscate: obfuscate,
			})
		},
	}
	cmd.Flags().StringVar(&dumpURI, "dump", "", "Dump source: local path, s3://bucket/key, or https:// URL")
	cmd.Flags().StringVar(&confirm, "confirm", "", "Required target-name confirmation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Validate the refresh request without cleaning or restoring")
	cmd.Flags().BoolVar(&obfuscate, "obfuscate", false, "Apply configured obfuscation rules after restore")
	return cmd
}

func runTargetRefresh(cmd *cobra.Command, name string, opts refresh.Options) error {
	if url := serverURLFromContext(cmd); url != "" {
		if opts.DumpURI != "" && isLocalFilePath(opts.DumpURI) {
			return fmt.Errorf("--dump with a local path is not supported in remote mode; use a URI (s3://, https://) or omit the flag to use the host's configured dump")
		}
		result, err := remoteTargetRefresh(cmd.Context(), url, name, opts)
		if err != nil {
			return err
		}
		return printTargetRefreshResult(result)
	}

	cfg := configFromContext(cmd)
	var docker *client.Client
	if !opts.DryRun {
		var err error
		docker, _, err = dockerutil.NewClient(cmd.Context(), cfg.DockerHost)
		if err != nil {
			return err
		}
		defer func() { _ = docker.Close() }()
	}

	result, err := refresh.New(cfg, eventStoreFromContext(cmd), docker).Refresh(cmd.Context(), name, opts)
	if err != nil {
		return err
	}
	return printTargetRefreshResult(apiv2.RefreshTargetResponse{
		Target:     result.Target,
		Engine:     result.Engine,
		DumpPath:   result.DumpPath,
		DryRun:     result.DryRun,
		Cleaned:    result.Cleaned,
		Restored:   result.Restored,
		Obfuscated: result.Obfuscated,
	})
}

func remoteTargetRefresh(ctx context.Context, baseURL, name string, opts refresh.Options) (apiv2.RefreshTargetResponse, error) {
	body := apiv2.RefreshTargetRequest{
		DumpURI:   opts.DumpURI,
		Confirm:   opts.Confirm,
		DryRun:    opts.DryRun,
		Obfuscate: opts.Obfuscate,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return apiv2.RefreshTargetResponse{}, fmt.Errorf("target refresh: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/v2/targets/"+pathEscape(name)+"/refresh",
		bytes.NewReader(data))
	if err != nil {
		return apiv2.RefreshTargetResponse{}, fmt.Errorf("target refresh: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token := os.Getenv("DITTO_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return apiv2.RefreshTargetResponse{}, fmt.Errorf("target refresh: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return apiv2.RefreshTargetResponse{}, decodeTargetHTTPError(resp)
	}

	var result apiv2.RefreshTargetResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return apiv2.RefreshTargetResponse{}, fmt.Errorf("target refresh: decode response: %w", err)
	}
	return result, nil
}

func printTargetRefreshResult(result apiv2.RefreshTargetResponse) error {
	if isPipe() {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if result.DryRun {
		fmt.Printf("Target refresh dry run passed: %s (%s)\n", result.Target, result.Engine)
		return nil
	}
	fmt.Printf("Target refreshed: %s (%s)\n", result.Target, result.Engine)
	return nil
}

func decodeTargetHTTPError(resp *http.Response) error {
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Error != "" {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, body.Error)
	}
	return fmt.Errorf("server returned %d", resp.StatusCode)
}

func pathEscape(s string) string {
	return url.PathEscape(s)
}
