package switchyard_test

import (
	"net/http"
	"strings"
	"testing"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// accessProxy builds a proxy with a single location (path "/") over one backend,
// restricted by the given whitelist/blacklist.
func accessProxy(t *testing.T, whitelist, blacklist []string) *sw.Proxy {
	t.Helper()
	b := newEchoBackend(t, "b")
	return mustNew(t, sw.Config{
		Backends: []sw.BackendConfig{{ID: "b", URL: b.URL}},
		Locations: []sw.LocationConfig{{
			Path: "/", Backends: []string{"b"},
			Whitelist: whitelist, Blacklist: blacklist,
		}},
	})
}

// decideFrom runs the decider for a request from remoteAddr and returns the
// decision (no I/O — the decider is pure).
func decideFrom(p *sw.Proxy, remoteAddr string) sw.Decision {
	return p.Decider.Decide(sw.Request{Method: "GET", Path: "/x", RemoteAddr: remoteAddr})
}

// --- axis 1 & 2: default IP access control ---------------------------------

func TestAccessBlacklistDenies(t *testing.T) {
	p := accessProxy(t, nil, []string{"10.0.0.0/8"})
	if d := decideFrom(p, "10.1.2.3:5000"); d.Action != sw.ActionReject || d.Status != http.StatusForbidden {
		t.Errorf("blacklisted IP: action=%v status=%d, want reject/403", d.Action, d.Status)
	}
	if d := decideFrom(p, "8.8.8.8:5000"); d.Action != sw.ActionForward {
		t.Errorf("non-blacklisted IP: action=%v, want forward", d.Action)
	}
}

func TestAccessWhitelistOnly(t *testing.T) {
	p := accessProxy(t, []string{"8.8.8.8", "203.0.113.0/24"}, nil)
	if d := decideFrom(p, "8.8.8.8:1"); d.Action != sw.ActionForward {
		t.Errorf("whitelisted single IP: action=%v, want forward", d.Action)
	}
	if d := decideFrom(p, "203.0.113.7:1"); d.Action != sw.ActionForward {
		t.Errorf("whitelisted CIDR IP: action=%v, want forward", d.Action)
	}
	if d := decideFrom(p, "1.1.1.1:1"); d.Status != http.StatusForbidden {
		t.Errorf("non-whitelisted IP: status=%d, want 403", d.Status)
	}
}

func TestAccessBlacklistBeatsWhitelist(t *testing.T) {
	p := accessProxy(t, []string{"10.0.0.0/8"}, []string{"10.0.0.5"})
	if d := decideFrom(p, "10.0.0.5:1"); d.Status != http.StatusForbidden {
		t.Errorf("blacklisted IP inside whitelist range: status=%d, want 403", d.Status)
	}
	if d := decideFrom(p, "10.0.0.9:1"); d.Action != sw.ActionForward {
		t.Errorf("whitelisted, not blacklisted: action=%v, want forward", d.Action)
	}
}

func TestAccessNoRestrictionAllowsAll(t *testing.T) {
	p := accessProxy(t, nil, nil)
	if d := decideFrom(p, "203.0.113.1:1"); d.Action != sw.ActionForward {
		t.Errorf("unrestricted location: action=%v, want forward", d.Action)
	}
}

// End-to-end: a denied client gets the configurable 403 response.
func TestAccessForbiddenResponse(t *testing.T) {
	// httptest.NewRequest sets RemoteAddr 192.0.2.1:1234, which this blacklist
	// covers.
	p := accessProxy(t, nil, []string{"192.0.2.0/24"})
	rec := serve(p, "GET", "http://x/thing")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "forbidden") {
		t.Errorf("body = %q, want default forbidden message", body)
	}
}

// --- axis 3: SDK overrides -------------------------------------------------

type denyAll struct{}

func (denyAll) Allow(sw.Request) bool { return false }

func TestCustomAccessControllerHonored(t *testing.T) {
	p := accessProxy(t, nil, nil) // no config restriction
	for _, loc := range p.Locations {
		loc.Access = denyAll{} // override after New
	}
	if d := decideFrom(p, "8.8.8.8:1"); d.Status != http.StatusForbidden {
		t.Errorf("custom denyAll: status=%d, want 403", d.Status)
	}
}

func TestCustomForbiddenResponderHonored(t *testing.T) {
	p := accessProxy(t, nil, []string{"192.0.2.0/24"})
	p.Forbidden = sentinelResponder{} // defined in response_test.go (same package)
	rec := serve(p, "GET", "http://x/thing")
	if rec.Code != http.StatusTeapot || rec.Body.String() != "brewed" {
		t.Errorf("got %d %q, want 418 brewed", rec.Code, rec.Body.String())
	}
}

// --- fail-fast -------------------------------------------------------------

func TestAccessFailFastBadEntry(t *testing.T) {
	b := newEchoBackend(t, "b")
	bad := [][]string{{"not-an-ip"}, {"10.0.0.0/33"}, {""}}
	for i, list := range bad {
		_, err := sw.New(sw.Config{
			Backends:  []sw.BackendConfig{{ID: "b", URL: b.URL}},
			Locations: []sw.LocationConfig{{Path: "/", Backends: []string{"b"}, Blacklist: list}},
		})
		if err == nil {
			t.Errorf("bad blacklist #%d %v should fail New", i, list)
		}
	}
}
