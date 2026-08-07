package switchyard_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// --- construction / validation (fail-fast) ---------------------------------

func TestNewValidationErrors(t *testing.T) {
	dir := t.TempDir()
	const okURL = "http://127.0.0.1:1" // valid scheme+host; New does not dial
	okBackend := []sw.BackendConfig{{ID: "api1", URL: okURL}}

	tests := []struct {
		name string
		cfg  sw.Config
	}{
		{"backend missing url", sw.Config{Backends: []sw.BackendConfig{{ID: "a"}}}},
		{"backend invalid url", sw.Config{Backends: []sw.BackendConfig{{ID: "a", URL: "not-a-url"}}}},
		{"duplicate url", sw.Config{Backends: []sw.BackendConfig{{ID: "a", URL: okURL}, {ID: "b", URL: okURL}}}},
		{"duplicate id", sw.Config{Backends: []sw.BackendConfig{{ID: "x", URL: "http://127.0.0.1:1"}, {ID: "x", URL: "http://127.0.0.1:2"}}}},
		{"empty path", sw.Config{Backends: okBackend, Locations: []sw.LocationConfig{{Path: "", Backends: []string{"api1"}}}}},
		{"unknown backend id", sw.Config{Backends: okBackend, Locations: []sw.LocationConfig{{Path: "/x/", Backends: []string{"nope"}}}}},
		{"proxy without backends", sw.Config{Backends: okBackend, Locations: []sw.LocationConfig{{Path: "/x/"}}}},
		{"static without root", sw.Config{Locations: []sw.LocationConfig{{Path: "/s/", Type: "static"}}}},
		{"static root missing", sw.Config{Locations: []sw.LocationConfig{{Path: "/s/", Type: "static", Root: "/no/such/dir/xyz"}}}},
		{"root on proxy", sw.Config{Backends: okBackend, Locations: []sw.LocationConfig{{Path: "/x/", Backends: []string{"api1"}, Root: dir}}}},
		{"backends on static", sw.Config{Backends: okBackend, Locations: []sw.LocationConfig{{Path: "/s/", Type: "static", Root: dir, Backends: []string{"api1"}}}}},
		{"unknown type", sw.Config{Backends: okBackend, Locations: []sw.LocationConfig{{Path: "/x/", Type: "weird", Backends: []string{"api1"}}}}},
		{"bad regex", sw.Config{Backends: okBackend, Locations: []sw.LocationConfig{{Path: "([", Regex: true, Backends: []string{"api1"}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := sw.New(tt.cfg); err == nil {
				t.Errorf("New(%s) = nil error, want a validation failure", tt.name)
			}
		})
	}
}

func TestNewSucceedsAndHandlerNonNil(t *testing.T) {
	a := newEchoBackend(t, "api1")
	p := mustNew(t, sw.Config{Backends: []sw.BackendConfig{{ID: "api1", URL: a.URL}}})
	if p.Handler() == nil {
		t.Error("Handler() = nil")
	}
}

// A dead backend yields a consistent 502 via the per-backend ErrorHandler.
func TestBackendFailureIs502(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := down.URL
	down.Close() // now nothing is listening

	p := mustNew(t, sw.Config{Backends: []sw.BackendConfig{{ID: "dead", URL: url}}})
	rec := serve(p, "GET", "http://x/")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "backend unavailable") {
		t.Errorf("body = %q, want it to mention 'backend unavailable'", rec.Body.String())
	}
}

// --- end-to-end flow across features ---------------------------------------

func TestEndToEndFlow(t *testing.T) {
	a := newEchoBackend(t, "api1")
	b := newEchoBackend(t, "api2")
	fe := newEchoBackend(t, "frontend")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "logo.txt"), []byte("PNGDATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := sw.Config{
		Backends: []sw.BackendConfig{
			{ID: "api1", URL: a.URL}, {ID: "api2", URL: b.URL}, {ID: "frontend", URL: fe.URL},
		},
		SetHeaders: map[string]string{"X-Real-IP": "$remote_addr"},
		Locations: []sw.LocationConfig{
			{Path: "/api/", Backends: []string{"api1", "api2"}},
			{Path: "/media/", Type: "static", Root: dir},
			{Path: "/", Backends: []string{"frontend"}},
		},
	}
	p := mustNew(t, cfg)

	// Round-robin across both api backends + header injection + X-Forwarded-For.
	for i := 0; i < 4; i++ {
		if rec := serve(p, "GET", "http://x/api/users"); rec.Code != http.StatusOK {
			t.Fatalf("/api/ status = %d, want 200", rec.Code)
		}
	}
	if a.count() == 0 || b.count() == 0 {
		t.Errorf("round-robin didn't hit both backends: api1=%d api2=%d", a.count(), b.count())
	}
	if a.header("X-Real-IP") == "" {
		t.Error("backend did not receive injected X-Real-IP")
	}
	if a.header("X-Forwarded-For") == "" {
		t.Error("backend did not receive X-Forwarded-For (maintained by the reverse proxy)")
	}

	// Static file.
	if rec := serve(p, "GET", "http://x/media/logo.txt"); rec.Code != http.StatusOK || rec.Body.String() != "PNGDATA" {
		t.Errorf("/media/ = %d %q, want 200 PNGDATA", rec.Code, rec.Body.String())
	}

	// Catch-all → frontend.
	if rec := serve(p, "GET", "http://x/anything"); rec.Body.String() != "frontend" {
		t.Errorf("/ = %q, want frontend", rec.Body.String())
	}
}

func TestNoMatchingLocationReturns404(t *testing.T) {
	a := newEchoBackend(t, "api1")
	cfg := sw.Config{ // no catch-all
		Backends:  []sw.BackendConfig{{ID: "api1", URL: a.URL}},
		Locations: []sw.LocationConfig{{Path: "/api/", Backends: []string{"api1"}}},
	}
	p := mustNew(t, cfg)
	if rec := serve(p, "GET", "http://x/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
