package switchyard

import "sync/atomic"

// BackendSelector chooses which backend from a pool should handle a request. It
// is the pluggable load-balancing stage. The default is RoundRobinSelector, but
// SDK users may supply their own — weighted, least-connections, sticky-by-IP,
// etc. — either globally (via the Proxy) or per-location (via Location.Selector).
//
// pool is the candidate backends for this request; req is the immutable request
// snapshot. Return nil to signal "no backend available" (the caller turns this
// into a 502).
type BackendSelector interface {
	Select(pool []*Backend, req Request) *Backend
}

// RoundRobinSelector is the default BackendSelector: it rotates through the pool
// using a lock-free atomic counter. Each pool (the global one and each location)
// gets its own instance, so their rotations are independent.
//
// Embed RoundRobinSelector to override only part of the behavior while reusing
// the rotation, calling s.RoundRobinSelector.Select(pool, req) as a fallback.
// Do not copy a RoundRobinSelector after first use: it holds an atomic counter.
type RoundRobinSelector struct {
	next atomic.Uint64
}

// Select returns the next backend in rotation, or nil for an empty pool.
func (s *RoundRobinSelector) Select(pool []*Backend, req Request) *Backend {
	if len(pool) == 0 {
		return nil
	}
	i := s.next.Add(1) - 1
	return pool[int(i%uint64(len(pool)))]
}
