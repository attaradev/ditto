// Package config loads and validates ditto.yaml configuration.
package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

// Config is the top-level configuration structure, mirroring ditto.yaml.
type Config struct {
	Source Source `mapstructure:"source"`
	Dump   Dump   `mapstructure:"dump"`

	CopyTTLSeconds int `mapstructure:"copy_ttl_seconds"`
	PortPoolStart  int `mapstructure:"port_pool_start"`
	PortPoolEnd    int `mapstructure:"port_pool_end"`
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
	PasswordSecret string `mapstructure:"password_secret"` // AWS Secrets Manager ARN
}

// Dump controls the dump scheduler.
type Dump struct {
	Schedule       string `mapstructure:"schedule"`
	Path           string `mapstructure:"path"`
	StaleThreshold int    `mapstructure:"stale_threshold"` // seconds
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
	v.SetDefault("dump.path", "/data/dump/latest.gz")
	v.SetDefault("dump.stale_threshold", 7200)

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
	case "mariadb":
		src.Port = 3306
	}
}

// engineFromScheme maps a URL scheme to a ditto engine name.
func engineFromScheme(scheme string) (string, error) {
	switch strings.ToLower(scheme) {
	case "postgres", "postgresql":
		return "postgres", nil
	case "mysql", "mariadb":
		return "mariadb", nil
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
	if len(missing) > 0 {
		return fmt.Errorf("config: missing required fields: %v", missing)
	}
	return nil
}
