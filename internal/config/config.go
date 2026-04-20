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
	Source Source `mapstructure:"source"`
	Dump   Dump   `mapstructure:"dump"`

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

// Source holds connection parameters for the RDS source database.
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
	Schedule       string        `mapstructure:"schedule"`
	Path           string        `mapstructure:"path"`
	SchemaPath     string        `mapstructure:"schema_path"`     // optional: path for a schema-only (DDL) dump; empty = disabled
	StaleThreshold int           `mapstructure:"stale_threshold"` // seconds
	ClientImage    string        `mapstructure:"client_image"`    // optional helper image override for dump operations
	OnFailure      DumpOnFailure `mapstructure:"on_failure"`
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
	v.SetDefault("warm_pool_size", 0)
	v.SetDefault("docker_host", "")
	v.SetDefault("server.addr", ":8080")
	v.SetDefault("server.enabled", false)

	v.SetEnvPrefix("DITTO")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigName("ditto")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("$HOME/.ditto")
		v.AddConfigPath("/etc/ditto")
	}

	if err := v.ReadInConfig(); err != nil {
		// A missing config file is only an error when a path was explicitly set.
		if path != "" {
			return nil, fmt.Errorf("config: read %s: %w", path, err)
		}
		// Otherwise fall through with defaults.
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

	return &cfg, validate(&cfg)
}

// applySourceURL parses src.URL and back-fills any individual Source fields
// that are still at their zero values. Explicit fields always take precedence.
func applySourceURL(src *Source) error {
	u, err := url.Parse(src.URL)
	if err != nil {
		return err
	}

	// Derive engine from scheme.
	engine, err := engineFromScheme(u.Scheme)
	if err != nil {
		return err
	}

	if src.Engine == "" {
		src.Engine = engine
	}
	if src.Host == "" {
		src.Host = u.Hostname()
	}
	if src.Port == 0 {
		if portStr := u.Port(); portStr != "" {
			p, err := strconv.Atoi(portStr)
			if err != nil {
				return fmt.Errorf("invalid port %q: %w", portStr, err)
			}
			src.Port = p
		}
	}
	if src.Database == "" {
		src.Database = strings.TrimPrefix(u.Path, "/")
	}
	if src.User == "" && u.User != nil {
		src.User = u.User.Username()
	}
	if src.Password == "" && src.PasswordSecret == "" && u.User != nil {
		if p, ok := u.User.Password(); ok {
			src.Password = p
		}
	}
	return nil
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
	var missing []string
	if cfg.Source.Engine == "" {
		missing = append(missing, "source.engine")
	}
	if cfg.Source.Host == "" {
		missing = append(missing, "source.host")
	}
	if cfg.Source.Database == "" {
		missing = append(missing, "source.database")
	}
	if cfg.Source.User == "" {
		missing = append(missing, "source.user")
	}
	if cfg.Source.Password == "" && cfg.Source.PasswordSecret == "" {
		missing = append(missing, "source.password or source.password_secret")
	}
	if cfg.CopyTTLSeconds <= 0 {
		return fmt.Errorf("config: copy_ttl_seconds must be greater than zero")
	}
	if cfg.PortPoolStart <= 0 || cfg.PortPoolEnd <= 0 || cfg.PortPoolEnd < cfg.PortPoolStart {
		return fmt.Errorf("config: invalid port pool range %d-%d", cfg.PortPoolStart, cfg.PortPoolEnd)
	}
	if cfg.Server.Enabled {
		if cfg.Server.AdvertiseHost == "" {
			missing = append(missing, "server.advertise_host")
		}
		if cfg.Server.DBBindHost == "" {
			missing = append(missing, "server.db_bind_host")
		}
		if cfg.Server.CopySecretSecret == "" {
			missing = append(missing, "server.copy_secret_secret")
		}
		// Auth: require either static_token OR full OIDC config, not both.
		hasStatic := cfg.Server.Auth.StaticToken != ""
		hasOIDC := cfg.Server.Auth.Issuer != "" || cfg.Server.Auth.Audience != "" || cfg.Server.Auth.JWKSURL != ""
		if !hasStatic && !hasOIDC {
			missing = append(missing, "server.auth.static_token or server.auth.issuer+audience+jwks_url")
		}
		if hasOIDC && !hasStatic {
			if cfg.Server.Auth.Issuer == "" {
				missing = append(missing, "server.auth.issuer")
			}
			if cfg.Server.Auth.Audience == "" {
				missing = append(missing, "server.auth.audience")
			}
			if cfg.Server.Auth.JWKSURL == "" {
				missing = append(missing, "server.auth.jwks_url")
			}
		}
		if cfg.Server.DBTLS.CertFile == "" {
			missing = append(missing, "server.db_tls.cert_file")
		}
		if cfg.Server.DBTLS.KeyFile == "" {
			missing = append(missing, "server.db_tls.key_file")
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("config: missing required fields: %v", missing)
	}
	return validateObfuscation(cfg.Obfuscation.Rules)
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
		if r.Table == "" {
			return fmt.Errorf("config: obfuscation rule %d: table is required", i)
		}
		if r.Column == "" {
			return fmt.Errorf("config: obfuscation rule %d: column is required", i)
		}
		if !validStrategies[r.Strategy] {
			return fmt.Errorf("config: obfuscation rule %d: unknown strategy %q (use: nullify, redact, mask, hash)", i, r.Strategy)
		}
		if r.MaskChar != "" && len([]rune(r.MaskChar)) != 1 {
			return fmt.Errorf("config: obfuscation rule %d: mask_char must be a single character", i)
		}
		if r.Strategy == "replace" && !validReplaceTypes[r.Type] {
			return fmt.Errorf("config: obfuscation rule %d: replace strategy requires type (email, name, phone, ip, url, uuid)", i)
		}
	}
	return nil
}
