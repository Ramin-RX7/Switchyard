package switchyard

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveHealthMerge(t *testing.T) {
	global := &HealthConfig{
		Passive: &PassiveConfig{Count: iptr(5), Window: dptr(30 * time.Second), Statuses: []int{500}},
		Active:  &ActiveConfig{Path: "/g", Interval: dptr(5 * time.Second)},
	}
	backend := &HealthConfig{
		Passive: &PassiveConfig{Count: iptr(2)},             // override only count
		Active:  &ActiveConfig{Interval: dptr(time.Second)}, // override only interval
	}
	p := resolveHealth(global, backend)

	if !p.passive.enabled || p.passive.count != 2 { // backend wins
		t.Errorf("passive.count = %d (enabled=%v), want 2/true", p.passive.count, p.passive.enabled)
	}
	if p.passive.window != 30*time.Second { // inherited from global
		t.Errorf("passive.window = %v, want 30s (inherited)", p.passive.window)
	}
	if !p.passive.statuses[500] { // inherited from global
		t.Error("passive.statuses should contain 500 (inherited)")
	}
	if !p.active.enabled || p.active.path != "/g" { // path inherited from global
		t.Errorf("active.path = %q (enabled=%v), want /g/true", p.active.path, p.active.enabled)
	}
	if p.active.interval != time.Second { // backend wins
		t.Errorf("active.interval = %v, want 1s (backend)", p.active.interval)
	}
	if p.active.expectedStatus != defaultHealthActiveStatus { // default
		t.Errorf("active.expected_status = %d, want default %d", p.active.expectedStatus, defaultHealthActiveStatus)
	}
}

func TestResolveHealthDisabledByDefault(t *testing.T) {
	p := resolveHealth(nil, nil)
	if p.passive.enabled || p.active.enabled {
		t.Errorf("empty health config should be disabled, got %+v", p)
	}
	// A passive block with count 0 is not enabled.
	p = resolveHealth(&HealthConfig{Passive: &PassiveConfig{Count: iptr(0)}}, nil)
	if p.passive.enabled {
		t.Error("passive with count 0 should be disabled")
	}
}

// oneBackend builds a single resolved *Backend from a health config for
// white-box tests (buildBackends does the resolution).
func oneBackend(t *testing.T, url string, hc *HealthConfig) *Backend {
	t.Helper()
	bs, err := buildBackends(Config{Backends: []BackendConfig{{URL: url, Health: hc}}})
	if err != nil {
		t.Fatalf("buildBackends: %v", err)
	}
	return bs[0]
}

func TestPassiveWindowBoundary(t *testing.T) {
	hc := &HealthConfig{Passive: &PassiveConfig{
		Statuses: []int{503}, Count: iptr(2), Window: dptr(30 * time.Millisecond), Cooldown: dptr(time.Hour),
	}}

	// Two failures spaced beyond the window must NOT trip.
	spaced := oneBackend(t, "http://127.0.0.1:1", hc)
	spaced.observeOutcome(503, nil)
	time.Sleep(45 * time.Millisecond)
	spaced.observeOutcome(503, nil)
	if !spaced.Healthy() {
		t.Error("two failures spaced beyond the window should not eject")
	}

	// Two failures within the window must trip.
	burst := oneBackend(t, "http://127.0.0.1:1", hc)
	burst.observeOutcome(503, nil)
	burst.observeOutcome(503, nil)
	if burst.Healthy() {
		t.Error("two failures within the window should eject")
	}

	// A non-listed status is not a failure.
	other := oneBackend(t, "http://127.0.0.1:1", hc)
	other.observeOutcome(200, nil)
	other.observeOutcome(200, nil)
	if !other.Healthy() {
		t.Error("non-listed statuses should not count as failures")
	}
}

func TestProbeCycleRetries(t *testing.T) {
	// Fails the first attempt of the cycle, succeeds thereafter.
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	withRetry := oneBackend(t, srv.URL, &HealthConfig{Active: &ActiveConfig{Path: "/", ExpectedStatus: iptr(200), Retries: iptr(1)}})
	client := &http.Client{Timeout: time.Second, Transport: withRetry.transport}
	if !withRetry.probeCycle(context.Background(), client, withRetry.health.active) {
		t.Error("a cycle with retries=1 should pass when the retry succeeds")
	}

	// Same server keeps returning 200 now; a retries=0 backend against an
	// always-500 server should fail.
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fail.Close()
	noRetry := oneBackend(t, fail.URL, &HealthConfig{Active: &ActiveConfig{Path: "/", ExpectedStatus: iptr(200), Retries: iptr(0)}})
	if noRetry.probeCycle(context.Background(), client, noRetry.health.active) {
		t.Error("a cycle with retries=0 should fail against an always-failing target")
	}
}

func TestHealthValidation(t *testing.T) {
	bad := []Config{
		{Health: &HealthConfig{Active: &ActiveConfig{Path: ""}}},                              // active needs a path
		{Health: &HealthConfig{Passive: &PassiveConfig{Statuses: []int{99}}}},                 // bad status
		{Health: &HealthConfig{Passive: &PassiveConfig{Count: iptr(-1)}}},                     // negative count
		{Health: &HealthConfig{Active: &ActiveConfig{Path: "/x", ExpectedStatus: iptr(700)}}}, // bad expected status
		{Backends: []BackendConfig{{URL: "http://x", Health: &HealthConfig{Active: &ActiveConfig{Path: ""}}}}},
	}
	for i, c := range bad {
		if err := c.validate(); err == nil {
			t.Errorf("health config #%d should have failed validation", i)
		}
	}
	ok := Config{Health: &HealthConfig{
		Passive: &PassiveConfig{Statuses: []int{503}, Count: iptr(3), Window: dptr(time.Minute)},
		Active:  &ActiveConfig{Path: "/healthz", ExpectedStatus: iptr(200)},
	}}
	if err := ok.validate(); err != nil {
		t.Errorf("valid health config rejected: %v", err)
	}
}

func TestMarkHealthLogsOncePerTransition(t *testing.T) {
	be := oneBackend(t, "http://127.0.0.1:1", &HealthConfig{Passive: &PassiveConfig{Count: iptr(1), Window: dptr(time.Minute)}})

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	be.markHealth(false, "test")
	be.markHealth(false, "test") // no transition → no log
	be.markHealth(true, "test")

	out := buf.String()
	if got := strings.Count(out, "marked unhealthy"); got != 1 {
		t.Errorf("unhealthy logged %d times, want 1:\n%s", got, out)
	}
	if got := strings.Count(out, "marked healthy"); got != 1 {
		t.Errorf("healthy logged %d times, want 1:\n%s", got, out)
	}
}
