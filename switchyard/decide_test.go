package switchyard_test

import (
	"net/http"
	"strings"
	"testing"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// twoBackendProxy builds a proxy with two api backends behind an "/api/" proxy
// location plus a "/" catch-all to the first backend.
func twoBackendProxy(t *testing.T) (*sw.Proxy, *echoBackend, *echoBackend) {
	t.Helper()
	a := newEchoBackend(t, "api1")
	b := newEchoBackend(t, "api2")
	cfg := sw.Config{
		Backends: []sw.BackendConfig{
			{ID: "api1", URL: a.URL},
			{ID: "api2", URL: b.URL},
		},
		Locations: []sw.LocationConfig{
			{Path: "/api/", Backends: []string{"api1", "api2"}},
		},
	}
	return mustNew(t, cfg), a, b
}

// --- axis 1 & 2: the default decision logic --------------------------------

func TestDecideForwardToLocationPool(t *testing.T) {
	p, _, _ := twoBackendProxy(t)
	d := p.Decider.Decide(sw.Request{Path: "/api/users"})
	if d.Action != sw.ActionForward {
		t.Fatalf("Action = %q, want forward", d.Action)
	}
	if d.Backend == nil || (d.Backend.ID != "api1" && d.Backend.ID != "api2") {
		t.Fatalf("Backend = %+v, want one of api1/api2", d.Backend)
	}
	if d.Location == nil || d.Location.Path() != "/api/" {
		t.Fatalf("Location = %+v, want /api/", d.Location)
	}
}

func TestDecideNoMatchingLocationIs404(t *testing.T) {
	p, _, _ := twoBackendProxy(t)
	d := p.Decider.Decide(sw.Request{Path: "/nope"})
	if d.Action != sw.ActionReject || d.Status != http.StatusNotFound {
		t.Fatalf("got action=%q status=%d, want reject/404", d.Action, d.Status)
	}
}

func TestDecideEmptyPoolIs502(t *testing.T) {
	p, _, _ := twoBackendProxy(t)
	p.Locations[0].Pool = sw.NewStaticPool(nil) // drain the pool post-New
	d := p.Decider.Decide(sw.Request{Path: "/api/x"})
	if d.Action != sw.ActionReject || d.Status != http.StatusBadGateway {
		t.Fatalf("got action=%q status=%d, want reject/502", d.Action, d.Status)
	}
}

func TestDecideStaticLocation(t *testing.T) {
	dir := t.TempDir()
	cfg := sw.Config{
		Locations: []sw.LocationConfig{{Path: "/media/", Type: "static", Root: dir}},
	}
	p := mustNew(t, cfg)
	d := p.Decider.Decide(sw.Request{Path: "/media/logo.png"})
	if d.Action != sw.ActionStatic {
		t.Fatalf("Action = %q, want static", d.Action)
	}
}

func TestDecideNoBackendsConfigured(t *testing.T) {
	p := mustNew(t, sw.Config{}) // no backends, no locations
	d := p.Decider.Decide(sw.Request{Path: "/"})
	if d.Action != sw.ActionReject {
		t.Fatalf("Action = %q, want reject", d.Action)
	}
}

// Default backend selection is round-robin: consecutive decisions alternate.
func TestDecideGlobalRoundRobinAlternates(t *testing.T) {
	a := newEchoBackend(t, "api1")
	b := newEchoBackend(t, "api2")
	cfg := sw.Config{Backends: []sw.BackendConfig{{ID: "api1", URL: a.URL}, {ID: "api2", URL: b.URL}}}
	p := mustNew(t, cfg)

	var got []string
	for i := 0; i < 4; i++ {
		got = append(got, p.Decider.Decide(sw.Request{Path: "/"}).Backend.ID)
	}
	want := []string{"api1", "api2", "api1", "api2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("round-robin sequence = %v, want %v", got, want)
		}
	}
}

// --- axis 3: a user-supplied Decider is honored ----------------------------

type teapotDecider struct{}

func (teapotDecider) Decide(sw.Request) sw.Decision {
	return sw.Decision{Action: sw.ActionReject, Reason: "custom decider", Status: http.StatusTeapot}
}

func TestCustomDeciderIsHonored(t *testing.T) {
	p, _, _ := twoBackendProxy(t)
	p.Decider = teapotDecider{} // override the whole decide stage

	rec := serve(p, "GET", "http://x/api/users")
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418 from custom decider", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "custom decider") {
		t.Errorf("body = %q, want it to contain the custom reason", rec.Body.String())
	}
}
