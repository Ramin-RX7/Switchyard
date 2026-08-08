package switchyard

import (
	"context"
	"sync/atomic"
	"time"
)

// limiter caps concurrent acquisitions (a counting semaphore) and tracks the
// live in-flight count. A nil *limiter means "unlimited" — all methods are
// safe to call on nil, so callers don't special-case uncapped scopes.
type limiter struct {
	sem      chan struct{}
	inflight atomic.Int64
}

// newLimiter returns a limiter of capacity max, or nil when max <= 0 (unlimited).
func newLimiter(max int) *limiter {
	if max <= 0 {
		return nil
	}
	return &limiter{sem: make(chan struct{}, max)}
}

// tryAcquire takes a slot without blocking. It returns true (slot held) or
// false (at capacity). Always true for a nil (unlimited) limiter.
func (l *limiter) tryAcquire() bool {
	if l == nil {
		return true
	}
	select {
	case l.sem <- struct{}{}:
		l.inflight.Add(1)
		return true
	default:
		return false
	}
}

// acquire takes a slot, waiting up to wait for one to free up (wait <= 0 means
// don't block — equivalent to tryAcquire). It returns false on timeout or if
// ctx is cancelled first. Always true for a nil limiter.
func (l *limiter) acquire(ctx context.Context, wait time.Duration) bool {
	if l == nil {
		return true
	}
	if wait <= 0 {
		return l.tryAcquire()
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case l.sem <- struct{}{}:
		l.inflight.Add(1)
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// release returns a previously-acquired slot. No-op for a nil limiter.
func (l *limiter) release() {
	if l == nil {
		return
	}
	l.inflight.Add(-1)
	<-l.sem
}

// count is the current number of held slots (0 for a nil limiter).
func (l *limiter) count() int {
	if l == nil {
		return 0
	}
	return int(l.inflight.Load())
}

// capacity is the configured maximum (0 for a nil/unlimited limiter).
func (l *limiter) capacity() int {
	if l == nil {
		return 0
	}
	return cap(l.sem)
}
