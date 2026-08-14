package switchyard_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// --- axis 1: real flow — response headers on proxied / static / generated ---

func TestResponseHeadersOnProxiedResponse(t *testing.T) {
	a := newEchoBackend(t, "a")
	p := mustNew(t, sw.Config{
		Backends:           []sw.BackendConfig{{ID: "a", URL: a.URL}},
		SetResponseHeaders: map[string]string{"X-Served-By": "switchyard", "X-Scheme": "$scheme"},
		Locations:          []sw.LocationConfig{{Path: "/", Backends: []string{"a"}}},
	})
	rec := serve(p, "GET", "http://x/thing")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Served-By"); got != "switchyard" {
		t.Errorf("X-Served-By = %q, want switchyard", got)
	}
	if got := rec.Header().Get("X-Scheme"); got != "http" {
		t.Errorf("X-Scheme = %q, want http ($scheme rendered)", got)
	}
	if rec.Body.String() != "a" {
		t.Errorf("body = %q, want the backend body a", rec.Body.String())
	}
}

func TestResponseHeadersOnStaticResponse(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := mustNew(t, sw.Config{
		SetResponseHeaders: map[string]string{"Cache-Control": "no-store"},
		Locations:          []sw.LocationConfig{{Path: "/", Type: "static", Root: dir}},
	})
	rec := serve(p, "GET", "http://x/hello.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if rec.Body.String() != "hi" {
		t.Errorf("body = %q, want hi", rec.Body.String())
	}
}

func TestResponseHeadersOnGeneratedResponse(t *testing.T) {
	p := mustNew(t, sw.Config{
		SetResponseHeaders: map[string]string{"X-Served-By": "switchyard"},
		Locations: []sw.LocationConfig{{
			Path: "/health", Type: "response",
			Response: &sw.ResponseConfig{Status: ip(200), Body: "ok"},
		}},
	})
	rec := serve(p, "GET", "http://x/health")
	if rec.Code != 200 || rec.Body.String() != "ok" {
		t.Fatalf("got %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Served-By"); got != "switchyard" {
		t.Errorf("X-Served-By = %q, want switchyard", got)
	}
}

// --- global + location stacking (location wins on a shared key) -------------

func TestResponseHeadersStacking(t *testing.T) {
	a := newEchoBackend(t, "a")
	p := mustNew(t, sw.Config{
		Backends:           []sw.BackendConfig{{ID: "a", URL: a.URL}},
		SetResponseHeaders: map[string]string{"X-Served-By": "global", "X-Global": "g"},
		Locations: []sw.LocationConfig{{
			Path: "/", Backends: []string{"a"},
			SetResponseHeaders: map[string]string{"X-Served-By": "loc"},
		}},
	})
	rec := serve(p, "GET", "http://x/thing")
	if got := rec.Header().Get("X-Served-By"); got != "loc" {
		t.Errorf("X-Served-By = %q, want loc (location wins)", got)
	}
	if got := rec.Header().Get("X-Global"); got != "g" {
		t.Errorf("X-Global = %q, want g (global retained)", got)
	}
}

// --- axis 3: SDK custom ResponseHeaderApplier is honored --------------------

type stampResponder struct{}

func (stampResponder) Apply(req sw.Request, h http.Header) { h.Set("X-Custom", "yes-"+req.Method) }

func TestCustomResponseHeaderApplierHonored(t *testing.T) {
	a := newEchoBackend(t, "a")
	p := mustNew(t, sw.Config{
		Backends:  []sw.BackendConfig{{ID: "a", URL: a.URL}},
		Locations: []sw.LocationConfig{{Path: "/", Backends: []string{"a"}}},
	})
	p.ResponseHeaders = stampResponder{} // override after New
	rec := serve(p, "GET", "http://x/thing")
	if got := rec.Header().Get("X-Custom"); got != "yes-GET" {
		t.Errorf("X-Custom = %q, want yes-GET", got)
	}
}

// --- streaming still works: the wrapper forwards Flush ----------------------

type flushActor struct{}

func (flushActor) Act(w http.ResponseWriter, _ *http.Request, _ sw.Request, _ sw.Decision) {
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	_, _ = w.Write([]byte("streamed"))
}

func TestResponseHeaderWriterForwardsFlush(t *testing.T) {
	a := newEchoBackend(t, "a")
	p := mustNew(t, sw.Config{
		Backends:           []sw.BackendConfig{{ID: "a", URL: a.URL}},
		SetResponseHeaders: map[string]string{"X-Served-By": "switchyard"}, // forces the wrapper
		Locations:          []sw.LocationConfig{{Path: "/", Backends: []string{"a"}}},
	})
	p.Actor = flushActor{}
	rec := serve(p, "GET", "http://x/thing")
	if !rec.Flushed {
		t.Error("Flush did not reach the underlying writer (streaming would break)")
	}
	if got := rec.Header().Get("X-Served-By"); got != "switchyard" {
		t.Errorf("X-Served-By = %q, want switchyard (injected before flush)", got)
	}
}

// --- fail-fast: an unknown variable in a response header errors at New ------

func TestResponseHeadersFailFast(t *testing.T) {
	if _, err := sw.New(sw.Config{SetResponseHeaders: map[string]string{"X-Bad": "$nope"}}); err == nil {
		t.Error("unknown variable in set_response_headers should fail New")
	}
	if _, err := sw.New(sw.Config{
		Locations: []sw.LocationConfig{{Path: "/", Type: "response",
			Response:           &sw.ResponseConfig{Body: "ok"},
			SetResponseHeaders: map[string]string{"X-Bad": "$nope"}}},
	}); err == nil {
		t.Error("unknown variable in a location's set_response_headers should fail New")
	}
}
