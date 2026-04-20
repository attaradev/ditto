package engine_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/attaradev/ditto/engine"
	"github.com/docker/docker/client"
)

type stubEngine struct{ name string }

func (s *stubEngine) Name() string           { return s.name }
func (s *stubEngine) ContainerImage() string { return "stub:latest" }
func (s *stubEngine) ContainerSpec(_ engine.CopyBootstrap) engine.ContainerSpec {
	return engine.ContainerSpec{}
}
func (s *stubEngine) ContainerPort() int { return 1234 }
func (s *stubEngine) ConnectionString(conn engine.ConnectionConfig) string {
	return fmt.Sprintf("stub://%s:%d", conn.Host, conn.Port)
}
func (s *stubEngine) Dump(_ context.Context, _ *client.Client, _ string, _ engine.SourceConfig, _ string, _ engine.DumpOptions) error {
	return nil
}
func (s *stubEngine) Restore(_ context.Context, _ *client.Client, _ string, _ string, _ engine.CopyBootstrap) error {
	return nil
}
func (s *stubEngine) DumpFromContainer(_ context.Context, _ *client.Client, _ string, _ string, _ engine.CopyBootstrap, _ engine.DumpOptions) error {
	return nil
}
func (s *stubEngine) WaitReady(_ engine.ConnectionConfig, _ time.Duration) error { return nil }

func TestRegistryRegisterAndGet(t *testing.T) {
	e := &stubEngine{name: "stub-" + t.Name()}
	engine.Register(e)

	got, err := engine.Get(e.name)
	if err != nil {
		t.Fatalf("Get(%q): %v", e.name, err)
	}
	if got.Name() != e.name {
		t.Errorf("Name: got %q, want %q", got.Name(), e.name)
	}
}

func TestRegistryUnknownEngine(t *testing.T) {
	_, err := engine.Get("definitely-not-registered")
	if err == nil {
		t.Fatal("expected error for unknown engine, got nil")
	}
}

func TestRegistryDuplicatePanics(t *testing.T) {
	name := "dup-" + t.Name()
	engine.Register(&stubEngine{name: name})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for duplicate registration")
		}
	}()
	engine.Register(&stubEngine{name: name})
}
