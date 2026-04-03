package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigLoad(t *testing.T) {
	content := `
source:
  engine: postgres
  host: mydb.us-east-1.rds.amazonaws.com
  port: 5432
  database: myapp
  user: ditto_dump
  password: secret123

dump:
  schedule: "0 * * * *"
  path: /data/dump/latest.gz
  stale_threshold: 7200

copy_ttl_seconds: 7200
port_pool_start: 5433
port_pool_end: 5600
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ditto.yaml")
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Source.Engine != "postgres" {
		t.Errorf("Source.Engine: got %q, want %q", cfg.Source.Engine, "postgres")
	}
	if cfg.Source.Host != "mydb.us-east-1.rds.amazonaws.com" {
		t.Errorf("Source.Host: got %q", cfg.Source.Host)
	}
	if cfg.CopyTTLSeconds != 7200 {
		t.Errorf("CopyTTLSeconds: got %d, want 7200", cfg.CopyTTLSeconds)
	}
	if cfg.PortPoolStart != 5433 {
		t.Errorf("PortPoolStart: got %d, want 5433", cfg.PortPoolStart)
	}
}

func TestConfigMissingRequired(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ditto.yaml")
	// Write a config that has no source fields.
	if err := os.WriteFile(cfgPath, []byte("copy_ttl_seconds: 100\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected validation error for missing required fields, got nil")
	}
}

func TestConfigEnvOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ditto.yaml")
	content := `
source:
  engine: postgres
  host: original.rds.amazonaws.com
  database: myapp
  user: ditto
  password: pass
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DITTO_SOURCE_HOST", "override.rds.amazonaws.com")

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Source.Host != "override.rds.amazonaws.com" {
		t.Errorf("env override not applied: got %q, want %q",
			cfg.Source.Host, "override.rds.amazonaws.com")
	}
}
