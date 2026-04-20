// Package copy manages the lifecycle of ephemeral database copies.
package copy

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// PortPool manages a bounded set of host ports for copy containers.
// Allocate() returns a free port; Release() returns it to the pool.
// All operations are safe for concurrent use.
type PortPool struct {
	mu     sync.Mutex
	start  int
	end    int
	inUse  map[int]bool
	notify chan struct{} // buffered capacity-1 channel; signalled on Release
}

// NewPortPool creates a pool covering ports [start, end] inclusive.
// Ports in occupied are pre-marked as in-use (loaded from SQLite at startup).
func NewPortPool(start, end int, occupied []int) *PortPool {
	p := &PortPool{
		start:  start,
		end:    end,
		inUse:  make(map[int]bool, len(occupied)),
		notify: make(chan struct{}, 1),
	}
	for _, port := range occupied {
		p.inUse[port] = true
	}
	return p
}

// Allocate returns a free port, marking it as in-use. It scans start-to-end
// and additionally TCP-dials each candidate to catch externally occupied ports
// (e.g. orphaned containers from a previous crash).
//
// Returns an error if no port is available.
func (p *PortPool) Allocate() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for port := p.start; port <= p.end; port++ {
		if p.inUse[port] {
			continue
		}
		// TCP check inside the lock: ensures two goroutines cannot race on the
		// same port even if neither has it in inUse yet.
		if portOccupied(port) {
			continue
		}
		p.inUse[port] = true
		return port, nil
	}
	return 0, fmt.Errorf("port pool exhausted (%d–%d all in use)", p.start, p.end)
}

// AllocateWithTimeout waits up to timeout for a free port, retrying each time
// a port is released. Returns an error if the timeout elapses before a port
// becomes available.
func (p *PortPool) AllocateWithTimeout(ctx context.Context, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for {
		port, err := p.Allocate()
		if err == nil {
			return port, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			total := p.end - p.start + 1
			return 0, fmt.Errorf("port pool exhausted (%d–%d, %d ports total) after waiting %s — expand port_pool_end in ditto.yaml", p.start, p.end, total, timeout)
		}
		timer := time.NewTimer(remaining)
		select {
		case <-p.notify:
			timer.Stop()
		case <-timer.C:
			total := p.end - p.start + 1
			return 0, fmt.Errorf("port pool exhausted (%d–%d, %d ports total) after waiting %s — expand port_pool_end in ditto.yaml", p.start, p.end, total, timeout)
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		}
	}
}

// Release marks port as free and signals any goroutines waiting in
// AllocateWithTimeout. It is safe to call on a port that was never allocated.
func (p *PortPool) Release(port int) {
	p.mu.Lock()
	delete(p.inUse, port)
	p.mu.Unlock()
	// Non-blocking send: if a signal is already pending, the waiter will pick it up.
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

// InUse returns a snapshot of currently allocated ports.
func (p *PortPool) InUse() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	ports := make([]int, 0, len(p.inUse))
	for port := range p.inUse {
		ports = append(ports, port)
	}
	return ports
}

// Free returns the number of unallocated ports in the pool.
func (p *PortPool) Free() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	total := p.end - p.start + 1
	return total - len(p.inUse)
}

// portOccupied returns true if something is already listening on port.
func portOccupied(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
