package switchyard_test

import (
	"net/http"
	"strings"
	"testing"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// --- axis 1: real request flow through a "response" location ----------------

func TestResponseLocationGenerates(t *testing.T) {
	p := mustNew(t, sw.Config{
		Locations: []sw.LocationConfig{{
			Path: "/health",
			Type: "response",
			Response: &sw.ResponseConfig{
				Status:  ip(201),
				Headers: map[string]string{"Content-Type": "application/json", "X-Path": "$uri"},
				Body:    `{"ok":true,"at":"$time_iso8601"}`,
			},
		}},
	})
	rec := serve(p, "GET", "http://x/health")
	if rec.Code != 201 {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if xp := rec.Header().Get("X-Path"); xp != "/health" {
		t.Errorf("X-Path = %q, want /health (rendered $uri)", xp)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"ok":true`) {
		t.Errorf("body = %q, want the configured JSON", body)
	}
	if strings.Contains(body, "$time_iso8601") || strings.Contains(body, `"at":""`) {
		t.Errorf("body = %q, want $time_iso8601 rendered to a value", body)
	}
}

// --- axis 2: defaults --------------------------------------------------------

func TestResponseLocationDefaults(t *testing.T) {
	p := mustNew(t, sw.Config{
		Locations: []sw.LocationConfig{{
			Path: "/hi", Type: "response",
			Response: &sw.ResponseConfig{Body: "hello"}, // no status, no Content-Type
		}},
	})
	rec := serve(p, "GET", "http://x/hi")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 default", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain default", ct)
	}
	if rec.Body.String() != "hello" {
		t.Errorf("body = %q, want hello", rec.Body.String())
	}
}

func TestNotFoundResponderDefault(t *testing.T) {
	p := mustNew(t, sw.Config{
		Locations: []sw.LocationConfig{{Path: "/only", Type: "response", Response: &sw.ResponseConfig{Body: "x"}}},
	})
	rec := serve(p, "GET", "http://x/nope")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no matching location") {
		t.Errorf("body = %q, want default not-found message", rec.Body.String())
	}
}

func TestNotFoundResponderConfigured(t *testing.T) {
	p := mustNew(t, sw.Config{
		NotFound: &sw.ResponseConfig{
			Status:  ip(404),
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    `{"error":"not found","path":"$uri"}`,
		},
		Locations: []sw.LocationConfig{{Path: "/only", Type: "response", Response: &sw.ResponseConfig{Body: "x"}}},
	})
	rec := serve(p, "GET", "http://x/missing")
	if rec.Code != 404 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status/content-type = %d/%q, want 404/application/json", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), `"path":"/missing"`) {
		t.Errorf("body = %q, want rendered request path", rec.Body.String())
	}
}

// --- axis 3: user-supplied ResponseGenerators are honored -------------------

type sentinelResponder struct{}

func (sentinelResponder) Generate(w http.ResponseWriter, _ *http.Request, _ sw.Request) {
	w.WriteHeader(http.StatusTeapot)
	_, _ = w.Write([]byte("brewed"))
}

func TestCustomLocationResponderHonored(t *testing.T) {
	p := mustNew(t, sw.Config{
		Locations: []sw.LocationConfig{{Path: "/health", Type: "response", Response: &sw.ResponseConfig{Body: "default"}}},
	})
	for _, loc := range p.Locations {
		if loc.Path() == "/health" {
			loc.Responder = sentinelResponder{}
		}
	}
	rec := serve(p, "GET", "http://x/health")
	if rec.Code != http.StatusTeapot || rec.Body.String() != "brewed" {
		t.Errorf("got %d %q, want 418 brewed", rec.Code, rec.Body.String())
	}
}

func TestCustomNotFoundResponderHonored(t *testing.T) {
	p := mustNew(t, sw.Config{
		Locations: []sw.LocationConfig{{Path: "/only", Type: "response", Response: &sw.ResponseConfig{Body: "x"}}},
	})
	p.NotFound = sentinelResponder{} // override after New; read live by the actor
	rec := serve(p, "GET", "http://x/missing")
	if rec.Code != http.StatusTeapot || rec.Body.String() != "brewed" {
		t.Errorf("got %d %q, want 418 brewed", rec.Code, rec.Body.String())
	}
}

// --- fail-fast: bad response configs are rejected at New --------------------

func TestResponseConfigFailFast(t *testing.T) {
	bad := []sw.Config{
		// unknown variable in the body
		{Locations: []sw.LocationConfig{{Path: "/x", Type: "response", Response: &sw.ResponseConfig{Body: "$nope"}}}},
		// unknown variable in a header value
		{Locations: []sw.LocationConfig{{Path: "/x", Type: "response", Response: &sw.ResponseConfig{Headers: map[string]string{"X-Y": "$nope"}}}}},
		// "response" type without a response block
		{Locations: []sw.LocationConfig{{Path: "/x", Type: "response"}}},
		// unknown variable in a built-in error responder
		{BackendError: &sw.ResponseConfig{Body: "$nope"}},
	}
	for i, c := range bad {
		if _, err := sw.New(c); err == nil {
			t.Errorf("config #%d should have failed New", i)
		}
	}
}
