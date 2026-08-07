package switchyard_test

import (
	"testing"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

func routerProxy(t *testing.T) *sw.Proxy {
	t.Helper()
	a := newEchoBackend(t, "api1")
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "api1", URL: a.URL}},
		Locations: []sw.LocationConfig{
			{Path: "/api/", Backends: []string{"api1"}},
			{Path: `^/v[0-9]+/`, Regex: true, Backends: []string{"api1"}},
		},
	}
	return mustNew(t, cfg)
}

// --- axis 2: default first-match routing -----------------------------------

func TestDefaultRouterMatch(t *testing.T) {
	p := routerProxy(t)
	tests := []struct {
		path string
		want string // matched Path(), or "" for no match
	}{
		{"/api/users", "/api/"},
		{"/v2/items", `^/v[0-9]+/`},
		{"/v99/x", `^/v[0-9]+/`},
		{"/vX/x", ""},  // regex doesn't match
		{"/other", ""}, // no location
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			loc := p.Router.Match(sw.Request{Path: tt.path})
			switch {
			case tt.want == "" && loc != nil:
				t.Errorf("Match(%q) = %q, want no match", tt.path, loc.Path())
			case tt.want != "" && (loc == nil || loc.Path() != tt.want):
				t.Errorf("Match(%q) = %v, want %q", tt.path, loc, tt.want)
			}
		})
	}
}

func TestDefaultRouterFirstMatchWins(t *testing.T) {
	a := newEchoBackend(t, "api1")
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "api1", URL: a.URL}},
		Locations: []sw.LocationConfig{
			{Path: "/api/", Backends: []string{"api1"}},
			{Path: "/api/v2/", Backends: []string{"api1"}}, // more specific, but second
		},
	}
	p := mustNew(t, cfg)
	if loc := p.Router.Match(sw.Request{Path: "/api/v2/x"}); loc == nil || loc.Path() != "/api/" {
		t.Errorf("first-match: got %v, want /api/ (declared first)", loc)
	}
}

// --- axis 3: a user-supplied Router is honored -----------------------------

// headerRouter routes to a chosen location based on an X-Route header.
type headerRouter struct{ a, b *sw.Location }

func (r headerRouter) Match(req sw.Request) *sw.Location {
	if req.Header.Get("X-Route") == "b" {
		return r.b
	}
	return r.a
}

func TestCustomRouterHonored(t *testing.T) {
	a := newEchoBackend(t, "api1")
	b := newEchoBackend(t, "api2")
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "api1", URL: a.URL}, {ID: "api2", URL: b.URL}},
		Locations: []sw.LocationConfig{
			{Path: "/one/", Backends: []string{"api1"}},
			{Path: "/two/", Backends: []string{"api2"}},
		},
	}
	p := mustNew(t, cfg)
	p.Router = headerRouter{a: p.Locations[0], b: p.Locations[1]}

	// Same path, different header → different backend, decided by the custom router.
	if got := p.Decider.Decide(sw.Request{Path: "/anything"}).Backend.ID; got != "api1" {
		t.Errorf("no header → %q, want api1", got)
	}
	h := map[string][]string{"X-Route": {"b"}}
	if got := p.Decider.Decide(sw.Request{Path: "/anything", Header: h}).Backend.ID; got != "api2" {
		t.Errorf("X-Route:b → %q, want api2", got)
	}
}
