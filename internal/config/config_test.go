package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
  client_image: postgres:15-alpine

copy_ttl_seconds: 7200
port_pool_start: 5433
port_pool_end: 5600
docker_host: unix:///var/run/docker.sock
`
	cfgPath := writeConfigFile(t, content)
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
	if cfg.Dump.ClientImage != "postgres:15-alpine" {
		t.Errorf("Dump.ClientImage: got %q", cfg.Dump.ClientImage)
	}
	if cfg.DockerHost != "unix:///var/run/docker.sock" {
		t.Errorf("DockerHost: got %q", cfg.DockerHost)
	}
}

func TestConfigMissingRequired(t *testing.T) {
	cfgPath := writeConfigFile(t, "copy_ttl_seconds: 100\n")
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected validation error for missing required fields, got nil")
	}
}

type sourceWant struct {
	engine string
	host   string
	port   int
	db     string
	user   string
	pass   string
}

type sourceURLInput struct {
	scheme   string
	user     string
	password string
	host     string
	port     int
	database string
}

func assertSourceConfig(t *testing.T, got Source, want sourceWant) {
	t.Helper()
	if got.Engine != want.engine {
		t.Errorf("Engine: got %q, want %q", got.Engine, want.engine)
	}
	if got.Host != want.host {
		t.Errorf("Host: got %q, want %q", got.Host, want.host)
	}
	if want.port != 0 && got.Port != want.port {
		t.Errorf("Port: got %d, want %d", got.Port, want.port)
	}
	if got.Database != want.db {
		t.Errorf("Database: got %q, want %q", got.Database, want.db)
	}
	if got.User != want.user {
		t.Errorf("User: got %q, want %q", got.User, want.user)
	}
	if got.Password != want.pass {
		t.Errorf("Password: got %q, want %q", got.Password, want.pass)
	}
}

func TestConfigSourceURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want sourceWant
	}{
		{
			name: "postgres scheme",
			url: testSourceURL(sourceURLInput{
				scheme:   "postgres",
				user:     "ditto_dump",
				password: "secret",
				host:     "mydb.us-east-1.rds.amazonaws.com",
				port:     5432,
				database: "myapp",
			}),
			want: sourceWant{engine: "postgres", host: "mydb.us-east-1.rds.amazonaws.com", port: 5432, db: "myapp", user: "ditto_dump", pass: "secret"},
		},
		{
			name: "postgresql scheme",
			url: testSourceURL(sourceURLInput{
				scheme:   "postgresql",
				user:     "user",
				password: "pass",
				host:     "localhost",
				database: "testdb",
			}),
			want: sourceWant{engine: "postgres", host: "localhost", db: "testdb", user: "user", pass: "pass"},
		},
		{
			name: "mysql scheme maps to mysql engine",
			url: testSourceURL(sourceURLInput{
				scheme:   "mysql",
				user:     "root",
				password: "pass",
				host:     "127.0.0.1",
				port:     3306,
				database: "app",
			}),
			want: sourceWant{engine: "mysql", host: "127.0.0.1", port: 3306, db: "app", user: "root", pass: "pass"},
		},
		{
			name: "mariadb scheme maps to mysql engine",
			url: testSourceURL(sourceURLInput{
				scheme:   "mariadb",
				user:     "user",
				password: "pass",
				host:     "db.example.com",
				port:     3307,
				database: "mydb",
			}),
			want: sourceWant{engine: "mysql", host: "db.example.com", port: 3307, db: "mydb", user: "user", pass: "pass"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := writeConfigFile(t, "source:\n  url: "+tc.url+"\n")
			cfg, err := Load(cfgPath)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			assertSourceConfig(t, cfg.Source, tc.want)
		})
	}
}

func TestConfigSourceURLExplicitFieldsWin(t *testing.T) {
	sourceURL := testSourceURL(sourceURLInput{
		scheme:   "postgres",
		user:     "urluser",
		password: "urlpass",
		host:     "urlhost",
		port:     5432,
		database: "urldb",
	})
	// engine and host are set explicitly — they must not be overwritten by the URL.
	cfgPath := writeConfigFile(t, fmt.Sprintf(`
source:
  url: %s
  engine: mysql
  host: explicit-host.example.com
  user: explicit-user
  password: explicit-pass
  database: explicit-db
`, sourceURL))
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Source.Engine != "mysql" {
		t.Errorf("Engine: got %q, want mysql (explicit wins)", cfg.Source.Engine)
	}
	if cfg.Source.Host != "explicit-host.example.com" {
		t.Errorf("Host: got %q, want explicit-host.example.com", cfg.Source.Host)
	}
	if cfg.Source.User != "explicit-user" {
		t.Errorf("User: got %q, want explicit-user", cfg.Source.User)
	}
}

func TestConfigSourceURLInvalidScheme(t *testing.T) {
	cfgPath := writeConfigFile(t, "source:\n  url: "+testSourceURL(sourceURLInput{
		scheme:   "mongodb",
		user:     "user",
		password: "pass",
		host:     "host",
		database: "db",
	})+"\n")
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for unsupported scheme, got nil")
	}
}

func TestConfigEnvOverride(t *testing.T) {
	cfgPath := writeConfigFile(t, `
source:
  engine: postgres
  host: original.rds.amazonaws.com
  database: myapp
  user: ditto
  password: pass
`)
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

func TestConfigServerEnabledRequiresSharedHostFields(t *testing.T) {
	cfgPath := writeConfigFile(t, `
source:
  engine: postgres
  host: source.example.com
  database: app
  user: ditto
  password: secret
server:
  enabled: true
`)
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("Load: expected validation error for missing shared-host fields")
	}
	if got := err.Error(); got == "" || !containsAll(got,
		"server.advertise_host",
		"server.db_bind_host",
		"server.copy_secret_secret",
		"server.auth.static_token or server.auth.issuer+audience+jwks_url",
		"server.db_tls.cert_file",
		"server.db_tls.key_file",
	) {
		t.Fatalf("Load error missing expected fields: %v", err)
	}
}

func TestConfigTargets(t *testing.T) {
	cfgPath := writeConfigFile(t, `
source:
  engine: postgres
  host: source.example.com
  database: app
  user: ditto
  password: secret

targets:
  staging:
    engine: mysql
    host: staging.example.com
    database: app
    user: ditto_refresh
    password_secret: env:DITTO_TARGET_PASSWORD
    allow_destructive_refresh: true
`)
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	target := cfg.Targets["staging"]
	if target.Engine != "mysql" {
		t.Fatalf("Engine: got %q, want mysql", target.Engine)
	}
	if target.Port != 3306 {
		t.Fatalf("Port: got %d, want 3306", target.Port)
	}
	if !target.AllowDestructiveRefresh {
		t.Fatal("AllowDestructiveRefresh: got false, want true")
	}
}

func TestConfigInvalidTarget(t *testing.T) {
	cfgPath := writeConfigFile(t, `
source:
  engine: postgres
  host: source.example.com
  database: app
  user: ditto
  password: secret

targets:
  staging:
    engine: sqlite
    host: staging.example.com
    database: app
    user: ditto_refresh
    password: secret
`)
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("Load: expected invalid target error, got nil")
	}
	if !strings.Contains(err.Error(), `target "staging" has unsupported engine`) {
		t.Fatalf("Load error: %v", err)
	}
}

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ditto.yaml")
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

func testSourceURL(in sourceURLInput) string {
	u := &url.URL{
		Scheme: in.scheme,
		User:   url.UserPassword(in.user, in.password),
		Host:   in.host,
		Path:   "/" + in.database,
	}
	if in.port != 0 {
		u.Host = u.Host + ":" + fmt.Sprint(in.port)
	}
	return u.String()
}

func containsAll(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}
