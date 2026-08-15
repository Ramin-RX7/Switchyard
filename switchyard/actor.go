package switchyard

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"strings"
)

// Actor performs the side effect a Decision calls for: forward to a backend,
// serve a static file, or reject. It is the pluggable "act" stage and the only
// place with side effects. The default is DefaultActor; an SDK user may replace
// or wrap it (e.g. to add retries, response rewriting, or metrics).
type Actor interface {
	Act(w http.ResponseWriter, r *http.Request, req Request, d Decision)
}

// actorEnv is the narrow slice of the Proxy that DefaultActor depends on.
// *Proxy implements it: applying global then location headers (reading p.Headers
// live so overrides late-bind), and exposing the pool a forward may reroute
// within.
type actorEnv interface {
	applyStackedHeaders(req Request, r *http.Request, loc *Location)
	forwardPool(d Decision) []*Backend
	forwardSelector(d Decision) BackendSelector
	// notFoundResponder / badGatewayResponder / methodNotAllowedResponder /
	// forbiddenResponder are the generators for routing rejects (404 no-match;
	// 502 empty pool / no backend; 405 no backend accepts the method; 403 access
	// denied). Read live so SDK overrides of the corresponding Proxy fields take
	// effect.
	notFoundResponder() ResponseGenerator
	badGatewayResponder() ResponseGenerator
	methodNotAllowedResponder() ResponseGenerator
	forbiddenResponder() ResponseGenerator
}

// DefaultActor is the built-in Actor. On a forward it enforces the location and
// backend connection caps (per the overflow policy), applies stacked headers
// and the per-request timeout, then proxies. It turns a routing reject into a
// consistent HTTP error, and an over-capacity condition into the configured
// overflow response.
type DefaultActor struct {
	env      actorEnv
	overflow overflowPolicy
	retry    retryPolicy // global retry policy; a matched location may carry its own
}

// Act carries out the decision.
func (a *DefaultActor) Act(w http.ResponseWriter, r *http.Request, req Request, d Decision) {
	switch d.Action {
	case ActionForward:
		a.forward(w, r, req, d)
	case ActionStatic:
		a.env.applyStackedHeaders(req, r, d.Location)
		d.Location.Static.Serve(w, r, req)
	case ActionRespond:
		// A generated response: no upstream, so set_headers (which mutates the
		// forwarded request) does not apply — the response's own headers come
		// from its configured response block.
		d.Location.Responder.Generate(w, r, req)
	default: // ActionReject (routing decision, not capacity)
		// Built-in scenarios flow through the configurable error responders:
		// 404 for no-match; 502 (or unset) for empty pool / no backend. A custom
		// Decider that chooses any other status has it honored directly.
		switch d.Status {
		case http.StatusNotFound:
			a.env.notFoundResponder().Generate(w, r, req)
		case http.StatusMethodNotAllowed:
			if len(d.AllowedMethods) > 0 {
				w.Header().Set("Allow", strings.Join(d.AllowedMethods, ", "))
			}
			a.env.methodNotAllowedResponder().Generate(w, r, req)
		case http.StatusForbidden:
			a.env.forbiddenResponder().Generate(w, r, req)
		case 0, http.StatusBadGateway:
			a.env.badGatewayResponder().Generate(w, r, req)
		default:
			http.Error(w, "switchyard: "+d.Reason, d.Status)
		}
	}
}

// chooseBackend acquires a backend slot for the forward, honoring the overflow
// policy. It first tries the decided backend; with "reroute" it then tries the
// other pool members; finally it falls back to waiting on the decided backend
// for the policy's queue window. Returns the backend whose slot is now held, or
// nil if none could be acquired (the caller rejects). The returned backend's
// slot must be released by the caller.
func (a *DefaultActor) chooseBackend(ctx context.Context, d Decision) *Backend {
	if d.Backend.lim.tryAcquire() {
		return d.Backend
	}
	if a.overflow.reroutes() {
		for _, b := range a.env.forwardPool(d) {
			if b == d.Backend {
				continue
			}
			if b.lim.tryAcquire() {
				return b
			}
		}
	}
	if wait := a.overflow.fallbackWait(); wait > 0 {
		if d.Backend.lim.acquire(ctx, wait) {
			return d.Backend
		}
	}
	return nil
}

// forward carries out an ActionForward. It acquires the location slot once for
// the whole request (independent of any per-attempt backend slots), then takes
// either the single-shot fast path (retry disabled for this scope) or the retry
// loop.
func (a *DefaultActor) forward(w http.ResponseWriter, r *http.Request, req Request, d Decision) {
	var loc *limiter
	if d.Location != nil {
		loc = d.Location.lim
	}
	if !a.overflow.acquire(r.Context(), loc) {
		a.overflow.reject(w, r, req)
		return
	}
	defer loc.release()

	if pol := a.retryFor(d); pol.enabled() {
		a.forwardRetry(w, r, req, d, pol)
		return
	}
	a.forwardOnce(w, r, req, d)
}

// retryFor returns the retry policy governing this decision: the matched
// location's, or the global one when no location applied.
func (a *DefaultActor) retryFor(d Decision) retryPolicy {
	if d.Location != nil {
		return d.Location.retry
	}
	return a.retry
}

// forwardOnce is the single-shot forward (retry disabled). It preserves the
// original behavior: acquire one backend slot (with overflow reroute/queue),
// apply headers, honor the per-request timeout, and proxy. On a backend failure
// the ReverseProxy ErrorHandler renders the BadGateway response directly.
func (a *DefaultActor) forwardOnce(w http.ResponseWriter, r *http.Request, req Request, d Decision) {
	b := a.chooseBackend(r.Context(), d)
	if b == nil {
		a.overflow.reject(w, r, req)
		return
	}
	defer b.lim.release()

	// Reflect the backend actually used (may differ from d.Backend after a
	// reroute) so logging reports the right backend_id.
	setRecordBackend(r.Context(), b)
	a.env.applyStackedHeaders(req, r, d.Location)
	if rt := b.requestTimeout; rt > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), rt)
		defer cancel()
		r = r.WithContext(ctx)
	}
	b.proxy.ServeHTTP(w, r)
}

// forwardRetry runs the retry loop: up to pol.attempts retries beyond the first
// try. Each attempt selects a backend (respecting retry_same_backend and
// skip_unhealthy), acquires its slot, and proxies. The per-request retryState —
// read by the ReverseProxy ModifyResponse/ErrorHandler hooks — reports each
// attempt's outcome; this loop owns every client write. Retries only ever occur
// before any byte reaches the client, so streaming and upgrades are unaffected.
func (a *DefaultActor) forwardRetry(w http.ResponseWriter, r *http.Request, req Request, d Decision, pol retryPolicy) {
	pool := a.env.forwardPool(d)
	attempts := pol.attempts
	if !prepareRetryBody(r, pol) {
		attempts = 0 // body too large to replay → single attempt (custom exhaustion still applies)
	}
	a.env.applyStackedHeaders(req, r, d.Location)

	st := &retryState{
		onStatus:       pol.onStatus,
		statusEligible: pol.statusEligible(req.Method),
		onConnError:    pol.onConnError,
		hasCustomResp:  pol.resp != nil,
	}
	baseCtx := context.WithValue(r.Context(), retryKey, st)

	tried := map[*Backend]bool{}
	for attempt := 0; attempt <= attempts; attempt++ {
		if attempt > 0 {
			if !sleepBackoff(baseCtx, pol.backoff, attempt) {
				// ctx cancelled during backoff (client gone / force reload).
				a.renderExhausted(w, r, req, d, pol)
				return
			}
		}
		cands := pol.candidates(pool, tried)
		if len(cands) == 0 {
			break // no eligible backend remains
		}
		b := a.acquireForRetry(baseCtx, d, req, cands, attempt)
		if b == nil {
			a.overflow.reject(w, r, req) // capacity exhausted for this attempt
			return
		}

		st.canRetry = attempt < attempts && pol.hasNextCandidate(pool, tried, b)
		st.outcome = outcomeCommitted
		st.lastErr = nil
		st.connError = false
		st.lastStatus = 0

		setRecordBackend(r.Context(), b)
		ctx := baseCtx
		var cancel context.CancelFunc
		if rt := b.requestTimeout; rt > 0 {
			ctx, cancel = context.WithTimeout(baseCtx, rt)
		}
		rr := r.WithContext(ctx)
		if r.GetBody != nil {
			if body, err := r.GetBody(); err == nil {
				rr.Body = body
			}
		}
		b.proxy.ServeHTTP(w, rr)
		if cancel != nil {
			cancel()
		}
		b.lim.release()

		switch st.outcome {
		case outcomeCommitted:
			return
		case outcomeTerminalReload:
			http.Error(w, "switchyard: reloading", http.StatusServiceUnavailable)
			return
		case outcomeTerminal:
			a.renderExhausted(w, r, req, d, pol)
			return
		case outcomeRetry:
			tried[b] = true
			bumpRecordRetries(r.Context())
			logRetry(b, attempt, st)
		}
	}
	// Fell out of the loop: no further eligible backend. Render the exhaustion
	// response (a status passthrough would already have committed above).
	a.renderExhausted(w, r, req, d, pol)
}

// acquireForRetry selects a backend from cands and acquires its slot, honoring
// the overflow capacity policy. Attempt 0 prefers the Decider's backend when it
// is still eligible; later attempts advance the location's selector (round-robin
// by default). Returns nil when capacity is exhausted (the caller overflow-rejects).
func (a *DefaultActor) acquireForRetry(ctx context.Context, d Decision, req Request, cands []*Backend, attempt int) *Backend {
	var prefer *Backend
	if attempt == 0 && containsBackend(cands, d.Backend) {
		prefer = d.Backend
	} else {
		prefer = a.env.forwardSelector(d).Select(cands, req)
	}
	if prefer != nil && prefer.lim.tryAcquire() {
		return prefer
	}
	if a.overflow.reroutes() {
		for _, b := range cands {
			if b == prefer {
				continue
			}
			if b.lim.tryAcquire() {
				return b
			}
		}
	}
	if wait := a.overflow.fallbackWait(); wait > 0 && prefer != nil {
		if prefer.lim.acquire(ctx, wait) {
			return prefer
		}
	}
	return nil
}

// renderExhausted writes the client response when retries are exhausted: the
// configured retry.response if any, otherwise the BadGateway (502) responder.
// (A status-based exhaustion with no custom response never reaches here — that
// response is passed through to the client by ModifyResponse.)
func (a *DefaultActor) renderExhausted(w http.ResponseWriter, r *http.Request, req Request, d Decision, pol retryPolicy) {
	if pol.resp != nil {
		pol.resp.Generate(w, r, req)
		return
	}
	a.env.badGatewayResponder().Generate(w, r, req)
}

// prepareRetryBody buffers the request body once so it can be replayed on each
// attempt (setting r.GetBody). It returns false — meaning "retry disabled for
// this request" — when the body exceeds pol.maxBodyBytes or cannot be read; the
// body is left intact for a single forward either way.
func prepareRetryBody(r *http.Request, pol retryPolicy) bool {
	if r.Body == nil || r.Body == http.NoBody {
		return true
	}
	if r.GetBody != nil {
		return true // already replayable
	}
	limit := pol.maxBodyBytes
	buf, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(buf))
		return false
	}
	if len(buf) > limit {
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(buf), r.Body))
		return false
	}
	r.Body.Close()
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(buf)), nil
	}
	r.Body, _ = r.GetBody()
	return true
}

// logRetry logs a failed attempt and the intent to retry. attempt is 0-based, so
// the human-facing attempt number is attempt+1.
func logRetry(b *Backend, attempt int, st *retryState) {
	if st.connError {
		log.Printf("switchyard: backend %s failed on attempt %d: %v; retrying", b.URL, attempt+1, st.lastErr)
		return
	}
	log.Printf("switchyard: backend %s returned %d on attempt %d; retrying", b.URL, st.lastStatus, attempt+1)
}

// setRecordBackend records the backend actually used on the LogRecord (if any),
// so logging reports the right backend_id after a reroute/retry.
func setRecordBackend(ctx context.Context, b *Backend) {
	if rec, ok := ctx.Value(recordKey).(*LogRecord); ok {
		rec.Backend = b
	}
}

// bumpRecordRetries increments the retry counter on the LogRecord (if any).
func bumpRecordRetries(ctx context.Context) {
	if rec, ok := ctx.Value(recordKey).(*LogRecord); ok {
		rec.Retries++
	}
}

// containsBackend reports whether b is in pool.
func containsBackend(pool []*Backend, b *Backend) bool {
	for _, p := range pool {
		if p == b {
			return true
		}
	}
	return false
}
