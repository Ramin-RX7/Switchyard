package switchyard_test

import (
	"sync"
	"testing"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// threeBackendPool returns the global pool of a proxy with three backends.
func threeBackendPool(t *testing.T) []*sw.Backend {
	t.Helper()
	a := newEchoBackend(t, "b0")
	b := newEchoBackend(t, "b1")
	c := newEchoBackend(t, "b2")
	p := mustNew(t, sw.Config{Backends: []sw.BackendConfig{
		{ID: "b0", URL: a.URL}, {ID: "b1", URL: b.URL}, {ID: "b2", URL: c.URL},
	}})
	return p.Pool.Backends()
}

// --- axis 2: the default RoundRobinSelector --------------------------------

func TestRoundRobinRotatesAndWraps(t *testing.T) {
	pool := threeBackendPool(t)
	s := &sw.RoundRobinSelector{}
	want := []string{"b0", "b1", "b2", "b0", "b1", "b2", "b0"}
	for i, w := range want {
		if got := s.Select(pool, sw.Request{}).ID; got != w {
			t.Fatalf("Select #%d = %q, want %q", i, got, w)
		}
	}
}

func TestRoundRobinEmptyPoolReturnsNil(t *testing.T) {
	s := &sw.RoundRobinSelector{}
	if b := s.Select(nil, sw.Request{}); b != nil {
		t.Fatalf("Select(empty) = %v, want nil", b)
	}
}

// Two locations sharing backends must rotate on independent counters.
func TestPerLocationCountersAreIndependent(t *testing.T) {
	a := newEchoBackend(t, "api1")
	b := newEchoBackend(t, "api2")
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "api1", URL: a.URL}, {ID: "api2", URL: b.URL}},
		Locations: []sw.LocationConfig{
			{Path: "/a/", Backends: []string{"api1", "api2"}},
			{Path: "/b/", Backends: []string{"api1", "api2"}},
		},
	}
	p := mustNew(t, cfg)

	// Advance /a/ once (→ api1), then /b/ should still start at api1 (its own counter).
	if got := p.Decider.Decide(sw.Request{Path: "/a/x"}).Backend.ID; got != "api1" {
		t.Fatalf("/a/ first = %q, want api1", got)
	}
	if got := p.Decider.Decide(sw.Request{Path: "/b/x"}).Backend.ID; got != "api1" {
		t.Fatalf("/b/ first = %q, want api1 (independent counter); shared counter would give api2", got)
	}
}

// Concurrency: the lock-free counter must not race and must hand out every slot.
func TestRoundRobinConcurrent(t *testing.T) {
	pool := threeBackendPool(t)
	s := &sw.RoundRobinSelector{}
	const n = 300
	counts := map[string]*int64{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := s.Select(pool, sw.Request{}).ID
			mu.Lock()
			if counts[id] == nil {
				var z int64
				counts[id] = &z
			}
			*counts[id]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	for _, b := range pool {
		if counts[b.ID] == nil || *counts[b.ID] != int64(n/len(pool)) {
			t.Errorf("backend %s got %v selections, want %d (even split)", b.ID, counts[b.ID], n/len(pool))
		}
	}
}

// --- axis 3: a user-supplied selector is honored ---------------------------

func TestCustomSelectorHonored(t *testing.T) {
	p, a, b := twoBackendProxy(t)
	p.Locations[0].Selector = fixedSelector{idx: 0} // always the first backend (api1)

	const n = 5
	for i := 0; i < n; i++ {
		serve(p, "GET", "http://x/api/users")
	}
	if a.count() != n || b.count() != 0 {
		t.Errorf("sticky selection: api1=%d api2=%d, want %d/0", a.count(), b.count(), n)
	}
}
