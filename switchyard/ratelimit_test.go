package switchyard_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

func strp(s string) *string { return &s }

// rlServe drives one request with a chosen method/path/remote-addr/headers.
func rlServe(p *sw.Proxy, method, target, remoteAddr string, headers map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	p.Handler().ServeHTTP(rec, req)
	return rec
}

// rlConfig builds a single-backend proxy with a global rate_limit.
func rlProxy(t *testing.T, rl *sw.RateLimitConfig) *sw.Proxy {
	t.Helper()
	be := newEchoBackend(t, "api")
	return mustNew(t, sw.Config{
		Backends:  []sw.BackendConfig{{ID: "api", URL: be.URL}},
		RateLimit: rl,
	})
}

func TestRateLimitTokenBucket(t *testing.T) {
	p := rlProxy(t, &sw.RateLimitConfig{Rate: riptr(2), Period: riptr(3600), Burst: riptr(2)})

	for i := 1; i <= 2; i++ {
		if rec := rlServe(p, "GET", "http://x/", "10.0.0.1:1", nil); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200", i, rec.Code)
		}
	}
	rec := rlServe(p, "GET", "http://x/", "10.0.0.1:1", nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("3rd request: status %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 should carry a Retry-After header")
	}
}

func TestRateLimitRefill(t *testing.T) {
	// 1 token/sec, burst 1: one request, then throttled, then allowed after refill.
	p := rlProxy(t, &sw.RateLimitConfig{Rate: riptr(1), Period: riptr(1), Burst: riptr(1)})

	if rec := rlServe(p, "GET", "http://x/", "10.0.0.2:1", nil); rec.Code != http.StatusOK {
		t.Fatalf("1st: status %d, want 200", rec.Code)
	}
	if rec := rlServe(p, "GET", "http://x/", "10.0.0.2:1", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("2nd: status %d, want 429", rec.Code)
	}
	time.Sleep(1100 * time.Millisecond) // refill ~1 token
	if rec := rlServe(p, "GET", "http://x/", "10.0.0.2:1", nil); rec.Code != http.StatusOK {
		t.Fatalf("after refill: status %d, want 200", rec.Code)
	}
}

func TestRateLimitKeyByIP(t *testing.T) {
	p := rlProxy(t, &sw.RateLimitConfig{Rate: riptr(1), Period: riptr(3600), Burst: riptr(1), Key: []string{"ip"}})

	if rec := rlServe(p, "GET", "http://x/", "1.1.1.1:9", nil); rec.Code != http.StatusOK {
		t.Fatalf("IP A first: %d, want 200", rec.Code)
	}
	if rec := rlServe(p, "GET", "http://x/", "1.1.1.1:9", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("IP A second: %d, want 429", rec.Code)
	}
	if rec := rlServe(p, "GET", "http://x/", "2.2.2.2:9", nil); rec.Code != http.StatusOK {
		t.Fatalf("IP B: %d, want 200 (separate bucket)", rec.Code)
	}
}

func TestRateLimitKeyByHeaderNotMethod(t *testing.T) {
	p := rlProxy(t, &sw.RateLimitConfig{Rate: riptr(1), Period: riptr(3600), Burst: riptr(1), Key: []string{"header:X-Api-Key"}})
	hdr := func(v string) map[string]string { return map[string]string{"X-Api-Key": v} }

	if rec := rlServe(p, "GET", "http://x/", "", hdr("k1")); rec.Code != http.StatusOK {
		t.Fatalf("k1 first: %d, want 200", rec.Code)
	}
	// Same key, different method: still throttled (method is not part of the key).
	if rec := rlServe(p, "POST", "http://x/", "", hdr("k1")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("k1 different method: %d, want 429", rec.Code)
	}
	if rec := rlServe(p, "GET", "http://x/", "", hdr("k2")); rec.Code != http.StatusOK {
		t.Fatalf("k2: %d, want 200 (separate bucket)", rec.Code)
	}
}

func TestRateLimitMethodsFilter(t *testing.T) {
	p := rlProxy(t, &sw.RateLimitConfig{Rate: riptr(1), Period: riptr(3600), Burst: riptr(1), Key: []string{"ip"}, Methods: []string{"POST"}})

	if rec := rlServe(p, "POST", "http://x/", "3.3.3.3:1", nil); rec.Code != http.StatusOK {
		t.Fatalf("POST first: %d, want 200", rec.Code)
	}
	if rec := rlServe(p, "POST", "http://x/", "3.3.3.3:1", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("POST second: %d, want 429", rec.Code)
	}
	// GET is not subject to the rule at all.
	for i := 0; i < 5; i++ {
		if rec := rlServe(p, "GET", "http://x/", "3.3.3.3:1", nil); rec.Code != http.StatusOK {
			t.Fatalf("GET %d: %d, want 200 (not limited)", i, rec.Code)
		}
	}
}

func TestRateLimitTiersStack(t *testing.T) {
	be := newEchoBackend(t, "api")
	p := mustNew(t, sw.Config{
		Backends:  []sw.BackendConfig{{ID: "api", URL: be.URL}},
		RateLimit: &sw.RateLimitConfig{Rate: riptr(100), Period: riptr(3600), Burst: riptr(100)}, // generous global
		Locations: []sw.LocationConfig{{
			Path: "/tight/", Backends: []string{"api"},
			RateLimit: &sw.RateLimitConfig{Rate: riptr(1), Period: riptr(3600), Burst: riptr(1)}, // strict location
		}},
	})
	if rec := rlServe(p, "GET", "http://x/tight/a", "9.9.9.9:1", nil); rec.Code != http.StatusOK {
		t.Fatalf("loc first: %d, want 200", rec.Code)
	}
	if rec := rlServe(p, "GET", "http://x/tight/a", "9.9.9.9:1", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("loc second: %d, want 429 (location tier)", rec.Code)
	}
}

func TestRateLimitGlobalGuardsUnmatched(t *testing.T) {
	// Global limit applies before routing, so it also throttles no-match (404) traffic.
	be := newEchoBackend(t, "api")
	p := mustNew(t, sw.Config{
		Backends:  []sw.BackendConfig{{ID: "api", URL: be.URL}},
		RateLimit: &sw.RateLimitConfig{Rate: riptr(1), Period: riptr(3600), Burst: riptr(1)},
		Locations: []sw.LocationConfig{{Path: "/api/", Backends: []string{"api"}}}, // no catch-all
	})
	if rec := rlServe(p, "GET", "http://x/nope", "8.8.8.8:1", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("1st unmatched: %d, want 404", rec.Code)
	}
	if rec := rlServe(p, "GET", "http://x/nope", "8.8.8.8:1", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("2nd unmatched: %d, want 429 (global guards 404s)", rec.Code)
	}
}

func TestRateLimitHeaderModes(t *testing.T) {
	cases := []struct {
		mode             string
		onAllowed, on429 bool
	}{
		{"off", false, false},
		{"on-reject", false, true},
		{"always", true, true},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			p := rlProxy(t, &sw.RateLimitConfig{Rate: riptr(1), Period: riptr(3600), Burst: riptr(1), Headers: c.mode})
			allowed := rlServe(p, "GET", "http://x/", "7.7.7.7:1", nil)
			has := func(rec *httptest.ResponseRecorder) bool { return rec.Header().Get("RateLimit-Limit") != "" }
			if has(allowed) != c.onAllowed {
				t.Errorf("allowed RateLimit-Limit present=%v, want %v", has(allowed), c.onAllowed)
			}
			rejected := rlServe(p, "GET", "http://x/", "7.7.7.7:1", nil)
			if rejected.Code != http.StatusTooManyRequests {
				t.Fatalf("expected 429, got %d", rejected.Code)
			}
			if has(rejected) != c.on429 {
				t.Errorf("429 RateLimit-Limit present=%v, want %v", has(rejected), c.on429)
			}
		})
	}
}

func TestRateLimitCustomReject(t *testing.T) {
	p := rlProxy(t, &sw.RateLimitConfig{Rate: riptr(1), Period: riptr(3600), Burst: riptr(1), Status: riptr(503), Body: strp("slow down")})
	rlServe(p, "GET", "http://x/", "6.6.6.6:1", nil)
	rec := rlServe(p, "GET", "http://x/", "6.6.6.6:1", nil)
	if rec.Code != 503 || rec.Body.String() != "slow down" {
		t.Fatalf("custom reject = %d %q, want 503 'slow down'", rec.Code, rec.Body.String())
	}
}

// --- SDK swaps ---------------------------------------------------------------

// countingStore wraps a store and counts Get calls, proving the default
// algorithm uses the injected store.
type countingStore struct {
	inner sw.RateLimitStore
	gets  atomic.Int64
}

func (s *countingStore) Get(ctx context.Context, key string) ([]byte, bool, time.Time, error) {
	s.gets.Add(1)
	return s.inner.Get(ctx, key)
}
func (s *countingStore) SetIfAbsent(ctx context.Context, key string, state []byte, ttl time.Duration) (bool, error) {
	return s.inner.SetIfAbsent(ctx, key, state, ttl)
}
func (s *countingStore) CompareAndSwap(ctx context.Context, key string, o, n []byte, ttl time.Duration) (bool, error) {
	return s.inner.CompareAndSwap(ctx, key, o, n, ttl)
}

func TestRateLimitCustomStore(t *testing.T) {
	store := &countingStore{inner: sw.NewMemoryRateLimitStore()}
	p := rlProxy(t, &sw.RateLimitConfig{Rate: riptr(5), Period: riptr(3600), Burst: riptr(5)})
	p.RateLimitStore = store

	rlServe(p, "GET", "http://x/", "5.5.5.5:1", nil)
	if store.gets.Load() == 0 {
		t.Error("custom store was not consulted by the default algorithm")
	}
}

// denyLimiter is a custom algorithm that rejects everything.
type denyLimiter struct{}

func (denyLimiter) Allow(_ context.Context, _ sw.RateLimitStore, _ string, _ float64, burst int) (sw.Allowance, error) {
	return sw.Allowance{OK: false, Limit: burst, RetryAfter: time.Second}, nil
}

// Concurrent requests to one key must be limited (not all admitted) and must be
// race-free under -race.
func TestRateLimitConcurrent(t *testing.T) {
	p := rlProxy(t, &sw.RateLimitConfig{Rate: riptr(5), Period: riptr(3600), Burst: riptr(5)})

	const n = 80
	var allowed atomic.Int64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if rlServe(p, "GET", "http://x/", "1.2.3.4:1", nil).Code == http.StatusOK {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got == 0 || got >= n {
		t.Fatalf("allowed %d of %d, want the limiter to admit some but not all", got, n)
	}
}

func TestRateLimitCustomAlgorithm(t *testing.T) {
	p := rlProxy(t, &sw.RateLimitConfig{Rate: riptr(1000), Period: riptr(1), Burst: riptr(1000)})
	p.RateLimiter = denyLimiter{}
	if rec := rlServe(p, "GET", "http://x/", "4.4.4.4:1", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("custom algorithm should reject: got %d, want 429", rec.Code)
	}
}
