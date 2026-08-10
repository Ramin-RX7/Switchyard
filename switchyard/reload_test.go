package switchyard_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

func serveH(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// A graceful reload swaps the config for subsequent requests.
func TestServerGracefulReloadSwapsConfig(t *testing.T) {
	a := newEchoBackend(t, "A")
	b := newEchoBackend(t, "B")
	target := a.URL
	build := func() (*sw.Proxy, error) {
		return sw.New(sw.Config{Backends: []sw.BackendConfig{{ID: "x", URL: target}}})
	}
	s := &sw.Server{Build: build}
	h, err := s.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := serveH(h, "GET", "http://x/").Body.String(); got != "A" {
		t.Fatalf("before reload = %q, want A", got)
	}
	target = b.URL
	s.Reload(false)
	if got := serveH(h, "GET", "http://x/").Body.String(); got != "B" {
		t.Fatalf("after reload = %q, want B", got)
	}
}

// During a graceful reload, a request already in flight finishes on the OLD
// config while new requests use the new one.
func TestServerGracefulReloadInflightUsesOldConfig(t *testing.T) {
	url, gate, hit := blockingBackend(t)
	fast := newEchoBackend(t, "B")
	target := url
	build := func() (*sw.Proxy, error) {
		return sw.New(sw.Config{Backends: []sw.BackendConfig{{ID: "x", URL: target}}})
	}
	s := &sw.Server{Build: build}
	h, err := s.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	codes := make(chan int, 1)
	go func() { codes <- serveH(h, "GET", "http://x/").Code }()
	<-hit // parked on the old (slow) backend

	target = fast.URL
	s.Reload(false) // graceful: in-flight keeps old, new requests use new

	if got := serveH(h, "GET", "http://x/").Body.String(); got != "B" {
		t.Errorf("new request after reload = %q, want B (new config)", got)
	}
	close(gate) // let the in-flight request finish on the old backend
	if code := <-codes; code != http.StatusOK {
		t.Errorf("in-flight request during graceful reload = %d, want 200 (old backend)", code)
	}
}

// A force reload cancels in-flight requests with a 503.
func TestServerForceReloadCancelsInflight(t *testing.T) {
	url, gate, hit := blockingBackend(t)
	defer close(gate)
	build := func() (*sw.Proxy, error) {
		return sw.New(sw.Config{Backends: []sw.BackendConfig{{ID: "x", URL: url}}})
	}
	s := &sw.Server{Build: build}
	h, err := s.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	codes := make(chan int, 1)
	go func() { codes <- serveH(h, "GET", "http://x/").Code }()
	<-hit // parked on the backend
	s.Reload(true)
	if code := <-codes; code != http.StatusServiceUnavailable {
		t.Errorf("in-flight request during force reload = %d, want 503", code)
	}
}

// A reload whose Build fails leaves the current config serving.
func TestServerReloadBadConfigKeepsOld(t *testing.T) {
	a := newEchoBackend(t, "A")
	calls := 0
	build := func() (*sw.Proxy, error) {
		calls++
		if calls >= 2 {
			return nil, fmt.Errorf("boom")
		}
		return sw.New(sw.Config{Backends: []sw.BackendConfig{{ID: "x", URL: a.URL}}})
	}
	s := &sw.Server{Build: build}
	h, err := s.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	s.Reload(false) // Build fails on the second call → keep old
	if got := serveH(h, "GET", "http://x/").Body.String(); got != "A" {
		t.Errorf("after failed reload = %q, want A (old config retained)", got)
	}
}

func TestServerStartBuildError(t *testing.T) {
	s := &sw.Server{Build: func() (*sw.Proxy, error) { return nil, fmt.Errorf("nope") }}
	if _, err := s.Start(); err == nil {
		t.Error("Start should fail when Build errors")
	}
	if _, err := (&sw.Server{}).Start(); err == nil {
		t.Error("Start should fail when Build is nil")
	}
}

func TestReadPidFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.pid")
	if err := os.WriteFile(p, []byte("4321\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if pid, err := sw.ReadPidFile(p); err != nil || pid != 4321 {
		t.Errorf("ReadPidFile = (%d, %v), want (4321, nil)", pid, err)
	}
	if _, err := sw.ReadPidFile(filepath.Join(dir, "missing")); err == nil {
		t.Error("ReadPidFile on a missing file should error")
	}
	if err := os.WriteFile(p, []byte("notanumber"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := sw.ReadPidFile(p); err == nil {
		t.Error("ReadPidFile on garbage should error")
	}
}
