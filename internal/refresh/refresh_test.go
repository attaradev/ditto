package refresh

import (
	"strings"
	"testing"

	"github.com/attaradev/ditto/internal/config"
	"github.com/attaradev/ditto/internal/store"
)

func TestRefreshDryRunDoesNotRequireDockerOrDump(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()

	cfg := testConfig()
	result, err := New(cfg, store.NewEventStore(db), nil).Refresh(t.Context(), "staging", Options{
		Confirm: "staging",
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("Refresh dry run: %v", err)
	}
	if !result.DryRun || result.Cleaned || result.Restored {
		t.Fatalf("result: got %+v", result)
	}

	events, err := store.NewEventStore(db).List("staging")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 1 || events[0].Action != "dry-run" {
		t.Fatalf("events: got %+v", events)
	}
}

func TestRefreshSafetyChecks(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Config
		opts    Options
		wantErr string
	}{
		{
			name:    "requires allow flag",
			cfg:     testConfigWithTarget(config.Target{Engine: "postgres", Host: "target.example.com", Port: 5432, Database: "app", User: "ditto", Password: "secret"}),
			opts:    Options{Confirm: "staging", DryRun: true},
			wantErr: "does not allow destructive refresh",
		},
		{
			name:    "requires confirmation",
			cfg:     testConfig(),
			opts:    Options{DryRun: true},
			wantErr: "confirmation mismatch",
		},
		{
			name: "rejects source target",
			cfg: testConfigWithTarget(config.Target{
				Engine:                  "postgres",
				Host:                    "source.example.com",
				Port:                    5432,
				Database:                "app",
				User:                    "ditto",
				Password:                "secret",
				AllowDestructiveRefresh: true,
			}),
			opts:    Options{Confirm: "staging", DryRun: true},
			wantErr: "matches the configured source database",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.cfg, nil, nil).Refresh(t.Context(), "staging", tc.opts)
			if err == nil {
				t.Fatal("Refresh: expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Refresh error: got %q, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestQuoteMySQLIdent(t *testing.T) {
	got := quoteMySQLIdent("a`b")
	if got != "`a``b`" {
		t.Fatalf("quoteMySQLIdent: got %q", got)
	}
}

func testConfig() *config.Config {
	return testConfigWithTarget(config.Target{
		Engine:                  "postgres",
		Host:                    "target.example.com",
		Port:                    5432,
		Database:                "app",
		User:                    "ditto",
		Password:                "secret",
		AllowDestructiveRefresh: true,
	})
}

func testConfigWithTarget(target config.Target) *config.Config {
	return &config.Config{
		Source: config.Source{
			Engine:   "postgres",
			Host:     "source.example.com",
			Port:     5432,
			Database: "app",
			User:     "ditto",
			Password: "secret",
		},
		Dump: config.Dump{Path: "/tmp/ditto-test-dump.gz"},
		Targets: map[string]config.Target{
			"staging": target,
		},
	}
}
