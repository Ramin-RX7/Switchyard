package switchyard

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func dptr(d time.Duration) *Duration { x := Duration(d); return &x }
func iptr(i int) *int                { return &i }

func TestDurationUnmarshal(t *testing.T) {
	var s struct {
		D Duration `json:"d"`
	}
	if err := json.Unmarshal([]byte(`{"d":30}`), &s); err != nil || s.D.std() != 30*time.Second {
		t.Errorf("parse 30 = (%v, %v), want 30s", s.D.std(), err)
	}
	if err := json.Unmarshal([]byte(`{"d":0}`), &s); err != nil || s.D.std() != 0 {
		t.Errorf("0 duration should parse to 0, got (%v, %v)", s.D.std(), err)
	}
	if err := json.Unmarshal([]byte(`{"d":"30s"}`), &s); err == nil {
		t.Error("a duration string should be rejected now (integer seconds only)")
	}
	if err := json.Unmarshal([]byte(`{"d":2.5}`), &s); err == nil {
		t.Error("a fractional-second duration should be rejected")
	}
}

func TestResolveBackendMergePrecedence(t *testing.T) {
	// Defaults when nothing is set.
	def := Config{}.resolveBackend(BackendConfig{})
	if def.tlsHandshake != defaultTLSHandshakeTimeout {
		t.Errorf("default tls = %v, want %v", def.tlsHandshake, defaultTLSHandshakeTimeout)
	}
	if def.maxIdleConnsPerHost != defaultMaxIdleConnsPerHost {
		t.Errorf("default idle/host = %d, want %d", def.maxIdleConnsPerHost, defaultMaxIdleConnsPerHost)
	}
	if def.maxConns != 0 || def.requestTimeout != 0 {
		t.Errorf("defaults: maxConns=%d requestTimeout=%v, want 0/0", def.maxConns, def.requestTimeout)
	}

	// Project default is used when the backend doesn't override.
	proj := Config{Timeouts: &TimeoutsConfig{TLSHandshake: dptr(5 * time.Second)}}
	if got := proj.resolveBackend(BackendConfig{}).tlsHandshake; got != 5*time.Second {
		t.Errorf("project tls inherited = %v, want 5s", got)
	}

	// Backend overrides project.
	bc := BackendConfig{
		MaxConnections: iptr(3),
		Timeouts:       &TimeoutsConfig{TLSHandshake: dptr(2 * time.Second), Request: dptr(time.Minute)},
		Transport:      &TransportConfig{MaxIdleConnsPerHost: iptr(10)},
	}
	got := proj.resolveBackend(bc)
	if got.tlsHandshake != 2*time.Second {
		t.Errorf("backend tls override = %v, want 2s", got.tlsHandshake)
	}
	if got.requestTimeout != time.Minute {
		t.Errorf("backend request timeout = %v, want 1m", got.requestTimeout)
	}
	if got.maxIdleConnsPerHost != 10 {
		t.Errorf("backend idle/host override = %d, want 10", got.maxIdleConnsPerHost)
	}
	if got.maxConns != 3 {
		t.Errorf("backend maxConns = %d, want 3", got.maxConns)
	}
}

func TestServerTimeoutDefaults(t *testing.T) {
	rh, rd, wr, id := Config{}.serverTimeouts()
	if rh != defaultReadHeaderTimeout || id != defaultServerIdleTimeout || rd != 0 || wr != 0 {
		t.Errorf("server defaults = %v/%v/%v/%v", rh, rd, wr, id)
	}
	cfg := Config{Server: &ServerConfig{WriteTimeout: dptr(15 * time.Second)}}
	if _, _, wr, _ := cfg.serverTimeouts(); wr != 15*time.Second {
		t.Errorf("write timeout = %v, want 15s", wr)
	}
}

func TestOverflowPolicyDefaults(t *testing.T) {
	o, err := Config{}.overflowPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if o.strategy != "reject" {
		t.Errorf("overflow default strategy = %q, want reject", o.strategy)
	}
	if st, body := renderResponder(o.resp); st != defaultOverflowStatus || body != defaultOverflowBody {
		t.Errorf("overflow default response = %d %q, want %d %q", st, body, defaultOverflowStatus, defaultOverflowBody)
	}

	cfg := Config{Overflow: &OverflowConfig{Strategy: "queue", QueueTimeout: dptr(2 * time.Second), Status: iptr(429), Body: strptr("busy")}}
	o, err = cfg.overflowPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if o.strategy != "queue" || o.queueWait != 2*time.Second {
		t.Errorf("overflow config = %+v", o)
	}
	if st, body := renderResponder(o.resp); st != 429 || body != "busy" {
		t.Errorf("overflow response = %d %q, want 429 busy", st, body)
	}
}

// renderResponder runs a TemplateResponder against a recorder and returns the
// written status and body, for asserting on generated responses.
func renderResponder(r *TemplateResponder) (int, string) {
	rec := httptest.NewRecorder()
	r.Generate(rec, httptest.NewRequest("GET", "/", nil), Request{})
	return rec.Code, rec.Body.String()
}

func TestConfigValidate(t *testing.T) {
	bad := []Config{
		{MaxConnections: iptr(-1)},
		{Timeouts: &TimeoutsConfig{Request: dptr(-time.Second)}},
		{Transport: &TransportConfig{MaxIdleConns: iptr(-5)}},
		{Overflow: &OverflowConfig{Strategy: "nope"}},
		{Overflow: &OverflowConfig{Status: iptr(99)}},
		{Backends: []BackendConfig{{URL: "http://x", MaxConnections: iptr(-2)}}},
		{BackendError: &ResponseConfig{Status: iptr(99)}},
		{NotFound: &ResponseConfig{Status: iptr(700)}},
		{Locations: []LocationConfig{{Path: "/", Response: &ResponseConfig{Status: iptr(42)}}}},
	}
	for i, c := range bad {
		if err := c.validate(); err == nil {
			t.Errorf("config #%d should have failed validation", i)
		}
	}
	for _, s := range []string{"reject", "queue", "reroute"} {
		if err := (Config{Overflow: &OverflowConfig{Strategy: s}}).validate(); err != nil {
			t.Errorf("strategy %q should be valid: %v", s, err)
		}
	}
}

func strptr(s string) *string { return &s }
