// Package config loads and validates ditto.yaml configuration.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

// defaultDumpFilePath returns ~/.ditto/latest.gz for local use, falling back to
// /data/dump/latest.gz when the home directory cannot be determined (server context).
func defaultDumpFilePath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home + "/.ditto/latest.gz"
	}
	return "/data/dump/latest.gz"
}

// Config is the top-level configuration structure, mirroring ditto.yaml.
type Config struct {
	Source  Source            `mapstructure:"source"`
	Dump    Dump              `mapstructure:"dump"`
	Targets map[string]Target `mapstructure:"targets"`

	CopyTTLSeconds int          `mapstructure:"copy_ttl_seconds"`
	PortPoolStart  int          `mapstructure:"port_pool_start"`
	PortPoolEnd    int          `mapstructure:"port_pool_end"`
	WarmPoolSize   int          `mapstructure:"warm_pool_size"` // 0 = disabled (default)
	CopyImage      string       `mapstructure:"copy_image"`     // optional Docker image override
	DockerHost     string       `mapstructure:"docker_host"`    // optional Docker-compatible daemon host override
	Server         ServerConfig `mapstructure:"server"`
	Obfuscation    Obfuscation  `mapstructure:"obfuscation"`
}

// ServerConfig holds shared-host listener and authentication settings for ditto host.
type ServerConfig struct {
	Enabled          bool             `mapstructure:"enabled"`
	Addr             string           `mapstructure:"addr"`               // listen address, default ":8080"
	AdvertiseHost    string           `mapstructure:"advertise_host"`     // host/DNS name returned in remote DSNs
	DBBindHost       string           `mapstructure:"db_bind_host"`       // host interface used for published DB ports
	CopySecretSecret string           `mapstructure:"copy_secret_secret"` // secret reference used to derive per-copy credentials
	Auth             ServerAuthConfig `mapstructure:"auth"`
	DBTLS            ServerDBTLS      `mapstructure:"db_tls"`
}

// ServerAuthConfig holds authentication settings for ditto host.
// Either StaticToken (simple shared secret) or OIDC fields must be set.
// StaticToken is for evaluation and single-operator use; prefer OIDC in production.
type ServerAuthConfig struct {
	StaticToken string `mapstructure:"static_token"` // secret reference: env:VAR, file:/path, or literal
	Issuer      string `mapstructure:"issuer"`
	Audience    string `mapstructure:"audience"`
	JWKSURL     string `mapstructure:"jwks_url"`
	AdminClaim  string `mapstructure:"admin_claim"`
	AdminValue  string `mapstructure:"admin_value"`
}

// ServerDBTLS holds the TLS certificate material mounted into remote copy containers.
type ServerDBTLS struct {
	CertFile string `mapstructure:"cert_file"`
	KeyFile  string `mapstructure:"key_file"`
}

// Source holds connection parameters for the source database.
type Source struct {
	URL            string `mapstructure:"url"` // DSN alternative to individual fields
	Engine         string `mapstructure:"engine"`
	Host           string `mapstructure:"host"`
	Port           int    `mapstructure:"port"`
	Database       string `mapstructure:"database"`
	User           string `mapstructure:"user"`
	Password       string `mapstructure:"password"`        // plain password (dev only)
	PasswordSecret string `mapstructure:"password_secret"` // secret reference: env:VAR, file:/path, or arn:aws:...
}

// Target holds connection parameters for a database that ditto may refresh
// from a configured dump. Target refresh is destructive and must be explicitly
// enabled per target.
type Target struct {
	Engine                  string `mapstructure:"engine"`
	Host                    string `mapstructure:"host"`
	Port                    int    `mapstructure:"port"`
	Database                string `mapstructure:"database"`
	User                    string `mapstructure:"user"`
	Password                string `mapstructure:"password"`        // plain password (dev only)
	PasswordSecret          string `mapstructure:"password_secret"` // secret reference: env:VAR, file:/path, or arn:aws:...
	AllowDestructiveRefresh bool   `mapstructure:"allow_destructive_refresh"`
}

// Obfuscation holds post-restore PII scrubbing rules applied to every copy.
type Obfuscation struct {
	Rules []ObfuscationRule `mapstructure:"rules"`
}

// ObfuscationRule describes how a single table column should be scrubbed.
// Strategies: nullify, redact, mask, hash, replace.
type ObfuscationRule struct {
	Table    string `mapstructure:"table"`
	Column   string `mapstructure:"column"`
	Strategy string `mapstructure:"strategy"`  // nullify | redact | mask | hash | replace
	With     string `mapstructure:"with"`      // redact: replacement text (default "[redacted]")
	MaskChar string `mapstructure:"mask_char"` // mask: character to use (default "*")
	KeepLast int    `mapstructure:"keep_last"` // mask: preserve trailing N characters
	Type     string `mapstructure:"type"`      // replace: data type — email | name | phone | ip | url | uuid
	WarnOnly bool   `mapstructure:"warn_only"` // if true, 0-row updates emit a warning instead of an error
}

// Dump controls the dump scheduler.
type Dump struct {
	Schedule         string        `mapstructure:"schedule"`
	Path             string        `mapstructure:"path"`
	SchemaPath       string        `mapstructure:"schema_path"`        // optional: path for a schema-only (DDL) dump; empty = disabled
	StaleThreshold   int           `mapstructure:"stale_threshold"`    // seconds
	ClientImage      string        `mapstructure:"client_image"`       // optional helper image override for dump operations
	ExcludeTableData []string      `mapstructure:"exclude_table_data"` // tables to include in schema but exclude from row data
	OnFailure        DumpOnFailure `mapstructure:"on_failure"`
}

// DumpOnFailure configures an alert sent when a scheduled dump fails.
// Either WebhookURL or Exec may be set; WebhookURL takes precedence.
type DumpOnFailure struct {
	WebhookURL string `mapstructure:"webhook_url"` // HTTP endpoint to POST a JSON failure payload
	Exec       string `mapstructure:"exec"`        // shell command to run on failure
}

// Load reads and validates the config file at path. Environment variables
// with the prefix DITTO_ override config file values (e.g.
// DITTO_SOURCE_HOST overrides source.host, DITTO_SOURCE_URL overrides
// source.url).
func Load(path string) (*Config, error) {
	v := viper.New()

	// Apply defaults. source.port is intentionally omitted here — it is
	// engine-specific and applied after URL parsing in applyDefaults.
	v.SetDefault("copy_ttl_seconds", 7200)
	v.SetDefault("port_pool_start", 5433)
	v.SetDefault("port_pool_end", 5600)
	v.SetDefault("dump.schedule", "0 * * * *")
	v.SetDefault("dump.path", defaultDumpFilePath())
	v.SetDefault("dump.stale_threshold", 7200)
	v.SetDefault("dump.client_image", "")
	v.SetDefault("dump.exclude_table_data", []string{})
	v.SetDefault("warm_pool_size", 0)
	v.SetDefault("docker_host", "")
	v.SetDefault("server.addr", ":8080")
	v.SetDefault("server.enabled", false)

	v.SetEnvPrefix("DITTO")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	configureViperFile(v, path)

	if err := readViperConfig(v, path); err != nil {
		return nil, err
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("config: unmarshal: %w", err)
	}

	if cfg.Source.URL != "" {
		if err := applySourceURL(&cfg.Source); err != nil {
			return nil, fmt.Errorf("config: source.url: %w", err)
		}
	}

	applyPortDefault(&cfg.Source)
	applyTargetPortDefaults(cfg.Targets)

	return &cfg, validate(&cfg)
}

func configureViperFile(v *viper.Viper, path string) {
	if path != "" {
		v.SetConfigFile(path)
		return
	}
	v.SetConfigName("ditto")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("$HOME/.ditto")
	v.AddConfigPath("/etc/ditto")
}

func readViperConfig(v *viper.Viper, path string) error {
	if err := v.ReadInConfig(); err != nil {
		// A missing config file is only an error when a path was explicitly set.
		if path != "" {
			return fmt.Errorf("config: read %s: %w", path, err)
		}
	}
	return nil
}

// applySourceURL parses src.URL and back-fills any individual Source fields
// that are still at their zero values. Explicit fields always take precedence.
func applySourceURL(src *Source) error {
	u, err := url.Parse(src.URL)
	if err != nil {
		return err
	}

	if err := applySourceEngine(src, u); err != nil {
		return err
	}
	applySourceHost(src, u)
	if err := applySourcePort(src, u); err != nil {
		return err
	}
	applySourceDatabase(src, u)
	applySourceUser(src, u)
	applySourcePassword(src, u)
	return nil
}

func applySourceEngine(src *Source, u *url.URL) error {
	if src.Engine != "" {
		return nil
	}
	engine, err := engineFromScheme(u.Scheme)
	if err != nil {
		return err
	}
	src.Engine = engine
	return nil
}

func applySourceHost(src *Source, u *url.URL) {
	if src.Host == "" {
		src.Host = u.Hostname()
	}
}

func applySourcePort(src *Source, u *url.URL) error {
	if src.Port != 0 {
		return nil
	}
	portStr := u.Port()
	if portStr == "" {
		return nil
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("invalid port %q: %w", portStr, err)
	}
	src.Port = p
	return nil
}

func applySourceDatabase(src *Source, u *url.URL) {
	if src.Database == "" {
		src.Database = strings.TrimPrefix(u.Path, "/")
	}
}

func applySourceUser(src *Source, u *url.URL) {
	if src.User == "" && u.User != nil {
		src.User = u.User.Username()
	}
}

func applySourcePassword(src *Source, u *url.URL) {
	if src.Password != "" || src.PasswordSecret != "" {
		return
	}
	if u.User == nil {
		return
	}
	if p, ok := u.User.Password(); ok {
		src.Password = p
	}
}

// applyPortDefault sets a sensible default port when none was specified.
func applyPortDefault(src *Source) {
	if src.Port != 0 {
		return
	}
	switch src.Engine {
	case "postgres":
		src.Port = 5432
	case "mysql":
		src.Port = 3306
	}
}

func defaultPort(engine string) int {
	switch engine {
	case "postgres":
		return 5432
	case "mysql":
		return 3306
	default:
		return 0
	}
}

func applyTargetPortDefaults(targets map[string]Target) {
	for name, target := range targets {
		if target.Port == 0 {
			target.Port = defaultPort(target.Engine)
			targets[name] = target
		}
	}
}

// engineFromScheme maps a URL scheme to a ditto engine name.
func engineFromScheme(scheme string) (string, error) {
	switch strings.ToLower(scheme) {
	case "postgres", "postgresql":
		return "postgres", nil
	case "mysql", "mariadb": // mariadb DSN scheme accepted as alias
		return "mysql", nil
	default:
		return "", fmt.Errorf("unsupported scheme %q (supported: postgres, postgresql, mysql, mariadb)", scheme)
	}
}

// validate checks that required fields are present.
func validate(cfg *Config) error {
	missing := sourceMissingFields(cfg.Source)
	if cfg.CopyTTLSeconds <= 0 {
		return fmt.Errorf("config: copy_ttl_seconds must be greater than zero")
	}
	if !isValidPortPoolRange(cfg.PortPoolStart, cfg.PortPoolEnd) {
		return fmt.Errorf("config: invalid port pool range %d-%d", cfg.PortPoolStart, cfg.PortPoolEnd)
	}
	if cfg.Server.Enabled {
		validateServerConfig(cfg.Server, &missing)
	}
	if len(missing) > 0 {
		return fmt.Errorf("config: missing required fields: %v", missing)
	}
	if err := validateTargets(cfg.Targets); err != nil {
		return err
	}
	if err := validateDump(cfg.Dump); err != nil {
		return err
	}
	return validateObfuscation(cfg.Obfuscation.Rules)
}

func sourceMissingFields(src Source) []string {
	var missing []string
	if src.Engine == "" {
		missing = append(missing, "source.engine")
	}
	if src.Host == "" {
		missing = append(missing, "source.host")
	}
	if src.Database == "" {
		missing = append(missing, "source.database")
	}
	if src.User == "" {
		missing = append(missing, "source.user")
	}
	if src.Password == "" && src.PasswordSecret == "" {
		missing = append(missing, "source.password or source.password_secret")
	}
	return missing
}

func isValidPortPoolRange(start, end int) bool {
	if start <= 0 || end <= 0 {
		return false
	}
	return end >= start
}

func validateServerConfig(srv ServerConfig, missing *[]string) {
	if srv.AdvertiseHost == "" {
		*missing = append(*missing, "server.advertise_host")
	}
	if srv.DBBindHost == "" {
		*missing = append(*missing, "server.db_bind_host")
	}
	if srv.CopySecretSecret == "" {
		*missing = append(*missing, "server.copy_secret_secret")
	}
	validateServerAuth(srv.Auth, missing)
	if srv.DBTLS.CertFile == "" {
		*missing = append(*missing, "server.db_tls.cert_file")
	}
	if srv.DBTLS.KeyFile == "" {
		*missing = append(*missing, "server.db_tls.key_file")
	}
}

func hasOIDCFieldSet(auth ServerAuthConfig) bool {
	return auth.Issuer != "" || auth.Audience != "" || auth.JWKSURL != ""
}

// validateServerAuth appends missing field names when the auth config is
// neither a complete static-token config nor a complete OIDC config.
func validateServerAuth(auth ServerAuthConfig, missing *[]string) {
	hasStatic := auth.StaticToken != ""
	hasOIDC := hasOIDCFieldSet(auth)
	if !hasStatic && !hasOIDC {
		*missing = append(*missing, "server.auth.static_token or server.auth.issuer+audience+jwks_url")
	}
	if hasOIDC && !hasStatic {
		validateOIDCFields(auth, missing)
	}
}

func validateOIDCFields(auth ServerAuthConfig, missing *[]string) {
	if auth.Issuer == "" {
		*missing = append(*missing, "server.auth.issuer")
	}
	if auth.Audience == "" {
		*missing = append(*missing, "server.auth.audience")
	}
	if auth.JWKSURL == "" {
		*missing = append(*missing, "server.auth.jwks_url")
	}
}

func validateTargets(targets map[string]Target) error {
	for name, target := range targets {
		if err := validateTarget(name, target); err != nil {
			return err
		}
	}
	return nil
}

func targetMissingFields(t Target) []string {
	var missing []string
	if t.Engine == "" {
		missing = append(missing, "engine")
	}
	if t.Host == "" {
		missing = append(missing, "host")
	}
	if t.Database == "" {
		missing = append(missing, "database")
	}
	if t.User == "" {
		missing = append(missing, "user")
	}
	if t.Password == "" && t.PasswordSecret == "" {
		missing = append(missing, "password or password_secret")
	}
	return missing
}

func validateTarget(name string, target Target) error {
	if missing := targetMissingFields(target); len(missing) > 0 {
		return fmt.Errorf("config: target %q missing required fields: %v", name, missing)
	}
	switch target.Engine {
	case "postgres", "mysql":
	default:
		return fmt.Errorf("config: target %q has unsupported engine %q (supported: postgres, mysql)", name, target.Engine)
	}
	if target.Port <= 0 {
		return fmt.Errorf("config: target %q has invalid port %d", name, target.Port)
	}
	return nil
}

func validateDump(d Dump) error {
	for i, table := range d.ExcludeTableData {
		if table == "" {
			return fmt.Errorf("config: dump.exclude_table_data[%d]: table name must not be empty", i)
		}
		if strings.Contains(table, ".") {
			return fmt.Errorf("config: dump.exclude_table_data[%d]: table name %q must not contain a dot (provide the table name only, not schema.table)", i, table)
		}
	}
	return nil
}

var validStrategies = map[string]bool{
	"nullify": true,
	"redact":  true,
	"mask":    true,
	"hash":    true,
	"replace": true,
}

var validReplaceTypes = map[string]bool{
	"email": true,
	"name":  true,
	"phone": true,
	"ip":    true,
	"url":   true,
	"uuid":  true,
}

func validateObfuscation(rules []ObfuscationRule) error {
	for i, r := range rules {
		if err := validateObfuscationRule(i, r); err != nil {
			return err
		}
	}
	return nil
}

func validateObfuscationRule(i int, r ObfuscationRule) error {
	if r.Table == "" {
		return fmt.Errorf("config: obfuscation rule %d: table is required", i)
	}
	if r.Column == "" {
		return fmt.Errorf("config: obfuscation rule %d: column is required", i)
	}
	if !validStrategies[r.Strategy] {
		return fmt.Errorf("config: obfuscation rule %d: unknown strategy %q (use: nullify, redact, mask, hash, replace)", i, r.Strategy)
	}
	if r.MaskChar != "" && len([]rune(r.MaskChar)) != 1 {
		return fmt.Errorf("config: obfuscation rule %d: mask_char must be a single character", i)
	}
	if r.Strategy == "replace" && !validReplaceTypes[r.Type] {
		return fmt.Errorf("config: obfuscation rule %d: replace strategy requires type (email, name, phone, ip, url, uuid)", i)
	}
	return nil
}
