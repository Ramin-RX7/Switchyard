package switchyard

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// clockStore is a single-key store with a settable clock, for deterministic
// token-bucket tests.
type clockStore struct {
	now   time.Time
	state []byte
	set   bool
}

func (s *clockStore) Get(_ context.Context, _ string) ([]byte, bool, time.Time, error) {
	return s.state, s.set, s.now, nil
}
func (s *clockStore) SetIfAbsent(_ context.Context, _ string, state []byte, _ time.Duration) (bool, error) {
	if s.set {
		return false, nil
	}
	s.state, s.set = state, true
	return true, nil
}
func (s *clockStore) CompareAndSwap(_ context.Context, _ string, _, new []byte, _ time.Duration) (bool, error) {
	s.state = new
	return true, nil
}

func TestTokenBucketRefillMath(t *testing.T) {
	st := &clockStore{now: time.Unix(1_000_000, 0)}
	lim := TokenBucketLimiter{}
	ctx := context.Background()

	// rate 1 tok/sec, burst 2: two immediate requests pass, third is denied.
	if a, _ := lim.Allow(ctx, st, "k", 1, 2); !a.OK || a.Remaining != 1 {
		t.Fatalf("1st: %+v, want OK remaining 1", a)
	}
	if a, _ := lim.Allow(ctx, st, "k", 1, 2); !a.OK || a.Remaining != 0 {
		t.Fatalf("2nd: %+v, want OK remaining 0", a)
	}
	a, _ := lim.Allow(ctx, st, "k", 1, 2)
	if a.OK {
		t.Fatalf("3rd should be denied, got %+v", a)
	}
	if a.RetryAfter <= 0 {
		t.Errorf("denied allowance should set RetryAfter, got %v", a.RetryAfter)
	}

	// Advance 2s → refill 2 tokens (capped at burst).
	st.now = st.now.Add(2 * time.Second)
	if a, _ := lim.Allow(ctx, st, "k", 1, 2); !a.OK {
		t.Fatalf("after refill: %+v, want OK", a)
	}
}

func TestMemoryStoreCASAndTTL(t *testing.T) {
	s := NewMemoryRateLimitStore()
	ctx := context.Background()

	ok, _ := s.SetIfAbsent(ctx, "k", []byte("v1"), time.Hour)
	if !ok {
		t.Fatal("SetIfAbsent on empty key should succeed")
	}
	if ok, _ := s.SetIfAbsent(ctx, "k", []byte("v2"), time.Hour); ok {
		t.Fatal("SetIfAbsent on existing key should fail")
	}

	// CAS with wrong old fails; with right old succeeds.
	if ok, _ := s.CompareAndSwap(ctx, "k", []byte("wrong"), []byte("v3"), time.Hour); ok {
		t.Fatal("CAS with mismatched old should fail")
	}
	if ok, _ := s.CompareAndSwap(ctx, "k", []byte("v1"), []byte("v3"), time.Hour); !ok {
		t.Fatal("CAS with correct old should succeed")
	}
	if got, _, _, _ := s.Get(ctx, "k"); string(got) != "v3" {
		t.Fatalf("after CAS, value = %q, want v3", got)
	}

	// TTL expiry: a tiny ttl makes the key absent shortly after.
	s.SetIfAbsent(ctx, "e", []byte("x"), 20*time.Millisecond)
	time.Sleep(40 * time.Millisecond)
	if _, exists, _, _ := s.Get(ctx, "e"); exists {
		t.Error("key should have expired")
	}
}

func TestRateLimitKeyFor(t *testing.T) {
	rule, err := newRateLimitRule("loc", &RateLimitConfig{
		Rate: iptr(1), Key: []string{"ip", "method", "header:X-Api-Key", "path"},
	})
	if err != nil || rule == nil {
		t.Fatalf("newRateLimitRule: rule=%v err=%v", rule, err)
	}
	req := Request{
		Method:     "POST",
		Path:       "/v1/x",
		RemoteAddr: "203.0.113.5:4444",
		Header:     http.Header{"X-Api-Key": []string{"abc"}},
		Query:      url.Values{},
	}
	if got, want := rule.keyFor(req), "loc|203.0.113.5|POST|abc|/v1/x"; got != want {
		t.Errorf("keyFor = %q, want %q", got, want)
	}
}

func TestNewRateLimitRuleDisabled(t *testing.T) {
	if r, _ := newRateLimitRule("g", nil); r != nil {
		t.Error("nil config should yield no rule")
	}
	if r, _ := newRateLimitRule("g", &RateLimitConfig{Rate: iptr(0)}); r != nil {
		t.Error("rate 0 should yield no rule (disabled)")
	}
	// Burst defaults to rate.
	r, _ := newRateLimitRule("g", &RateLimitConfig{Rate: iptr(5), Period: iptr(1)})
	if r == nil || r.burst != 5 || r.rate != 5 {
		t.Fatalf("defaults: %+v, want burst 5 rate 5/s", r)
	}
}

func TestRateLimitValidation(t *testing.T) {
	bad := []Config{
		{RateLimit: &RateLimitConfig{Key: []string{"bogus"}}},
		{RateLimit: &RateLimitConfig{Key: []string{"header:"}}},
		{RateLimit: &RateLimitConfig{Rate: iptr(-1)}},
		{RateLimit: &RateLimitConfig{Headers: "sometimes"}},
		{RateLimit: &RateLimitConfig{Status: iptr(99)}},
		{Locations: []LocationConfig{{Path: "/", RateLimit: &RateLimitConfig{Rate: iptr(-1)}}}},
	}
	for i, c := range bad {
		if err := c.validate(); err == nil {
			t.Errorf("rate-limit config #%d should have failed validation", i)
		}
	}
	ok := Config{RateLimit: &RateLimitConfig{Key: []string{"ip", "header:X", "method", "path"}, Rate: iptr(10), Headers: "always"}}
	if err := ok.validate(); err != nil {
		t.Errorf("valid rate-limit config rejected: %v", err)
	}
}
