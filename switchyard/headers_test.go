package switchyard_test

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// --- axis 1 & 2: default set_headers reach the backend ---------------------

func TestSetHeadersReachBackend(t *testing.T) {
	a := newEchoBackend(t, "api1")
	cfg := sw.Config{
		Backends:   []sw.BackendConfig{{ID: "api1", URL: a.URL}},
		SetHeaders: map[string]string{"X-Real-IP": "$remote_addr", "X-Forwarded-Proto": "$scheme"},
	}
	p := mustNew(t, cfg)

	serve(p, "GET", "http://x/") // httptest default RemoteAddr is 192.0.2.1:1234, scheme http
	if got := a.header("X-Real-IP"); got != "192.0.2.1" {
		t.Errorf("backend saw X-Real-IP = %q, want 192.0.2.1", got)
	}
	if got := a.header("X-Forwarded-Proto"); got != "http" {
		t.Errorf("backend saw X-Forwarded-Proto = %q, want http", got)
	}
}

// Global and location headers stack: location wins on a shared key, other
// globals are retained.
func TestSetHeadersStacking(t *testing.T) {
	a := newEchoBackend(t, "api1")
	cfg := sw.Config{
		Backends:   []sw.BackendConfig{{ID: "api1", URL: a.URL}},
		SetHeaders: map[string]string{"X-Src": "global", "X-Keep": "g"},
		Locations: []sw.LocationConfig{
			{Path: "/api/", Backends: []string{"api1"}, SetHeaders: map[string]string{"X-Src": "loc", "X-Route": "api"}},
		},
	}
	p := mustNew(t, cfg)

	serve(p, "GET", "http://x/api/x")
	if got := a.header("X-Src"); got != "loc" {
		t.Errorf("X-Src = %q, want loc (location wins)", got)
	}
	if got := a.header("X-Keep"); got != "g" {
		t.Errorf("X-Keep = %q, want g (global retained)", got)
	}
	if got := a.header("X-Route"); got != "api" {
		t.Errorf("X-Route = %q, want api (location-only)", got)
	}
}

// --- axis 3: a user-supplied HeaderApplier is honored ----------------------

// withRequestID mirrors examples/request-id: embed the config default, keep its
// headers, add X-Request-Id.
type withRequestID struct {
	*sw.TemplateHeaderSetter
	n atomic.Uint64
}

func (h *withRequestID) Apply(req sw.Request, r *http.Request) {
	if h.TemplateHeaderSetter != nil {
		h.TemplateHeaderSetter.Apply(req, r)
	}
	r.Header.Set("X-Request-Id", strconv.FormatUint(h.n.Add(1), 10))
}

func TestCustomHeaderApplierHonoredAndKeepsConfigHeaders(t *testing.T) {
	a := newEchoBackend(t, "api1")
	cfg := sw.Config{
		Backends:   []sw.BackendConfig{{ID: "api1", URL: a.URL}},
		SetHeaders: map[string]string{"X-Real-IP": "$remote_addr"},
	}
	p := mustNew(t, cfg)

	base, _ := p.Headers.(*sw.TemplateHeaderSetter) // the config-built default
	p.Headers = &withRequestID{TemplateHeaderSetter: base}

	serve(p, "GET", "http://x/")
	if got := a.header("X-Request-Id"); got == "" {
		t.Error("backend saw no X-Request-Id from custom HeaderApplier")
	}
	if got := a.header("X-Real-IP"); got != "192.0.2.1" {
		t.Errorf("config header lost: X-Real-IP = %q, want 192.0.2.1", got)
	}
}

// --- validation: unknown variable fails at New -----------------------------

func TestSetHeadersUnknownVarRejectedAtNew(t *testing.T) {
	cfg := sw.Config{SetHeaders: map[string]string{"X-Bad": "$not_a_variable"}}
	if _, err := sw.New(cfg); err == nil {
		t.Error("New with unknown $variable in set_headers = nil error, want failure")
	}
}
