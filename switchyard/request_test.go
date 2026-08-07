package switchyard

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
)

func TestCaptureRequestFields(t *testing.T) {
	r := httptest.NewRequest("GET", "http://example.com/v1/users?id=1&sort=asc", nil)
	r.Host = "example.com"
	r.RemoteAddr = "203.0.113.5:54321"

	req := captureRequest(r)

	if req.Method != "GET" {
		t.Errorf("Method = %q, want GET", req.Method)
	}
	if req.Path != "/v1/users" {
		t.Errorf("Path = %q, want /v1/users", req.Path)
	}
	if req.Host != "example.com" {
		t.Errorf("Host = %q, want example.com", req.Host)
	}
	if req.Scheme != "http" {
		t.Errorf("Scheme = %q, want http", req.Scheme)
	}
	if req.RawQuery != "id=1&sort=asc" {
		t.Errorf("RawQuery = %q, want id=1&sort=asc", req.RawQuery)
	}
	if want := "http://example.com/v1/users?id=1&sort=asc"; req.URL != want {
		t.Errorf("URL = %q, want %q", req.URL, want)
	}
	if got := req.Query.Get("id"); got != "1" {
		t.Errorf("Query id = %q, want 1", got)
	}
	if req.RemoteAddr != "203.0.113.5:54321" {
		t.Errorf("RemoteAddr = %q", req.RemoteAddr)
	}
	if req.ReceivedAt.IsZero() {
		t.Error("ReceivedAt is zero, want a timestamp")
	}
}

func TestCaptureRequestSchemeHTTPS(t *testing.T) {
	r := httptest.NewRequest("GET", "https://secure.example.com/", nil)
	r.TLS = &tls.ConnectionState{}
	if got := captureRequest(r).Scheme; got != "https" {
		t.Errorf("Scheme = %q, want https", got)
	}
}

// The snapshot must clone headers so later set_headers mutation of the forwarded
// request never changes what was captured (and logged).
func TestCaptureRequestHeaderCloneIsIndependent(t *testing.T) {
	r := httptest.NewRequest("GET", "http://example.com/", nil)
	r.Header.Set("X-Test", "original")

	req := captureRequest(r)
	if got := req.Header.Get("X-Test"); got != "original" {
		t.Fatalf("snapshot X-Test = %q, want original", got)
	}

	r.Header.Set("X-Test", "mutated") // mutate the live request after capture
	if got := req.Header.Get("X-Test"); got != "original" {
		t.Errorf("snapshot X-Test = %q after mutating live request, want original (clone leaked)", got)
	}
}
