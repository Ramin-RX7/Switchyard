package switchyard_test

import (
	"testing"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// --- axis 2: the default StaticPool ----------------------------------------

func TestStaticPoolReturnsList(t *testing.T) {
	pool := threeBackendPool(t)
	np := sw.NewStaticPool(pool)
	got := np.Backends()
	if len(got) != len(pool) {
		t.Fatalf("StaticPool size = %d, want %d", len(got), len(pool))
	}
	for i := range pool {
		if got[i] != pool[i] {
			t.Errorf("StaticPool[%d] = %v, want %v", i, got[i], pool[i])
		}
	}
}

// --- axis 3: a user-supplied BackendPool is honored ------------------------

// filterPool exposes only backends whose ID is in keep.
type filterPool struct {
	all  []*sw.Backend
	keep string
}

func (p filterPool) Backends() []*sw.Backend {
	var out []*sw.Backend
	for _, b := range p.all {
		if b.ID == p.keep {
			out = append(out, b)
		}
	}
	return out
}

func TestCustomPoolHonored(t *testing.T) {
	p, a, b := twoBackendProxy(t)
	// Restrict the location's pool to api2 only, via a custom BackendPool.
	p.Locations[0].Pool = filterPool{all: p.Pool.Backends(), keep: "api2"}

	const n = 4
	for i := 0; i < n; i++ {
		serve(p, "GET", "http://x/api/x")
	}
	if b.count() != n || a.count() != 0 {
		t.Errorf("custom pool: api1=%d api2=%d, want 0/%d", a.count(), b.count(), n)
	}
}
