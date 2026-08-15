package switchyard

import (
	"testing"
	"time"
)

func TestResolveRetryDefaults(t *testing.T) {
	p, err := resolveRetry(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.enabled() {
		t.Error("empty policy should be disabled (attempts default 0)")
	}
	if !p.onConnError || !p.retrySameBackend || !p.skipUnhealthy {
		t.Errorf("defaults: onConnError/retrySameBackend/skipUnhealthy = %v/%v/%v, want all true",
			p.onConnError, p.retrySameBackend, p.skipUnhealthy)
	}
	if p.maxBodyBytes != defaultRetryMaxBodyBytes {
		t.Errorf("maxBodyBytes = %d, want %d", p.maxBodyBytes, defaultRetryMaxBodyBytes)
	}
	if p.backoff.strategy != "exponential" || !p.backoff.jitter ||
		p.backoff.base != defaultRetryBackoffBaseMs*time.Millisecond ||
		p.backoff.max != defaultRetryBackoffMaxMs*time.Millisecond {
		t.Errorf("backoff defaults = %+v", p.backoff)
	}
}

func TestResolveRetryFieldMerge(t *testing.T) {
	global := &RetryConfig{
		Attempts:      iptr(3),
		OnStatus:      []int{500},
		SkipUnhealthy: bptrI(false),
		Backoff:       &BackoffConfig{Strategy: strptr("constant"), BaseMs: iptr(100)},
	}
	loc := &RetryConfig{
		OnStatus: []int{502, 503}, // location overrides just this field
		Backoff:  &BackoffConfig{BaseMs: iptr(200)},
	}
	p, err := resolveRetry(global, loc)
	if err != nil {
		t.Fatal(err)
	}
	if p.attempts != 3 { // inherited from global
		t.Errorf("attempts = %d, want 3 (inherited)", p.attempts)
	}
	if p.skipUnhealthy { // inherited from global
		t.Error("skipUnhealthy should be false (inherited from global)")
	}
	if !p.onStatus[502] || !p.onStatus[503] || p.onStatus[500] {
		t.Errorf("on_status = %v, want {502,503} (location wins)", p.onStatus)
	}
	if p.backoff.strategy != "constant" { // inherited
		t.Errorf("backoff.strategy = %q, want constant (inherited)", p.backoff.strategy)
	}
	if p.backoff.base != 200*time.Millisecond { // location wins
		t.Errorf("backoff.base = %v, want 200ms (location wins)", p.backoff.base)
	}
}

func TestBackoffDelay(t *testing.T) {
	exp := backoffPolicy{strategy: "exponential", base: 100 * time.Millisecond, max: time.Second}
	want := []time.Duration{100, 200, 400, 800, 1000, 1000}
	for i, w := range want {
		if got := exp.delay(i + 1); got != w*time.Millisecond {
			t.Errorf("exp.delay(%d) = %v, want %v", i+1, got, w*time.Millisecond)
		}
	}

	con := backoffPolicy{strategy: "constant", base: 100 * time.Millisecond}
	for _, n := range []int{1, 2, 5} {
		if got := con.delay(n); got != 100*time.Millisecond {
			t.Errorf("constant.delay(%d) = %v, want 100ms", n, got)
		}
	}

	none := backoffPolicy{strategy: "none", base: time.Second}
	if got := none.delay(3); got != 0 {
		t.Errorf("none.delay = %v, want 0", got)
	}
}

func TestBackoffJitterBounds(t *testing.T) {
	b := backoffPolicy{strategy: "constant", base: 100 * time.Millisecond, jitter: true}
	for i := 0; i < 100; i++ {
		if d := b.delay(1); d < 0 || d > 100*time.Millisecond {
			t.Fatalf("jittered delay %v out of [0,100ms]", d)
		}
	}
}

func TestRetryValidation(t *testing.T) {
	bad := []Config{
		{Retry: &RetryConfig{Attempts: iptr(-1)}},
		{Retry: &RetryConfig{MaxBodyBytes: iptr(-1)}},
		{Retry: &RetryConfig{OnStatus: []int{99}}},
		{Retry: &RetryConfig{Backoff: &BackoffConfig{Strategy: strptr("bogus")}}},
		{Retry: &RetryConfig{Backoff: &BackoffConfig{BaseMs: iptr(-5)}}},
		{Retry: &RetryConfig{Response: &ResponseConfig{Status: iptr(42)}}},
		{Locations: []LocationConfig{{Path: "/", Retry: &RetryConfig{Attempts: iptr(-1)}}}},
	}
	for i, c := range bad {
		if err := c.validate(); err == nil {
			t.Errorf("retry config #%d should have failed validation", i)
		}
	}
	for _, s := range []string{"none", "constant", "exponential"} {
		if err := (Config{Retry: &RetryConfig{Backoff: &BackoffConfig{Strategy: strptr(s)}}}).validate(); err != nil {
			t.Errorf("backoff strategy %q should be valid: %v", s, err)
		}
	}
}

// bptrI is a bool-pointer helper for the internal (white-box) tests.
func bptrI(b bool) *bool { return &b }
