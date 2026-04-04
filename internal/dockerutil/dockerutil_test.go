package dockerutil

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestResolveHostConfiguredOverrideWins(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///ignored.sock")

	var seen []string
	host, err := resolveHost(
		context.Background(),
		"unix:///configured.sock",
		os.Getenv,
		func(_ context.Context, host string) error {
			seen = append(seen, host)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("resolveHost: %v", err)
	}
	if host != "unix:///configured.sock" {
		t.Fatalf("host: got %q, want %q", host, "unix:///configured.sock")
	}
	if !reflect.DeepEqual(seen, []string{"unix:///configured.sock"}) {
		t.Fatalf("probe order: got %v", seen)
	}
}

func TestResolveHostUsesEnvBeforeDefault(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///from-env.sock")

	var seen []string
	host, err := resolveHost(
		context.Background(),
		"",
		os.Getenv,
		func(_ context.Context, host string) error {
			seen = append(seen, host)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("resolveHost: %v", err)
	}
	if host != "unix:///from-env.sock" {
		t.Fatalf("host: got %q, want %q", host, "unix:///from-env.sock")
	}
	if !reflect.DeepEqual(seen, []string{"unix:///from-env.sock"}) {
		t.Fatalf("probe order: got %v", seen)
	}
}

func TestResolveHostFallsBackToDefaultSocket(t *testing.T) {
	var seen []string
	host, err := resolveHost(
		context.Background(),
		"",
		func(string) string { return "" },
		func(_ context.Context, host string) error {
			seen = append(seen, host)
			return nil // default socket is reachable
		},
	)
	if err != nil {
		t.Fatalf("resolveHost: %v", err)
	}
	if host != "unix:///var/run/docker.sock" {
		t.Fatalf("host: got %q, want %q", host, "unix:///var/run/docker.sock")
	}
}

func TestResolveHostDoesNotFallbackPastBrokenEnvOverride(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///broken.sock")

	_, err := resolveHost(
		context.Background(),
		"",
		os.Getenv,
		func(_ context.Context, host string) error {
			return errors.New("dial failed")
		},
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "DOCKER_HOST=unix:///broken.sock") {
		t.Fatalf("error: got %q", err)
	}
}

func TestResolveHostMissingDaemonReturnsHelpfulError(t *testing.T) {
	_, err := resolveHost(
		context.Background(),
		"",
		func(string) string { return "" },
		func(_ context.Context, host string) error {
			return errors.New("not reachable")
		},
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "export DOCKER_HOST=") {
		t.Fatalf("error: got %q", err)
	}
}
