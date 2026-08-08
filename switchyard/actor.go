package switchyard

import (
	"context"
	"net/http"
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
}

// DefaultActor is the built-in Actor. On a forward it enforces the location and
// backend connection caps (per the overflow policy), applies stacked headers
// and the per-request timeout, then proxies. It turns a routing reject into a
// consistent HTTP error, and an over-capacity condition into the configured
// overflow response.
type DefaultActor struct {
	env      actorEnv
	overflow overflowPolicy
}

// Act carries out the decision.
func (a *DefaultActor) Act(w http.ResponseWriter, r *http.Request, req Request, d Decision) {
	switch d.Action {
	case ActionForward:
		// Acquire the location slot (no alternates), then a backend slot
		// (which may reroute to another pool member) — independent nested caps.
		var loc *limiter
		if d.Location != nil {
			loc = d.Location.lim
		}
		if !a.overflow.acquire(r.Context(), loc) {
			a.overflow.reject(w)
			return
		}
		b := a.chooseBackend(r.Context(), d)
		if b == nil {
			loc.release()
			a.overflow.reject(w)
			return
		}
		defer func() {
			b.lim.release()
			loc.release()
		}()

		// Reflect the backend actually used (may differ from d.Backend after a
		// reroute) so logging reports the right backend_id.
		if rec, ok := r.Context().Value(recordKey).(*LogRecord); ok {
			rec.Backend = b
		}
		a.env.applyStackedHeaders(req, r, d.Location)
		if rt := b.requestTimeout; rt > 0 {
			ctx, cancel := context.WithTimeout(r.Context(), rt)
			defer cancel()
			r = r.WithContext(ctx)
		}
		b.proxy.ServeHTTP(w, r)
	case ActionStatic:
		a.env.applyStackedHeaders(req, r, d.Location)
		d.Location.Static.Serve(w, r, req)
	default: // ActionReject (routing decision, not capacity)
		status := d.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		http.Error(w, "switchyard: "+d.Reason, status)
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
