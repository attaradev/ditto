package cmd

import "testing"

func TestResolveServerURLExplicitWins(t *testing.T) {
	t.Setenv("DITTO_SERVER", "http://from-env")
	if got := resolveServerURL("http://explicit"); got != "http://explicit" {
		t.Fatalf("resolveServerURL: got %q, want explicit", got)
	}
}

func TestResolveServerURLUsesEnv(t *testing.T) {
	t.Setenv("DITTO_SERVER", "http://from-env")
	if got := resolveServerURL(""); got != "http://from-env" {
		t.Fatalf("resolveServerURL: got %q, want env", got)
	}
}
