package switchyard

import "net/http"

// Actor performs the side effect a Decision calls for: forward to a backend,
// serve a static file, or reject. It is the pluggable "act" stage and the only
// place with side effects. The default is DefaultActor; an SDK user may replace
// or wrap it (e.g. to add retries, response rewriting, or metrics).
type Actor interface {
	Act(w http.ResponseWriter, r *http.Request, req Request, d Decision)
}

// DefaultActor is the built-in Actor. It applies stacked headers before
// forwarding or serving, and turns a reject into a consistent HTTP error.
type DefaultActor struct {
	p *Proxy
}

// Act carries out the decision.
func (a *DefaultActor) Act(w http.ResponseWriter, r *http.Request, req Request, d Decision) {
	switch d.Action {
	case ActionForward:
		a.p.applyStackedHeaders(req, r, d.Location)
		d.Backend.proxy.ServeHTTP(w, r)
	case ActionStatic:
		a.p.applyStackedHeaders(req, r, d.Location)
		d.Location.Static.Serve(w, r, req)
	default: // ActionReject
		status := d.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		http.Error(w, "switchyard: "+d.Reason, status)
	}
}
