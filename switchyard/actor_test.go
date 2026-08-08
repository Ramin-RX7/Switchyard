package switchyard_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// --- axis 1 & 2: the default Actor -----------------------------------------

func TestActorForwardReachesBackend(t *testing.T) {
	p, a, _ := twoBackendProxy(t)
	rec := serve(p, "GET", "http://x/api/users")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if a.count() == 0 {
		t.Error("backend api1 was never reached")
	}
}

func TestActorRejectUsesErrorResponders(t *testing.T) {
	p, _, _ := twoBackendProxy(t)
	r := httptest.NewRequest("GET", "http://x/", nil)

	// A 404 reject routes through the not-found responder.
	rec := httptest.NewRecorder()
	p.Actor.Act(rec, r, sw.Request{}, sw.Decision{Action: sw.ActionReject, Reason: "no matching location", Status: http.StatusNotFound})
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no matching location") {
		t.Errorf("body = %q, want not-found default", rec.Body.String())
	}

	// Any other reject routes through the bad-gateway responder (default 502).
	rec = httptest.NewRecorder()
	p.Actor.Act(rec, r, sw.Request{}, sw.Decision{Action: sw.ActionReject, Reason: "empty pool", Status: http.StatusBadGateway})
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "backend unavailable") {
		t.Errorf("body = %q, want bad-gateway default", rec.Body.String())
	}
}

func TestActorRejectDefaultsTo502(t *testing.T) {
	p, _, _ := twoBackendProxy(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://x/", nil)
	p.Actor.Act(rec, r, sw.Request{}, sw.Decision{Action: sw.ActionReject, Reason: "x"}) // Status 0
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (default)", rec.Code)
	}
}

// --- axis 3: a user-supplied Actor is honored ------------------------------

type sentinelActor struct{}

func (sentinelActor) Act(w http.ResponseWriter, _ *http.Request, _ sw.Request, _ sw.Decision) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("acted"))
}

func TestCustomActorHonored(t *testing.T) {
	p, a, _ := twoBackendProxy(t)
	p.Actor = sentinelActor{}
	rec := serve(p, "GET", "http://x/api/users")
	if rec.Body.String() != "acted" {
		t.Errorf("body = %q, want acted", rec.Body.String())
	}
	if a.count() != 0 {
		t.Error("custom actor should have prevented the real forward")
	}
}
