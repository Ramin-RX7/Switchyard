package switchyard_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

func ip(i int) *int                   { return &i }
func dp(d time.Duration) *sw.Duration { x := sw.Duration(d); return &x }
func sp(s string) *string             { return &s }

// blockingBackend serves requests that park until gate is closed, signalling
// each arrival on hit. Lets tests hold a connection slot deterministically.
func blockingBackend(t *testing.T) (url string, gate chan struct{}, hit chan struct{}) {
	t.Helper()
	gate = make(chan struct{})
	hit = make(chan struct{}, 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- struct{}{}
		<-gate
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, gate, hit
}

// --- backend max_connections: reject ---------------------------------------

func TestBackendMaxConnectionsReject(t *testing.T) {
	url, gate, hit := blockingBackend(t)
	defer close(gate)
	p := mustNew(t, sw.Config{
		Backends: []sw.BackendConfig{{ID: "a", URL: url, MaxConnections: ip(1)}},
	})
	h := p.Handler()

	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://x/", nil))
	<-hit // slot held

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://x/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("over-cap request = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "capacity reached") {
		t.Errorf("body = %q, want default overflow message", rec.Body.String())
	}
}

// --- configurable reject response ------------------------------------------

func TestOverflowCustomRejectResponse(t *testing.T) {
	url, gate, hit := blockingBackend(t)
	defer close(gate)
	p := mustNew(t, sw.Config{
		Backends: []sw.BackendConfig{{ID: "a", URL: url, MaxConnections: ip(1)}},
		Overflow: &sw.OverflowConfig{Status: ip(429), Body: sp("slow down")},
	})
	h := p.Handler()
	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://x/", nil))
	<-hit

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://x/", nil))
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "slow down") {
		t.Errorf("custom reject = %d %q, want 429 'slow down'", rec.Code, rec.Body.String())
	}
}

// --- location max_connections ----------------------------------------------

func TestLocationMaxConnectionsReject(t *testing.T) {
	url, gate, hit := blockingBackend(t)
	defer close(gate)
	p := mustNew(t, sw.Config{
		Backends: []sw.BackendConfig{{ID: "a", URL: url}}, // backend uncapped
		Locations: []sw.LocationConfig{
			{Path: "/api/", Backends: []string{"a"}, MaxConnections: ip(1)},
		},
	})
	h := p.Handler()
	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://x/api/x", nil))
	<-hit
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://x/api/y", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("over-cap location request = %d, want 503", rec.Code)
	}
}

// --- queue strategy: waits for a slot then succeeds, or times out ----------

func TestQueueStrategySucceedsWhenSlotFrees(t *testing.T) {
	url, gate, hit := blockingBackend(t)
	p := mustNew(t, sw.Config{
		Backends: []sw.BackendConfig{{ID: "a", URL: url, MaxConnections: ip(1)}},
		Overflow: &sw.OverflowConfig{Strategy: "queue", QueueTimeout: dp(2 * time.Second)},
	})
	h := p.Handler()

	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://x/", nil))
	<-hit // req1 holds the only slot

	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "http://x/", nil)) // queues
		done <- rec.Code
	}()

	close(gate) // release req1 (and later req2) → req2 should acquire and succeed
	if code := <-done; code != http.StatusOK {
		t.Fatalf("queued request = %d, want 200", code)
	}
}

func TestQueueStrategyTimesOut(t *testing.T) {
	url, gate, hit := blockingBackend(t)
	defer close(gate)
	p := mustNew(t, sw.Config{
		Backends: []sw.BackendConfig{{ID: "a", URL: url, MaxConnections: ip(1)}},
		Overflow: &sw.OverflowConfig{Strategy: "queue", QueueTimeout: dp(80 * time.Millisecond)},
	})
	h := p.Handler()
	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://x/", nil))
	<-hit // slot held for the duration

	start := time.Now()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://x/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("queued-then-timed-out = %d, want 503", rec.Code)
	}
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Errorf("returned after %v, expected it to wait ~queue_timeout", elapsed)
	}
}

// --- reroute strategy -------------------------------------------------------

func TestRerouteToFreeBackend(t *testing.T) {
	urlA, gateA, hitA := blockingBackend(t)
	urlB, gateB, hitB := blockingBackend(t)
	defer close(gateA)
	p := mustNew(t, sw.Config{
		Backends: []sw.BackendConfig{
			{ID: "a", URL: urlA, MaxConnections: ip(1)},
			{ID: "b", URL: urlB, MaxConnections: ip(1)},
		},
		Locations: []sw.LocationConfig{{Path: "/", Backends: []string{"a", "b"}}},
		Overflow:  &sw.OverflowConfig{Strategy: "reroute"},
	})
	p.Locations[0].Selector = fixedSelector{idx: 0} // always try "a" first
	h := p.Handler()

	// req1 occupies a's only slot.
	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://x/", nil))
	<-hitA

	// req2 also selects a (full) → must reroute to b.
	code := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "http://x/", nil))
		code <- rec.Code
	}()
	<-hitB       // reroute reached b
	close(gateB) // let req2 finish
	if c := <-code; c != http.StatusOK {
		t.Fatalf("rerouted request = %d, want 200", c)
	}
}

func TestRerouteAllFullRejects(t *testing.T) {
	urlA, gateA, hitA := blockingBackend(t)
	urlB, gateB, hitB := blockingBackend(t)
	defer close(gateA)
	defer close(gateB)
	p := mustNew(t, sw.Config{
		Backends: []sw.BackendConfig{
			{ID: "a", URL: urlA, MaxConnections: ip(1)},
			{ID: "b", URL: urlB, MaxConnections: ip(1)},
		},
		Locations: []sw.LocationConfig{{Path: "/", Backends: []string{"a", "b"}}},
		Overflow:  &sw.OverflowConfig{Strategy: "reroute"}, // no queue fallback
	})
	p.Locations[0].Selector = fixedSelector{idx: 0}
	h := p.Handler()

	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://x/", nil))
	<-hitA // a full
	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://x/", nil))
	<-hitB // second req rerouted to b; both now full

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://x/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("all-full reroute = %d, want 503", rec.Code)
	}
}

// --- capacity API for custom selectors -------------------------------------

func TestBackendCapacityAPI(t *testing.T) {
	url, gate, hit := blockingBackend(t)
	defer close(gate)
	p := mustNew(t, sw.Config{
		Backends: []sw.BackendConfig{{ID: "a", URL: url, MaxConnections: ip(5)}},
	})
	b := p.Pool.Backends()[0]
	if b.MaxConns() != 5 {
		t.Errorf("MaxConns() = %d, want 5", b.MaxConns())
	}
	if b.InFlight() != 0 {
		t.Errorf("InFlight() before = %d, want 0", b.InFlight())
	}
	h := p.Handler()
	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://x/", nil))
	<-hit
	if b.InFlight() != 1 {
		t.Errorf("InFlight() during = %d, want 1", b.InFlight())
	}
}

// --- per-request timeout ----------------------------------------------------

func TestBackendRequestTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()
	p := mustNew(t, sw.Config{
		Backends: []sw.BackendConfig{{ID: "a", URL: slow.URL, Timeouts: &sw.TimeoutsConfig{Request: dp(40 * time.Millisecond)}}},
	})
	rec := serve(p, "GET", "http://x/")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("slow backend past request timeout = %d, want 502", rec.Code)
	}
}
