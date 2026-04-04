package copy

import (
	"sync"
	"testing"
)

func TestPortPoolAllocateAndRelease(t *testing.T) {
	pool := NewPortPool(5433, 5440, nil)

	port, err := pool.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if port < 5433 || port > 5440 {
		t.Errorf("port %d out of range [5433, 5440]", port)
	}

	pool.Release(port)
	if f := pool.Free(); f != 8 {
		t.Errorf("Free after release: got %d, want 8", f)
	}
}

func TestPortPoolExhaustion(t *testing.T) {
	pool := NewPortPool(5433, 5434, nil) // only 2 ports

	p1, err := pool.Allocate()
	if err != nil {
		t.Fatalf("first Allocate: %v", err)
	}
	p2, err := pool.Allocate()
	if err != nil {
		t.Fatalf("second Allocate: %v", err)
	}
	if p1 == p2 {
		t.Errorf("allocated same port twice: %d", p1)
	}

	_, err = pool.Allocate()
	if err == nil {
		t.Fatal("expected error when pool exhausted, got nil")
	}

	pool.Release(p1)
	p3, err := pool.Allocate()
	if err != nil {
		t.Fatalf("Allocate after release: %v", err)
	}
	if p3 != p1 {
		t.Errorf("expected re-allocation of port %d, got %d", p1, p3)
	}
}

func TestPortPoolConcurrency(t *testing.T) {
	// Allocate all 50 ports concurrently, hold them, check no duplicates,
	// then release all. This confirms no two goroutines receive the same port
	// simultaneously.
	const (
		poolSize   = 50
		goroutines = 50
	)
	pool := NewPortPool(5433, 5433+poolSize-1, nil)

	type result struct {
		port int
		err  error
	}
	results := make([]result, goroutines)

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Go(func() {
			port, err := pool.Allocate()
			results[i] = result{port: port, err: err}
		})
	}
	wg.Wait()

	seen := map[int]bool{}
	for i, r := range results {
		if r.err != nil {
			t.Errorf("goroutine %d: Allocate error: %v", i, r.err)
			continue
		}
		if seen[r.port] {
			t.Errorf("duplicate port allocation: %d (goroutine %d)", r.port, i)
		}
		seen[r.port] = true
	}

	// Release all; pool should be fully free again.
	for _, r := range results {
		if r.err == nil {
			pool.Release(r.port)
		}
	}
	if f := pool.Free(); f != poolSize {
		t.Errorf("Free after all releases: got %d, want %d", f, poolSize)
	}
}

func TestPortPoolPreloadedOccupied(t *testing.T) {
	pool := NewPortPool(5433, 5435, []int{5433, 5434})

	// 5433 and 5434 are pre-occupied; only 5435 should be free.
	if f := pool.Free(); f != 1 {
		t.Errorf("Free: got %d, want 1", f)
	}

	port, err := pool.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if port != 5435 {
		t.Errorf("expected port 5435, got %d", port)
	}
}
