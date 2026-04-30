package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/attaradev/ditto/internal/config"
	"github.com/attaradev/ditto/internal/dockerutil"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	var serverURL string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose configuration, Docker, and connectivity issues",
		Long: `Check that ditto's prerequisites are satisfied before running other commands.

Verifies:
  • Docker daemon is reachable
  • Configuration loaded and source fields present
  • Dump file exists and is not stale
  • Source database is reachable (if host is resolvable)
  • OIDC JWKS endpoint is reachable (if server.auth is configured)

Run this first when something isn't working.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd, resolveServerURL(serverURL))
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "Shared ditto host URL to diagnose instead of local Docker/config")
	return cmd
}

type check struct {
	name string
	ok   bool
	msg  string
}

func runDoctor(cmd *cobra.Command, serverURL string) error {
	if serverURL != "" {
		return runRemoteDoctor(cmd, serverURL)
	}

	cfg := configFromContext(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()

	var checks []check

	// 1. Docker reachability.
	_, _, dockerErr := dockerutil.NewClient(ctx, cfg.DockerHost)
	checks = append(checks, check{
		name: "Docker daemon",
		ok:   dockerErr == nil,
		msg:  errMsg(dockerErr, "docker is not reachable — is Docker running?"),
	})

	// 2. Config: source fields.
	cfgCheck, cfgMsg := checkConfig(cfg)
	checks = append(checks, check{name: "Config (source)", ok: cfgCheck, msg: cfgMsg})

	// 3. Dump file.
	dumpCheck, dumpMsg := checkDump(cfg)
	checks = append(checks, check{name: "Dump file", ok: dumpCheck, msg: dumpMsg})

	// 4. Source DB connectivity (best-effort TCP dial via sql.Open ping).
	dbCheck, dbMsg := checkSourceDB(ctx, cfg)
	checks = append(checks, check{name: "Source DB connectivity", ok: dbCheck, msg: dbMsg})

	// 5. OIDC JWKS endpoint (only if configured).
	if cfg.Server.Enabled && cfg.Server.Auth.JWKSURL != "" {
		oidcCheck, oidcMsg := checkOIDC(ctx, cfg.Server.Auth.JWKSURL)
		checks = append(checks, check{name: "OIDC JWKS endpoint", ok: oidcCheck, msg: oidcMsg})
	}

	styleOK := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	styleFail := lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	styleLabel := lipgloss.NewStyle().Width(26)

	allOK := true
	for _, c := range checks {
		status := styleOK.Render("  OK")
		if !c.ok {
			status = styleFail.Render("FAIL")
			allOK = false
		}
		fmt.Printf("%s  %s  %s\n", status, styleLabel.Render(c.name), c.msg)
	}

	fmt.Println()
	if allOK {
		fmt.Println(styleOK.Render("All checks passed."))
		return nil
	}
	return fmt.Errorf("one or more checks failed — fix the issues above and re-run ditto doctor")
}

func runRemoteDoctor(cmd *cobra.Command, serverURL string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()

	ok, msg := checkRemoteServer(ctx, serverURL, os.Getenv("DITTO_TOKEN"))
	checks := []check{{name: "Shared ditto host", ok: ok, msg: msg}}

	styleOK := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	styleFail := lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	styleLabel := lipgloss.NewStyle().Width(26)

	for _, c := range checks {
		status := styleOK.Render("  OK")
		if !c.ok {
			status = styleFail.Render("FAIL")
		}
		fmt.Printf("%s  %s  %s\n", status, styleLabel.Render(c.name), c.msg)
	}
	fmt.Println()
	if ok {
		fmt.Println(styleOK.Render("All checks passed."))
		return nil
	}
	return fmt.Errorf("one or more checks failed — fix the issues above and re-run ditto doctor --server")
}

func checkRemoteServer(ctx context.Context, serverURL, token string) (bool, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(serverURL, "/")+"/v2/copies", nil)
	if err != nil {
		return false, fmt.Sprintf("invalid server URL: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Sprintf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, serverURL
	case http.StatusUnauthorized:
		return false, "authentication failed; set DITTO_TOKEN"
	case http.StatusForbidden:
		return false, "authenticated but not authorized"
	default:
		return false, fmt.Sprintf("server returned HTTP %d", resp.StatusCode)
	}
}

func checkConfig(cfg *config.Config) (bool, string) {
	if cfg.Source.Engine == "" {
		return false, "source.engine is missing — set DITTO_SOURCE_ENGINE or source.engine in ditto.yaml"
	}
	if cfg.Source.Host == "" {
		return false, "source.host is missing — set DITTO_SOURCE_URL or source.host in ditto.yaml"
	}
	if cfg.Source.Database == "" {
		return false, "source.database is missing"
	}
	if cfg.Source.User == "" {
		return false, "source.user is missing"
	}
	if cfg.Source.Password == "" && cfg.Source.PasswordSecret == "" {
		return false, "source.password or source.password_secret is missing"
	}
	return true, fmt.Sprintf("engine=%s host=%s db=%s", cfg.Source.Engine, cfg.Source.Host, cfg.Source.Database)
}

func checkDump(cfg *config.Config) (bool, string) {
	if cfg.Dump.Path == "" {
		return false, "dump.path is not set — set DITTO_DUMP_PATH or dump.path in ditto.yaml"
	}
	info, err := os.Stat(cfg.Dump.Path)
	if err != nil {
		return false, fmt.Sprintf("dump file not found at %s — run: ditto reseed", cfg.Dump.Path)
	}
	age := time.Since(info.ModTime()).Round(time.Second)
	staleThreshold := time.Duration(cfg.Dump.StaleThreshold) * time.Second
	if age > staleThreshold*2 {
		return false, fmt.Sprintf("dump is %s old (stale threshold: %s) — run: ditto reseed", age, staleThreshold)
	}
	return true, fmt.Sprintf("%s (age: %s)", cfg.Dump.Path, age)
}

func checkSourceDB(ctx context.Context, cfg *config.Config) (bool, string) {
	if cfg.Source.Host == "" || cfg.Source.Engine == "" {
		return false, "skipped (config incomplete)"
	}

	var driverName, dsn string
	switch cfg.Source.Engine {
	case "postgres":
		driverName = "pgx"
		pass := cfg.Source.Password
		dsn = fmt.Sprintf("postgres://%s:%s@%s:%d/%s?connect_timeout=5&sslmode=prefer",
			cfg.Source.User, pass, cfg.Source.Host, cfg.Source.Port, cfg.Source.Database)
	case "mysql":
		driverName = "mysql"
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?timeout=5s",
			cfg.Source.User, cfg.Source.Password, cfg.Source.Host, cfg.Source.Port, cfg.Source.Database)
	default:
		return false, fmt.Sprintf("unsupported engine %q", cfg.Source.Engine)
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return false, fmt.Sprintf("cannot open connection: %v", err)
	}
	defer func() { _ = db.Close() }()

	db.SetConnMaxLifetime(5 * time.Second)
	if err := db.PingContext(ctx); err != nil {
		return false, fmt.Sprintf("ping failed: %v", err)
	}
	return true, fmt.Sprintf("%s:%d/%s", cfg.Source.Host, cfg.Source.Port, cfg.Source.Database)
}

func checkOIDC(ctx context.Context, jwksURL string) (bool, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return false, fmt.Sprintf("invalid JWKS URL: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Sprintf("JWKS fetch failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("JWKS endpoint returned HTTP %d", resp.StatusCode)
	}
	return true, jwksURL
}

func errMsg(err error, fallback string) string {
	if err == nil {
		return "reachable"
	}
	if fallback != "" {
		return fallback
	}
	return err.Error()
}
