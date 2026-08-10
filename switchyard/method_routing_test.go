package switchyard_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// --- axis 1: real flow — method picks the backend --------------------------

func TestMethodRoutingSelectsByMethod(t *testing.T) {
	a := newEchoBackend(t, "api1")
	b := newEchoBackend(t, "api2")
	p := mustNew(t, sw.Config{
		Backends: []sw.BackendConfig{
			{ID: "api1", URL: a.URL, Methods: []string{"GET", "HEAD"}},
			{ID: "api2", URL: b.URL, Methods: []string{"POST", "PUT"}},
		},
		Locations: []sw.LocationConfig{{Path: "/api/", Backends: []string{"api1", "api2"}}},
	})
	if got := serve(p, "GET", "http://x/api/x").Body.String(); got != "api1" {
		t.Errorf("GET body = %q, want api1", got)
	}
	if got := serve(p, "POST", "http://x/api/x").Body.String(); got != "api2" {
		t.Errorf("POST body = %q, want api2", got)
	}
	if a.count() != 1 || b.count() != 1 {
		t.Errorf("hits: api1=%d api2=%d, want 1/1", a.count(), b.count())
	}
}

// A single method round-robins cleanly across just its accepting backends.
func TestMethodRoutingSingleMethodRoundRobin(t *testing.T) {
	a := newEchoBackend(t, "g1")
	b := newEchoBackend(t, "g2")
	c := newEchoBackend(t, "post")
	p := mustNew(t, sw.Config{
		Backends: []sw.BackendConfig{
			{ID: "g1", URL: a.URL, Methods: []string{"GET"}},
			{ID: "g2", URL: b.URL, Methods: []string{"GET"}},
			{ID: "post", URL: c.URL, Methods: []string{"POST"}},
		},
		Locations: []sw.LocationConfig{{Path: "/", Backends: []string{"g1", "g2", "post"}}},
	})
	var got []string
	for i := 0; i < 4; i++ {
		got = append(got, serve(p, "GET", "http://x/x").Body.String())
	}
	if got[0] != "g1" || got[1] != "g2" || got[2] != "g1" || got[3] != "g2" {
		t.Errorf("GET rotation = %v, want [g1 g2 g1 g2]", got)
	}
	if c.count() != 0 {
		t.Error("POST-only backend received GET traffic")
	}
}

// --- axis 2: defaults — no methods means accept-all; 405 default response ---

func TestMethodRoutingNoMethodsAcceptsAll(t *testing.T) {
	a := newEchoBackend(t, "any")
	p := mustNew(t, sw.Config{
		Backends:  []sw.BackendConfig{{ID: "any", URL: a.URL}}, // no methods → all
		Locations: []sw.LocationConfig{{Path: "/", Backends: []string{"any"}}},
	})
	for _, m := range []string{"GET", "POST", "DELETE", "PATCH"} {
		if rec := serve(p, m, "http://x/thing"); rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200 (accept-all backend)", m, rec.Code)
		}
	}
	if a.count() != 4 {
		t.Errorf("hits = %d, want 4", a.count())
	}
}

func TestMethodRoutingUnsupported405(t *testing.T) {
	a := newEchoBackend(t, "api1")
	b := newEchoBackend(t, "api2")
	p := mustNew(t, sw.Config{
		Backends: []sw.BackendConfig{
			{ID: "api1", URL: a.URL, Methods: []string{"GET", "HEAD"}},
			{ID: "api2", URL: b.URL, Methods: []string{"POST", "PUT"}},
		},
		Locations: []sw.LocationConfig{{Path: "/api/", Backends: []string{"api1", "api2"}}},
	})
	rec := serve(p, "DELETE", "http://x/api/x")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD, POST, PUT" {
		t.Errorf("Allow = %q, want the sorted method union", allow)
	}
	if !strings.Contains(rec.Body.String(), "method not allowed") {
		t.Errorf("body = %q, want default 405 body", rec.Body.String())
	}
	if a.count() != 0 || b.count() != 0 {
		t.Error("no backend should be hit on a 405")
	}
}

// --- axis 3: SDK-overridden 405 responder is honored (Allow still set) ------

func TestMethodRoutingCustom405Honored(t *testing.T) {
	a := newEchoBackend(t, "api1")
	p := mustNew(t, sw.Config{
		Backends:  []sw.BackendConfig{{ID: "api1", URL: a.URL, Methods: []string{"GET"}}},
		Locations: []sw.LocationConfig{{Path: "/", Backends: []string{"api1"}}},
	})
	p.MethodNotAllowed = sentinelResponder{} // defined in response_test.go (same package)
	rec := serve(p, "POST", "http://x/x")
	if rec.Code != http.StatusTeapot || rec.Body.String() != "brewed" {
		t.Errorf("got %d %q, want 418 brewed", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Allow") != "GET" {
		t.Errorf("Allow = %q, want GET (set before the custom responder runs)", rec.Header().Get("Allow"))
	}
}

// --- reroute must stay within method-eligible backends ----------------------

func TestMethodRoutingRerouteRespectsMethods(t *testing.T) {
	gURL, gate, hit := blockingBackend(t)
	defer close(gate)
	pb := newEchoBackend(t, "postonly")
	p := mustNew(t, sw.Config{
		Backends: []sw.BackendConfig{
			{ID: "getonly", URL: gURL, Methods: []string{"GET"}, MaxConnections: ip(1)},
			{ID: "postonly", URL: pb.URL, Methods: []string{"POST"}},
		},
		Locations: []sw.LocationConfig{{Path: "/", Backends: []string{"getonly", "postonly"}}},
		Overflow:  &sw.OverflowConfig{Strategy: "reroute"}, // no queue fallback
	})
	h := p.Handler()

	// Occupy getonly's single slot with a parked GET.
	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://x/", nil))
	<-hit

	// A second GET has only getonly as a candidate (postonly rejects GET). It is
	// full, and reroute must NOT leak the GET to the POST-only backend.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://x/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET with its only backend full = %d, want 503 (no cross-method reroute)", rec.Code)
	}
	if pb.count() != 0 {
		t.Error("reroute leaked a GET to the POST-only backend")
	}
}

// --- fail-fast: an empty method entry is rejected ---------------------------

func TestBackendEmptyMethodRejected(t *testing.T) {
	a := newEchoBackend(t, "a")
	if _, err := sw.New(sw.Config{Backends: []sw.BackendConfig{{ID: "a", URL: a.URL, Methods: []string{""}}}}); err == nil {
		t.Error("an empty method entry should fail New")
	}
}

// --- Backend.Accepts (normalization + accept-all) ---------------------------

func TestBackendAccepts(t *testing.T) {
	a := newEchoBackend(t, "a")
	b := newEchoBackend(t, "b")
	p := mustNew(t, sw.Config{Backends: []sw.BackendConfig{
		{ID: "a", URL: a.URL, Methods: []string{"get", "Post"}}, // mixed case → normalized
		{ID: "b", URL: b.URL}, // no methods → accept all
	}})
	byID := map[string]*sw.Backend{}
	for _, bk := range p.Pool.Backends() {
		byID[bk.ID] = bk
	}
	if !byID["a"].Accepts("GET") || !byID["a"].Accepts("get") || !byID["a"].Accepts("POST") {
		t.Error("backend a should accept GET/POST case-insensitively")
	}
	if byID["a"].Accepts("DELETE") {
		t.Error("backend a should reject DELETE")
	}
	if !byID["b"].Accepts("ANYTHING") {
		t.Error("backend b (no methods) should accept all")
	}
}
