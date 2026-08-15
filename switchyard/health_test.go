package switchyard_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// hdur builds a *sw.Duration for tests (the JSON form is integer seconds, but Go
// construction keeps sub-second precision — handy for fast tests).
func hdur(d time.Duration) *sw.Duration { x := sw.Duration(d); return &x }

// eventually polls cond until it is true or the timeout elapses.
func eventually(t *testing.T, cond func() bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s: %s", timeout, msg)
}

// flipServer is an upstream whose response status can be changed at runtime.
type flipServer struct {
	*httptest.Server
	code atomic.Int64
}

func newFlipServer(t *testing.T, initial int) *flipServer {
	t.Helper()
	s := &flipServer{}
	s.code.Store(int64(initial))
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(s.code.Load()))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *flipServer) set(code int) { s.code.Store(int64(code)) }

// --- passive detector --------------------------------------------------------

func TestPassiveStatusWindow(t *testing.T) {
	bad := newCtrlBackend(t, "bad", http.StatusServiceUnavailable)
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "bad", URL: bad.URL}},
		Health: &sw.HealthConfig{Passive: &sw.PassiveConfig{
			Statuses: []int{503}, Count: riptr(2), Window: hdur(time.Hour), Cooldown: hdur(time.Hour),
		}},
	}
	p := mustNew(t, cfg)
	be := p.Pool.Backends()[0]

	serve(p, "GET", "http://x/")
	if !be.Healthy() {
		t.Fatal("backend should still be healthy after 1 failure (count=2)")
	}
	serve(p, "GET", "http://x/")
	if be.Healthy() {
		t.Fatal("backend should be unhealthy after 2 failures within the window")
	}
}

func TestPassiveConnectionErrorsCount(t *testing.T) {
	dead := deadBackendURL(t)
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "dead", URL: dead}},
		Health:   &sw.HealthConfig{Passive: &sw.PassiveConfig{Count: riptr(2), Window: hdur(time.Hour), Cooldown: hdur(time.Hour)}},
	}
	p := mustNew(t, cfg)
	be := p.Pool.Backends()[0]

	serve(p, "GET", "http://x/")
	serve(p, "GET", "http://x/")
	if be.Healthy() {
		t.Fatal("backend should be unhealthy after 2 connection errors")
	}
}

func TestPassiveCooldownRecovery(t *testing.T) {
	bad := newCtrlBackend(t, "bad", http.StatusServiceUnavailable)
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "bad", URL: bad.URL}},
		Health: &sw.HealthConfig{Passive: &sw.PassiveConfig{
			Statuses: []int{503}, Count: riptr(1), Window: hdur(time.Hour), Cooldown: hdur(150 * time.Millisecond),
		}},
	}
	p := mustNew(t, cfg)
	be := p.Pool.Backends()[0]

	serve(p, "GET", "http://x/")
	if be.Healthy() {
		t.Fatal("backend should be unhealthy after 1 failure (count=1)")
	}
	eventually(t, be.Healthy, time.Second, "backend should recover after the cooldown")
}

// Only the backend with a health config is subject to ejection.
func TestPassiveIsPerBackend(t *testing.T) {
	guarded := newCtrlBackend(t, "guarded", http.StatusServiceUnavailable)
	plain := newCtrlBackend(t, "plain", http.StatusServiceUnavailable)
	cfg := sw.Config{
		Backends: []sw.BackendConfig{
			{ID: "guarded", URL: guarded.URL, Health: &sw.HealthConfig{Passive: &sw.PassiveConfig{
				Statuses: []int{503}, Count: riptr(1), Window: hdur(time.Hour), Cooldown: hdur(time.Hour),
			}}},
			{ID: "plain", URL: plain.URL},
		},
	}
	p := mustNew(t, cfg)
	bes := p.Pool.Backends()

	serve(p, "GET", "http://x/")
	serve(p, "GET", "http://x/")
	if bes[0].Healthy() {
		t.Error("guarded backend should be unhealthy")
	}
	if !bes[1].Healthy() {
		t.Error("plain backend (no health config) should stay healthy")
	}
}

// --- active detector ---------------------------------------------------------

func TestActiveEjectAndRecover(t *testing.T) {
	target := newFlipServer(t, http.StatusInternalServerError) // starts failing the probe
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "t", URL: target.URL, Health: &sw.HealthConfig{Active: &sw.ActiveConfig{
			Path: "/", Interval: hdur(20 * time.Millisecond), Timeout: hdur(time.Second),
			ExpectedStatus: riptr(200), UnhealthyThreshold: riptr(2), HealthyThreshold: riptr(2),
		}}}},
	}
	p := mustNew(t, cfg)
	be := p.Pool.Backends()[0]

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartHealthChecks(ctx)

	eventually(t, func() bool { return !be.Healthy() }, 2*time.Second, "active probe should eject a failing backend")
	target.set(http.StatusOK)
	eventually(t, be.Healthy, 2*time.Second, "active probe should recover a passing backend")
}

// With both detectors, a passive eject is recovered by the prober, not the
// cooldown (which is set absurdly high here to prove it is not used).
func TestActiveOwnsRecoveryWhenBoth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK) // probe always healthy
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable) // real traffic fails → passive ejects
	}))
	t.Cleanup(srv.Close)

	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "b", URL: srv.URL, Health: &sw.HealthConfig{
			Passive: &sw.PassiveConfig{Statuses: []int{503}, Count: riptr(1), Window: hdur(time.Hour), Cooldown: hdur(time.Hour)},
			Active:  &sw.ActiveConfig{Path: "/healthz", Interval: hdur(20 * time.Millisecond), Timeout: hdur(time.Second), ExpectedStatus: riptr(200), HealthyThreshold: riptr(1)},
		}}},
	}
	p := mustNew(t, cfg)
	be := p.Pool.Backends()[0]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartHealthChecks(ctx)

	serve(p, "GET", "http://x/") // one 503 → passive eject
	if be.Healthy() {
		t.Fatal("backend should be unhealthy after the passive trip")
	}
	// Cooldown is 1h, so recovery here can only come from the active prober.
	eventually(t, be.Healthy, 2*time.Second, "active prober should recover the backend despite the long cooldown")
}
