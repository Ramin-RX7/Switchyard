package switchyard_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

func bptr(b bool) *bool { return &b }
func riptr(i int) *int  { return &i }

// ctrlBackend is a controllable upstream: it returns a fixed status code, records
// hits, and remembers the last request body it received (for replay assertions).
type ctrlBackend struct {
	*httptest.Server
	id   string
	code int

	mu       sync.Mutex
	hits     int
	lastBody string
}

func newCtrlBackend(t *testing.T, id string, code int) *ctrlBackend {
	t.Helper()
	b := &ctrlBackend{id: id, code: code}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.hits++
		b.lastBody = string(body)
		b.mu.Unlock()
		w.WriteHeader(b.code)
		io.WriteString(w, b.id)
	}))
	t.Cleanup(b.Server.Close)
	return b
}

func (b *ctrlBackend) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hits
}

func (b *ctrlBackend) body() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastBody
}

// deadBackendURL starts a server and immediately closes it, yielding a URL that
// no longer accepts connections (produces a connection error when dialed).
func deadBackendURL(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := s.URL
	s.Close()
	return url
}

// serveBody drives one request with a body through the proxy handler.
func serveBody(p *sw.Proxy, method, target, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	p.Handler().ServeHTTP(rec, req)
	return rec
}

// --- trigger 1: connection-error retry --------------------------------------

func TestRetryOnConnectionError(t *testing.T) {
	dead := deadBackendURL(t)
	live := newCtrlBackend(t, "live", http.StatusOK)

	log := &recordingLogger{}
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "dead", URL: dead}, {ID: "live", URL: live.URL}},
		Retry:    &sw.RetryConfig{Attempts: riptr(2)},
	}
	p := mustNew(t, cfg)
	p.Logger = log

	rec := serve(p, "GET", "http://x/")
	if rec.Code != http.StatusOK || rec.Body.String() != "live" {
		t.Fatalf("got %d %q, want 200 live (rerouted off the dead backend)", rec.Code, rec.Body.String())
	}
	if live.count() != 1 {
		t.Errorf("live backend hits = %d, want 1", live.count())
	}
	if recs := log.records(); len(recs) != 1 || recs[0].Retries != 1 {
		t.Errorf("Retries = %v, want a single record with Retries=1", recs)
	}
}

// A POST is retried on a connection error (any method), with its body replayed.
func TestRetryConnectionErrorReplaysPostBody(t *testing.T) {
	dead := deadBackendURL(t)
	live := newCtrlBackend(t, "live", http.StatusOK)
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "dead", URL: dead}, {ID: "live", URL: live.URL}},
		Retry:    &sw.RetryConfig{Attempts: riptr(2)},
	}
	p := mustNew(t, cfg)

	rec := serveBody(p, "POST", "http://x/", "payload")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if live.body() != "payload" {
		t.Errorf("replayed body = %q, want %q", live.body(), "payload")
	}
}

// --- trigger 2: status-based retry ------------------------------------------

func TestRetryOnStatus(t *testing.T) {
	bad := newCtrlBackend(t, "bad", http.StatusServiceUnavailable)
	good := newCtrlBackend(t, "good", http.StatusOK)
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "bad", URL: bad.URL}, {ID: "good", URL: good.URL}},
		Retry:    &sw.RetryConfig{Attempts: riptr(1), OnStatus: []int{503}},
	}
	p := mustNew(t, cfg)

	rec := serve(p, "GET", "http://x/")
	if rec.Code != http.StatusOK || rec.Body.String() != "good" {
		t.Fatalf("got %d %q, want 200 good (rerouted off the 503)", rec.Code, rec.Body.String())
	}
	if bad.count() != 1 || good.count() != 1 {
		t.Errorf("hits bad=%d good=%d, want 1/1", bad.count(), good.count())
	}
}

// A status not in on_status is passed through untouched (no retry).
func TestNoRetryOnUnlistedStatus(t *testing.T) {
	a := newCtrlBackend(t, "a", http.StatusNotFound)
	b := newCtrlBackend(t, "b", http.StatusOK)
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "a", URL: a.URL}, {ID: "b", URL: b.URL}},
		Retry:    &sw.RetryConfig{Attempts: riptr(2), OnStatus: []int{503}},
	}
	p := mustNew(t, cfg)

	rec := serve(p, "GET", "http://x/")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (passed through)", rec.Code)
	}
	if a.count() != 1 || b.count() != 0 {
		t.Errorf("hits a=%d b=%d, want 1/0 (no retry)", a.count(), b.count())
	}
}

// POST is not status-retried by default (idempotent-only), but is with the opt-in.
func TestStatusRetryIdempotencyGate(t *testing.T) {
	newProxy := func(nonIdem bool) (*sw.Proxy, *ctrlBackend, *ctrlBackend) {
		bad := newCtrlBackend(t, "bad", http.StatusServiceUnavailable)
		good := newCtrlBackend(t, "good", http.StatusOK)
		cfg := sw.Config{
			Backends: []sw.BackendConfig{{ID: "bad", URL: bad.URL}, {ID: "good", URL: good.URL}},
			Retry:    &sw.RetryConfig{Attempts: riptr(1), OnStatus: []int{503}, RetryNonIdempotent: bptr(nonIdem)},
		}
		return mustNew(t, cfg), bad, good
	}

	// Default: POST not retried on status → client sees the 503.
	p, _, good := newProxy(false)
	if rec := serveBody(p, "POST", "http://x/", "x"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("default POST status = %d, want 503 (not retried)", rec.Code)
	}
	if good.count() != 0 {
		t.Errorf("good hit %d times, want 0 (POST not retried by default)", good.count())
	}

	// Opt-in: POST retried on status → reaches the good backend.
	p, _, good = newProxy(true)
	if rec := serveBody(p, "POST", "http://x/", "x"); rec.Code != http.StatusOK {
		t.Errorf("opt-in POST status = %d, want 200 (retried)", rec.Code)
	}
	if good.count() != 1 {
		t.Errorf("good hit %d times, want 1 (POST retried with opt-in)", good.count())
	}
}

// --- exhaustion --------------------------------------------------------------

func TestStatusExhaustionPassesThroughLastResponse(t *testing.T) {
	a := newCtrlBackend(t, "a", http.StatusServiceUnavailable)
	b := newCtrlBackend(t, "b", http.StatusServiceUnavailable)
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "a", URL: a.URL}, {ID: "b", URL: b.URL}},
		Retry:    &sw.RetryConfig{Attempts: riptr(2), OnStatus: []int{503}},
	}
	p := mustNew(t, cfg)

	rec := serve(p, "GET", "http://x/")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the real upstream 503 passed through", rec.Code)
	}
	if got := a.count() + b.count(); got != 3 {
		t.Errorf("total upstream hits = %d, want 3 (1 + 2 retries)", got)
	}
}

func TestStatusExhaustionCustomResponse(t *testing.T) {
	a := newCtrlBackend(t, "a", http.StatusServiceUnavailable)
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "a", URL: a.URL}},
		Retry: &sw.RetryConfig{
			Attempts: riptr(1), OnStatus: []int{503},
			Response: &sw.ResponseConfig{Status: riptr(599), Body: "exhausted"},
		},
	}
	p := mustNew(t, cfg)

	rec := serve(p, "GET", "http://x/")
	if rec.Code != 599 || rec.Body.String() != "exhausted" {
		t.Fatalf("got %d %q, want 599 exhausted (custom response)", rec.Code, rec.Body.String())
	}
}

func TestConnectionExhaustionIsBadGateway(t *testing.T) {
	d1, d2 := deadBackendURL(t), deadBackendURL(t)
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "d1", URL: d1}, {ID: "d2", URL: d2}},
		Retry:    &sw.RetryConfig{Attempts: riptr(2)},
	}
	p := mustNew(t, cfg)

	rec := serve(p, "GET", "http://x/")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (all backends down)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "backend unavailable") {
		t.Errorf("body = %q, want the backend_error response", rec.Body.String())
	}
}

// --- trigger 3: unhealthy skip ----------------------------------------------

func TestRetrySkipsUnhealthyBackend(t *testing.T) {
	a := newCtrlBackend(t, "a", http.StatusOK)
	b := newCtrlBackend(t, "b", http.StatusOK)
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "a", URL: a.URL}, {ID: "b", URL: b.URL}},
		Retry:    &sw.RetryConfig{Attempts: riptr(1)},
	}
	p := mustNew(t, cfg)
	// Flag the first backend unhealthy; it must be excluded from selection.
	p.Pool.Backends()[0].SetHealthy(false)

	rec := serve(p, "GET", "http://x/")
	if rec.Code != http.StatusOK || rec.Body.String() != "b" {
		t.Fatalf("got %d %q, want 200 b (unhealthy a skipped)", rec.Code, rec.Body.String())
	}
	if a.count() != 0 {
		t.Errorf("unhealthy backend was hit %d times, want 0", a.count())
	}
}

func TestAllUnhealthyFallsBack(t *testing.T) {
	a := newCtrlBackend(t, "a", http.StatusOK)
	b := newCtrlBackend(t, "b", http.StatusOK)
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "a", URL: a.URL}, {ID: "b", URL: b.URL}},
		Retry:    &sw.RetryConfig{Attempts: riptr(1)},
	}
	p := mustNew(t, cfg)
	for _, be := range p.Pool.Backends() {
		be.SetHealthy(false)
	}

	rec := serve(p, "GET", "http://x/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (best-effort fallback when all unhealthy)", rec.Code)
	}
}

// --- reselection semantics ---------------------------------------------------

func TestReselectionSameBackendDefault(t *testing.T) {
	a := newCtrlBackend(t, "a", http.StatusServiceUnavailable)
	cfg := sw.Config{ // single backend: default retry_same_backend reuses it
		Backends: []sw.BackendConfig{{ID: "a", URL: a.URL}},
		Retry:    &sw.RetryConfig{Attempts: riptr(2), OnStatus: []int{503}},
	}
	p := mustNew(t, cfg)

	rec := serve(p, "GET", "http://x/")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if a.count() != 3 {
		t.Errorf("hits = %d, want 3 (same backend retried: 1 + 2)", a.count())
	}
}

func TestReselectionDistinctBackendsOnly(t *testing.T) {
	a := newCtrlBackend(t, "a", http.StatusServiceUnavailable)
	cfg := sw.Config{ // single backend + retry_same_backend:false → no distinct candidate
		Backends: []sw.BackendConfig{{ID: "a", URL: a.URL}},
		Retry:    &sw.RetryConfig{Attempts: riptr(2), OnStatus: []int{503}, RetrySameBackend: bptr(false)},
	}
	p := mustNew(t, cfg)

	rec := serve(p, "GET", "http://x/")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if a.count() != 1 {
		t.Errorf("hits = %d, want 1 (no distinct backend to retry on)", a.count())
	}
}

// --- streaming safety --------------------------------------------------------

// A large 200 body streams through intact even with retry enabled.
func TestRetryEnabledStreamingUnaffected(t *testing.T) {
	big := strings.Repeat("x", 1<<16)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, big)
	}))
	t.Cleanup(up.Close)

	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "up", URL: up.URL}},
		Retry:    &sw.RetryConfig{Attempts: riptr(2), OnStatus: []int{503}},
	}
	p := mustNew(t, cfg)

	rec := serve(p, "GET", "http://x/")
	if rec.Code != http.StatusOK || rec.Body.Len() != len(big) {
		t.Fatalf("got %d, %d bytes; want 200, %d bytes", rec.Code, rec.Body.Len(), len(big))
	}
}

// --- SDK override ------------------------------------------------------------

// A custom Actor fully replaces the retry loop (retry is additive default behavior).
func TestCustomActorBypassesRetry(t *testing.T) {
	a := newCtrlBackend(t, "a", http.StatusServiceUnavailable)
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "a", URL: a.URL}},
		Retry:    &sw.RetryConfig{Attempts: riptr(3), OnStatus: []int{503}},
	}
	p := mustNew(t, cfg)
	p.Actor = actorFunc(func(w http.ResponseWriter, _ *http.Request, _ sw.Request, _ sw.Decision) {
		w.WriteHeader(http.StatusNoContent)
	})

	rec := serve(p, "GET", "http://x/")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (custom actor)", rec.Code)
	}
	if a.count() != 0 {
		t.Errorf("backend hit %d times, want 0 (custom actor never forwards)", a.count())
	}
}

// actorFunc adapts a function to the Actor interface.
type actorFunc func(http.ResponseWriter, *http.Request, sw.Request, sw.Decision)

func (f actorFunc) Act(w http.ResponseWriter, r *http.Request, req sw.Request, d sw.Decision) {
	f(w, r, req, d)
}
