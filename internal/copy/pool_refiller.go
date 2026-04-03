package copy

import (
	"context"
	"log/slog"
	"time"
)

// WarmPoolRefiller maintains a target number of pre-warmed copies in the pool.
// When a copy is claimed, Signal() triggers an immediate refill check.
// A 30-second heartbeat ticker catches any silent drops (e.g. TTL expiry of
// un-claimed warm copies, which is disabled by ListExpired, but kept as defence).
type WarmPoolRefiller struct {
	mgr    *Manager
	target int
	refill chan struct{} // buffered(1); Signal is non-blocking
}

// NewWarmPoolRefiller creates a refiller targeting target warm copies.
// target=0 means the pool is disabled; Run returns immediately.
func NewWarmPoolRefiller(mgr *Manager, target int) *WarmPoolRefiller {
	return &WarmPoolRefiller{
		mgr:    mgr,
		target: target,
		refill: make(chan struct{}, 1),
	}
}

// Signal queues a refill check. Non-blocking: if a signal is already pending,
// this call is a no-op.
func (r *WarmPoolRefiller) Signal() {
	select {
	case r.refill <- struct{}{}:
	default:
	}
}

// Run blocks until ctx is cancelled. It refills the pool on each signal and on
// a 30-second heartbeat. Returns immediately when target == 0.
func (r *WarmPoolRefiller) Run(ctx context.Context) {
	if r.target == 0 {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Initial fill: don't wait for the first tick or signal.
	r.fill(ctx)

	for {
		select {
		case <-r.refill:
			r.fill(ctx)
		case <-ticker.C:
			r.fill(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (r *WarmPoolRefiller) fill(ctx context.Context) {
	count, err := r.mgr.copies.CountWarm()
	if err != nil {
		slog.Error("pool: count warm copies failed", "err", err)
		return
	}
	needed := r.target - count
	for i := 0; i < needed; i++ {
		if ctx.Err() != nil {
			return
		}
		c, err := r.mgr.provisionWarm(ctx)
		if err != nil {
			slog.Error("pool: warm provision failed", "err", err)
			return // back off; next tick will retry
		}
		slog.Info("pool: warm copy ready", "id", c.ID)
	}
}
