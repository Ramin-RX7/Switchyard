package switchyard_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// Hundreds of simultaneous requests are all served correctly and spread across
// the pool. Run under `-race` (make race) to prove there are no data races in
// the shared state (round-robin counter, backend proxies, transport).
func TestConcurrentRequestsSafeAndDistributed(t *testing.T) {
	a := newEchoBackend(t, "api1")
	b := newEchoBackend(t, "api2")
	p := mustNew(t, sw.Config{
		Backends:   []sw.BackendConfig{{ID: "api1", URL: a.URL}, {ID: "api2", URL: b.URL}},
		SetHeaders: map[string]string{"X-Real-IP": "$remote_addr"}, // exercise the header stage concurrently too
	})
	h := p.Handler()

	const n = 300
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "http://x/", nil))
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()

	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i, c)
		}
	}
	if total := a.count() + b.count(); total != n {
		t.Errorf("backend hits = %d, want %d", total, n)
	}
	if a.count() == 0 || b.count() == 0 {
		t.Errorf("expected both backends used: api1=%d api2=%d", a.count(), b.count())
	}
}

// With MaxInFlight set, a request that arrives while the ceiling is occupied is
// rejected immediately with 503 (non-blocking backpressure).
func TestMaxInFlightRejectsExcess(t *testing.T) {
	gate := make(chan struct{})
	hit := make(chan struct{}, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- struct{}{} // signal the request has reached the backend (slot held)
		<-gate            // block until released
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	p := mustNew(t, sw.Config{Backends: []sw.BackendConfig{{ID: "a", URL: backend.URL}}})
	p.MaxInFlight = 1
	h := p.Handler()

	// Occupy the single slot with a request that blocks inside the backend.
	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://x/", nil))
	<-hit // the slot is now held

	// A second request must be rejected right away.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://x/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("over-limit request = %d, want 503", rec.Code)
	}

	close(gate) // release the first request
}

// Without a limit, the same burst all succeeds (default is unlimited).
func TestNoLimitByDefault(t *testing.T) {
	a := newEchoBackend(t, "api1")
	p := mustNew(t, sw.Config{Backends: []sw.BackendConfig{{ID: "api1", URL: a.URL}}})
	if p.MaxInFlight != 0 {
		t.Fatalf("MaxInFlight default = %d, want 0 (unlimited)", p.MaxInFlight)
	}
	if rec := serve(p, "GET", "http://x/"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
