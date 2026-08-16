package switchyard

import (
	"bytes"
	"context"
	"encoding/binary"
	"log"
	"math"
	"strings"
	"sync"
	"time"
)

// RateLimitStore is the pluggable, uniform storage for rate-limit state. It holds
// an opaque state blob per key with a TTL and exposes atomic primitives an
// algorithm composes into a decision. The value is []byte so any algorithm owns
// its own state shape; `now` is the store's clock (so a distributed store keeps
// time consistent). The same interface backs the in-memory default and any
// external store (Redis, memcached, …) — SDK users swap it via Proxy.RateLimitStore.
type RateLimitStore interface {
	// Get returns the state stored for key (exists=false when absent/expired) and
	// the store's current time.
	Get(ctx context.Context, key string) (state []byte, exists bool, now time.Time, err error)
	// SetIfAbsent stores state for key with ttl only if key is currently absent.
	SetIfAbsent(ctx context.Context, key string, state []byte, ttl time.Duration) (ok bool, err error)
	// CompareAndSwap replaces key's state with newState (and resets ttl) only if
	// its current state equals oldState.
	CompareAndSwap(ctx context.Context, key string, oldState, newState []byte, ttl time.Duration) (ok bool, err error)
}

// RateLimiter is the pluggable rate-limit algorithm. Allow decides whether ONE
// request against key is permitted under a bucket of rate tokens/sec and burst
// capacity, keeping state in store. It is stateless across keys (all state lives
// in the store), so a single instance serves every rule. The default is
// TokenBucketLimiter; SDK users swap it via Proxy.RateLimiter.
type RateLimiter interface {
	Allow(ctx context.Context, store RateLimitStore, key string, rate float64, burst int) (Allowance, error)
}

// Allowance is the outcome of a rate-limit check, carrying the metadata used for
// the Retry-After and RateLimit-* response headers.
type Allowance struct {
	OK         bool
	Limit      int           // bucket capacity (RateLimit-Limit)
	Remaining  int           // whole tokens left (RateLimit-Remaining)
	RetryAfter time.Duration // wait until one token is available (Retry-After; set when !OK)
	Reset      time.Duration // until the bucket is full again (RateLimit-Reset)
}

// --- default algorithm: token bucket ----------------------------------------

// TokenBucketLimiter is the default RateLimiter: a plain token bucket. The
// per-key state is {tokens, last-update} encoded into the store blob; each call
// refills by elapsed×rate (capped at burst), takes one token if available, and
// commits with an optimistic Get→SetIfAbsent/CompareAndSwap loop.
type TokenBucketLimiter struct{}

// maxCASAttempts bounds the optimistic retry loop; on repeated contention the
// limiter fails open (allows) to favor availability over strict accuracy.
const maxCASAttempts = 5

func (TokenBucketLimiter) Allow(ctx context.Context, store RateLimitStore, key string, rate float64, burst int) (Allowance, error) {
	if rate <= 0 || burst <= 0 {
		return Allowance{OK: false, Limit: burst}, nil // a zero bucket blocks everything
	}
	ttl := time.Duration(float64(burst)/rate*float64(time.Second)) + time.Second
	for attempt := 0; attempt < maxCASAttempts; attempt++ {
		state, exists, now, err := store.Get(ctx, key)
		if err != nil {
			return Allowance{}, err
		}
		tokens := float64(burst)
		if exists {
			t, tsNanos, ok := decodeBucket(state)
			if ok {
				elapsed := now.Sub(time.Unix(0, tsNanos)).Seconds()
				if elapsed > 0 {
					tokens = math.Min(float64(burst), t+elapsed*rate)
				} else {
					tokens = t
				}
			}
		}

		allowed := tokens >= 1
		if allowed {
			tokens -= 1
		}
		newState := encodeBucket(tokens, now.UnixNano())

		var committed bool
		if exists {
			committed, err = store.CompareAndSwap(ctx, key, state, newState, ttl)
		} else {
			committed, err = store.SetIfAbsent(ctx, key, newState, ttl)
		}
		if err != nil {
			return Allowance{}, err
		}
		if committed {
			a := Allowance{OK: allowed, Limit: burst, Remaining: int(tokens)}
			a.Reset = time.Duration((float64(burst) - tokens) / rate * float64(time.Second))
			if !allowed {
				a.RetryAfter = time.Duration((1 - tokens) / rate * float64(time.Second))
			}
			return a, nil
		}
		// Lost the race — another request updated the key; retry.
	}
	// Contention fallback: fail open.
	return Allowance{OK: true, Limit: burst}, nil
}

func encodeBucket(tokens float64, tsNanos int64) []byte {
	b := make([]byte, 16)
	binary.BigEndian.PutUint64(b[0:8], math.Float64bits(tokens))
	binary.BigEndian.PutUint64(b[8:16], uint64(tsNanos))
	return b
}

func decodeBucket(b []byte) (tokens float64, tsNanos int64, ok bool) {
	if len(b) != 16 {
		return 0, 0, false
	}
	return math.Float64frombits(binary.BigEndian.Uint64(b[0:8])), int64(binary.BigEndian.Uint64(b[8:16])), true
}

// --- default storage: in-memory ---------------------------------------------

// memoryRateLimitStore is the default RateLimitStore: a mutex-guarded map with
// lazy TTL (expired entries are treated as absent and dropped on access, so
// there is no background goroutine and no lifecycle to manage). It resets when a
// reload rebuilds the Proxy, like the round-robin counter and health state.
type memoryRateLimitStore struct {
	mu sync.Mutex
	m  map[string]rlEntry
}

type rlEntry struct {
	state []byte
	exp   time.Time
}

// NewMemoryRateLimitStore returns the default in-memory RateLimitStore.
func NewMemoryRateLimitStore() RateLimitStore {
	return &memoryRateLimitStore{m: make(map[string]rlEntry)}
}

func (s *memoryRateLimitStore) Get(_ context.Context, key string) ([]byte, bool, time.Time, error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[key]
	if !ok {
		return nil, false, now, nil
	}
	if now.After(e.exp) {
		delete(s.m, key)
		return nil, false, now, nil
	}
	return e.state, true, now, nil
}

func (s *memoryRateLimitStore) SetIfAbsent(_ context.Context, key string, state []byte, ttl time.Duration) (bool, error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[key]; ok && now.Before(e.exp) {
		return false, nil
	}
	s.m[key] = rlEntry{state: bytes.Clone(state), exp: now.Add(ttl)}
	return true, nil
}

func (s *memoryRateLimitStore) CompareAndSwap(_ context.Context, key string, oldState, newState []byte, ttl time.Duration) (bool, error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[key]
	if !ok || now.After(e.exp) {
		return false, nil // absent/expired ⇒ CAS fails; caller falls back to SetIfAbsent
	}
	if !bytes.Equal(e.state, oldState) {
		return false, nil
	}
	s.m[key] = rlEntry{state: bytes.Clone(newState), exp: now.Add(ttl)}
	return true, nil
}

// --- compiled rule -----------------------------------------------------------

// rateLimitRule is a compiled rate-limit tier (global or one location). The
// header mode and reject responder come straight from config; rate/burst/key are
// data passed to the pluggable RateLimiter.
type rateLimitRule struct {
	id      string              // bucket-key namespace ("global" or a location path)
	keyDims []string            // resolved key dimensions
	rate    float64             // tokens per second (config rate/period)
	burst   int                 // bucket capacity
	methods map[string]struct{} // nil = all methods
	headers string              // "off" | "on-reject" | "always"
	resp    *TemplateResponder  // the 429 reject response
}

// applies reports whether this rule governs the given method.
func (r *rateLimitRule) applies(method string) bool {
	if len(r.methods) == 0 {
		return true
	}
	_, ok := r.methods[strings.ToUpper(method)]
	return ok
}

// keyFor builds the bucket key from the rule's id and resolved key dimensions.
func (r *rateLimitRule) keyFor(req Request) string {
	parts := make([]string, 0, len(r.keyDims)+1)
	parts = append(parts, r.id)
	for _, d := range r.keyDims {
		switch {
		case d == "ip":
			parts = append(parts, hostPart(req.RemoteAddr))
		case d == "method":
			parts = append(parts, req.Method)
		case d == "path":
			parts = append(parts, req.Path)
		default: // header:<NAME>
			if name, ok := strings.CutPrefix(d, "header:"); ok {
				parts = append(parts, req.Header.Get(name))
			}
		}
	}
	return strings.Join(parts, "|")
}

// allow evaluates the rule for a request. When the rule does not apply to the
// method it permits with a zero Limit (so the caller emits no RateLimit-*
// headers). A store error fails open (allows) to favor availability.
func (r *rateLimitRule) allow(ctx context.Context, limiter RateLimiter, store RateLimitStore, req Request) (Allowance, bool) {
	if !r.applies(req.Method) {
		return Allowance{OK: true}, true
	}
	a, err := limiter.Allow(ctx, store, r.keyFor(req), r.rate, r.burst)
	if err != nil {
		log.Printf("switchyard: rate-limit store error (%s): %v; allowing", r.id, err)
		return Allowance{OK: true}, true
	}
	return a, a.OK
}

// newRateLimitRule compiles a RateLimitConfig into a rule, or returns nil when
// the config is absent or disabled (rate <= 0). id namespaces the bucket keys.
func newRateLimitRule(id string, rc *RateLimitConfig) (*rateLimitRule, error) {
	if rc == nil {
		return nil, nil
	}
	rate := ptrInt(rc.Rate, 0)
	if rate <= 0 {
		return nil, nil // disabled
	}
	period := ptrInt(rc.Period, defaultRateLimitPeriod)
	if period <= 0 {
		period = defaultRateLimitPeriod
	}
	burst := ptrInt(rc.Burst, rate)
	if burst <= 0 {
		burst = rate
	}
	dims := rc.Key
	if len(dims) == 0 {
		dims = defaultRateLimitKey
	}
	methods, err := methodSet(rc.Methods)
	if err != nil {
		return nil, err
	}
	headers := rc.Headers
	if headers == "" {
		headers = defaultRateLimitHeaders
	}
	body := ""
	if rc.Body != nil {
		body = *rc.Body
	}
	resp, err := newResponder(ResponseConfig{Status: rc.Status, Headers: rc.ResponseHeaders, Body: body}, defaultRateLimitStatus, defaultRateLimitBody)
	if err != nil {
		return nil, err
	}
	return &rateLimitRule{
		id:      id,
		keyDims: dims,
		rate:    float64(rate) / float64(period),
		burst:   burst,
		methods: methods,
		headers: headers,
		resp:    resp,
	}, nil
}
