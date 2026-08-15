package switchyard

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"time"
)

// retryPolicy is the resolved (merged) retry configuration for a scope (global or
// a location). It is built once at New/compile time by resolveRetry and read live
// during handling. The zero value is a disabled policy.
type retryPolicy struct {
	attempts           int
	onConnError        bool
	onStatus           map[int]bool
	retryNonIdempotent bool
	retrySameBackend   bool
	skipUnhealthy      bool
	maxBodyBytes       int
	backoff            backoffPolicy
	resp               *TemplateResponder // nil = default exhaustion behavior
}

// enabled reports whether this policy can ever retry: at least one attempt and at
// least one trigger (connection error or a status list).
func (p retryPolicy) enabled() bool {
	return p.attempts > 0 && (p.onConnError || len(p.onStatus) > 0)
}

// statusEligible reports whether a status-based retry may fire for method. Only
// idempotent methods qualify unless retry_non_idempotent is set.
func (p retryPolicy) statusEligible(method string) bool {
	if p.retryNonIdempotent {
		return true
	}
	return idempotentMethods[strings.ToUpper(method)]
}

// idempotentMethods are the HTTP methods safe to replay on a status-based retry.
var idempotentMethods = map[string]bool{
	"GET": true, "HEAD": true, "PUT": true, "DELETE": true, "OPTIONS": true, "TRACE": true,
}

// candidates returns the backends eligible for selection on this attempt: the
// pool minus already-tried backends (when retry_same_backend is false) and minus
// unhealthy backends (when skip_unhealthy is set). The tried filter is a hard
// constraint (drives exhaustion); the health filter is best-effort — if it would
// leave no candidate, the still-untried (unhealthy) backends are returned rather
// than blackholing the request.
func (p retryPolicy) candidates(pool []*Backend, tried map[*Backend]bool) []*Backend {
	base := pool
	if !p.retrySameBackend && len(tried) > 0 {
		base = make([]*Backend, 0, len(pool))
		for _, b := range pool {
			if !tried[b] {
				base = append(base, b)
			}
		}
	}
	if !p.skipUnhealthy {
		return base
	}
	healthy := make([]*Backend, 0, len(base))
	for _, b := range base {
		if b.Healthy() {
			healthy = append(healthy, b)
		}
	}
	if len(healthy) > 0 {
		return healthy
	}
	return base // all unhealthy → best-effort fallback
}

// hasNextCandidate reports whether a further attempt could select a backend that
// is not current and, when retry_same_backend is false, not already tried. Health
// is ignored here (it only affects which backend, not whether a retry is possible).
func (p retryPolicy) hasNextCandidate(pool []*Backend, tried map[*Backend]bool, current *Backend) bool {
	if p.retrySameBackend {
		return true
	}
	for _, b := range pool {
		if b == current || tried[b] {
			continue
		}
		return true
	}
	return false
}

// backoffPolicy is the resolved inter-attempt delay policy.
type backoffPolicy struct {
	strategy string // "none" | "constant" | "exponential"
	base     time.Duration
	max      time.Duration
	jitter   bool
}

// delay is the wait before retry number n (n = 1 for the first retry). With
// jitter, the computed delay d is replaced by a uniform random value in [0, d].
func (b backoffPolicy) delay(n int) time.Duration {
	var d time.Duration
	switch b.strategy {
	case "none":
		return 0
	case "constant":
		d = b.base
	default: // "exponential"
		d = b.base
		for i := 1; i < n; i++ {
			if b.max > 0 && d >= b.max {
				break
			}
			d *= 2
		}
		if b.max > 0 && d > b.max {
			d = b.max
		}
	}
	if b.jitter && d > 0 {
		d = time.Duration(rand.Int64N(int64(d) + 1))
	}
	return d
}

// --- per-request state shared with the ReverseProxy hooks -------------------

// retryOutcome is what the ModifyResponse/ErrorHandler hooks concluded about the
// attempt just made. The Actor retry loop owns every client write for a
// retry-active request and switches on this value.
type retryOutcome int

const (
	outcomeCommitted      retryOutcome = iota // ReverseProxy wrote the real response (success or passthrough)
	outcomeRetry                              // retryable failure; loop should try the next backend
	outcomeTerminal                           // give up; loop renders the exhaustion response
	outcomeTerminalReload                     // aborted by a force reload; loop writes 503
)

// retryState is threaded through the request context so the per-backend
// ModifyResponse/ErrorHandler hooks can decide (and report) the outcome of an
// attempt. It is only ever touched on the request goroutine (the hooks run
// synchronously inside ServeHTTP), so it needs no synchronization.
type retryState struct {
	// inputs (set by the loop before each attempt)
	onStatus       map[int]bool
	statusEligible bool
	onConnError    bool
	hasCustomResp  bool
	canRetry       bool

	// outputs (set by the hooks)
	outcome    retryOutcome
	lastStatus int
	lastErr    error
	connError  bool
}

// retryKey is the context key under which the per-request *retryState is stored
// for the ReverseProxy ModifyResponse/ErrorHandler hooks to find. It is distinct
// from recordKey (contextKey iota = 0, see logging.go).
const retryKey contextKey = 1

// Sentinel errors handed back to ReverseProxy from ModifyResponse so the response
// is discarded (nothing written to the client) and ErrorHandler runs.
var (
	errRetryStatus    = errors.New("switchyard: retryable status")
	errRetryExhausted = errors.New("switchyard: retry exhausted")
)

// onResponse is called from ModifyResponse with the upstream status. The returned
// error is handed back to ReverseProxy (nil = commit/stream the response).
func (s *retryState) onResponse(status int) error {
	if s.onStatus[status] && s.statusEligible {
		if s.canRetry {
			s.outcome = outcomeRetry
			s.lastStatus = status
			return errRetryStatus
		}
		if s.hasCustomResp {
			s.outcome = outcomeTerminal
			s.lastStatus = status
			return errRetryExhausted
		}
	}
	s.outcome = outcomeCommitted
	return nil
}

// onError is called from ErrorHandler for a transport error or one of our
// sentinels. It records the outcome; the loop performs any client write.
func (s *retryState) onError(err error) {
	switch {
	case errors.Is(err, errRetryStatus):
		s.outcome = outcomeRetry // already decided by onResponse
	case errors.Is(err, errRetryExhausted):
		s.outcome = outcomeTerminal
	default:
		s.connError = true
		s.lastErr = err
		if s.onConnError && s.canRetry {
			s.outcome = outcomeRetry
		} else {
			s.outcome = outcomeTerminal
		}
	}
}

// --- building the resolved policy from config -------------------------------

// retryPolicy builds the project-wide resolved policy from the top-level config.
func (c Config) retryPolicy() (retryPolicy, error) {
	return resolveRetry(c.Retry, nil)
}

// resolveRetry merges a location's RetryConfig (loc) over the global one (global),
// field by field: a field set on loc wins, else the global value, else the
// built-in default. It compiles the optional exhaustion responder, so a bad
// $variable in retry.response fails fast at startup.
func resolveRetry(global, loc *RetryConfig) (retryPolicy, error) {
	p := retryPolicy{
		onConnError:      true,
		retrySameBackend: true,
		skipUnhealthy:    true,
		maxBodyBytes:     defaultRetryMaxBodyBytes,
		backoff: backoffPolicy{
			strategy: "exponential",
			base:     defaultRetryBackoffBaseMs * time.Millisecond,
			max:      defaultRetryBackoffMaxMs * time.Millisecond,
			jitter:   true,
		},
	}

	var response *ResponseConfig
	apply := func(rc *RetryConfig) {
		if rc == nil {
			return
		}
		if rc.Attempts != nil {
			p.attempts = *rc.Attempts
		}
		if rc.OnConnectionError != nil {
			p.onConnError = *rc.OnConnectionError
		}
		if rc.OnStatus != nil {
			p.onStatus = statusSet(rc.OnStatus)
		}
		if rc.RetryNonIdempotent != nil {
			p.retryNonIdempotent = *rc.RetryNonIdempotent
		}
		if rc.RetrySameBackend != nil {
			p.retrySameBackend = *rc.RetrySameBackend
		}
		if rc.SkipUnhealthy != nil {
			p.skipUnhealthy = *rc.SkipUnhealthy
		}
		if rc.MaxBodyBytes != nil {
			p.maxBodyBytes = *rc.MaxBodyBytes
		}
		if b := rc.Backoff; b != nil {
			if b.Strategy != nil && *b.Strategy != "" {
				p.backoff.strategy = *b.Strategy
			}
			if b.BaseMs != nil {
				p.backoff.base = time.Duration(*b.BaseMs) * time.Millisecond
			}
			if b.MaxMs != nil {
				p.backoff.max = time.Duration(*b.MaxMs) * time.Millisecond
			}
			if b.Jitter != nil {
				p.backoff.jitter = *b.Jitter
			}
		}
		if rc.Response != nil {
			response = rc.Response
		}
	}
	apply(global)
	apply(loc)

	if response != nil {
		resp, err := newResponder(*response, defaultOverflowStatus, defaultRetryExhaustedBody)
		if err != nil {
			return retryPolicy{}, err
		}
		p.resp = resp
	}
	return p, nil
}

// statusSet turns a status list into a lookup set. An empty (but non-nil) list
// yields an empty set — meaning "no status triggers", distinct from unset.
func statusSet(codes []int) map[int]bool {
	m := make(map[int]bool, len(codes))
	for _, c := range codes {
		m[c] = true
	}
	return m
}

// sleepBackoff waits the backoff delay for retry number n, aborting early if ctx
// is cancelled (client gone or force reload). It reports whether the wait
// completed (true) rather than being cancelled (false).
func sleepBackoff(ctx context.Context, b backoffPolicy, n int) bool {
	d := b.delay(n)
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
