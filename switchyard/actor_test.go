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

func TestActorRejectWritesReasonAndStatus(t *testing.T) {
	p, _, _ := twoBackendProxy(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://x/", nil)
	p.Actor.Act(rec, r, sw.Request{}, sw.Decision{Action: sw.ActionReject, Reason: "boom", Status: http.StatusServiceUnavailable})
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "switchyard: boom") {
		t.Errorf("body = %q, want it to contain 'switchyard: boom'", rec.Body.String())
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
