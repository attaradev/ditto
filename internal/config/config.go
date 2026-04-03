// Package config loads and validates ditto.yaml configuration.
package config

import (
	"fmt"
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
// DITTO_SOURCE_HOST overrides source.host).
func Load(path string) (*Config, error) {
	v := viper.New()

	// Apply defaults.
	v.SetDefault("copy_ttl_seconds", 7200)
	v.SetDefault("port_pool_start", 5433)
	v.SetDefault("port_pool_end", 5600)
	v.SetDefault("source.port", 5432)
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

	return &cfg, validate(&cfg)
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
