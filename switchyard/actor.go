package switchyard

import (
	"context"
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
			a.overflow.reject(w, r, req)
			return
		}
		b := a.chooseBackend(r.Context(), d)
		if b == nil {
			loc.release()
			a.overflow.reject(w, r, req)
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
