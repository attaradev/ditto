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

func TestConfigSourceURL(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantEngine string
		wantHost   string
		wantPort   int
		wantDB     string
		wantUser   string
		wantPass   string
	}{
		{
			name:       "postgres scheme",
			url:        "postgres://ditto_dump:secret@mydb.us-east-1.rds.amazonaws.com:5432/myapp",
			wantEngine: "postgres",
			wantHost:   "mydb.us-east-1.rds.amazonaws.com",
			wantPort:   5432,
			wantDB:     "myapp",
			wantUser:   "ditto_dump",
			wantPass:   "secret",
		},
		{
			name:       "postgresql scheme",
			url:        "postgresql://user:pass@localhost/testdb",
			wantEngine: "postgres",
			wantHost:   "localhost",
			wantDB:     "testdb",
			wantUser:   "user",
			wantPass:   "pass",
		},
		{
			name:       "mysql scheme maps to mariadb engine",
			url:        "mysql://root:pass@127.0.0.1:3306/app",
			wantEngine: "mariadb",
			wantHost:   "127.0.0.1",
			wantPort:   3306,
			wantDB:     "app",
			wantUser:   "root",
			wantPass:   "pass",
		},
		{
			name:       "mariadb scheme",
			url:        "mariadb://user:pass@db.example.com:3307/mydb",
			wantEngine: "mariadb",
			wantHost:   "db.example.com",
			wantPort:   3307,
			wantDB:     "mydb",
			wantUser:   "user",
			wantPass:   "pass",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "ditto.yaml")
			content := "source:\n  url: " + tc.url + "\n"
			if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}

			cfg, err := Load(cfgPath)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			if cfg.Source.Engine != tc.wantEngine {
				t.Errorf("Engine: got %q, want %q", cfg.Source.Engine, tc.wantEngine)
			}
			if cfg.Source.Host != tc.wantHost {
				t.Errorf("Host: got %q, want %q", cfg.Source.Host, tc.wantHost)
			}
			if tc.wantPort != 0 && cfg.Source.Port != tc.wantPort {
				t.Errorf("Port: got %d, want %d", cfg.Source.Port, tc.wantPort)
			}
			if cfg.Source.Database != tc.wantDB {
				t.Errorf("Database: got %q, want %q", cfg.Source.Database, tc.wantDB)
			}
			if cfg.Source.User != tc.wantUser {
				t.Errorf("User: got %q, want %q", cfg.Source.User, tc.wantUser)
			}
			if cfg.Source.Password != tc.wantPass {
				t.Errorf("Password: got %q, want %q", cfg.Source.Password, tc.wantPass)
			}
		})
	}
}

func TestConfigSourceURLExplicitFieldsWin(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ditto.yaml")
	// engine and host are set explicitly — they must not be overwritten by the URL.
	content := `
source:
  url: postgres://urluser:urlpass@urlhost:5432/urldb
  engine: mariadb
  host: explicit-host.example.com
  user: explicit-user
  password: explicit-pass
  database: explicit-db
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Source.Engine != "mariadb" {
		t.Errorf("Engine: got %q, want mariadb (explicit wins)", cfg.Source.Engine)
	}
	if cfg.Source.Host != "explicit-host.example.com" {
		t.Errorf("Host: got %q, want explicit-host.example.com", cfg.Source.Host)
	}
	if cfg.Source.User != "explicit-user" {
		t.Errorf("User: got %q, want explicit-user", cfg.Source.User)
	}
}

func TestConfigSourceURLInvalidScheme(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ditto.yaml")
	content := "source:\n  url: mongodb://user:pass@host/db\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for unsupported scheme, got nil")
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
