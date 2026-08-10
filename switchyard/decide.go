package switchyard

import (
	"net/http"
	"sort"
)

// Decider interprets a captured request and returns a Decision describing what
// should happen to it. It is the pluggable routing stage and must stay passive:
// no I/O, no forwarding — only matching and backend selection.
//
// The default is DefaultDecider. SDK users may replace it wholesale, or embed
// DefaultDecider and override Decide while delegating to the base for the cases
// they don't care about.
type Decider interface {
	Decide(req Request) Decision
}

// DefaultDecider is the built-in Decider. With locations configured it matches
// them in order (first match wins) and selects within the matched location's
// pool via that location's Selector; otherwise it selects over the global pool
// via the Proxy's Selector.
//
// It reads its inputs (locations, global pool, global selector) live from the
// routingEnv, so reassigning p.Selector/p.Router/p.Pool or a Location's Selector
// after New takes effect without rebuilding the decider.
type DefaultDecider struct {
	env routingEnv
}

// routingEnv is the narrow slice of the Proxy that DefaultDecider depends on.
// *Proxy implements it, reading its fields live so overrides late-bind.
type routingEnv interface {
	hasLocations() bool
	match(req Request) *Location
	globalPool() []*Backend
	globalSelector() BackendSelector
}

// Decide implements Decider. It performs matching and an atomic counter
// increment only — no I/O and no forwarding.
func (d *DefaultDecider) Decide(req Request) Decision {
	e := d.env
	if e.hasLocations() {
		loc := e.match(req)
		if loc == nil {
			return Decision{Action: ActionReject, Reason: "no matching location", Status: http.StatusNotFound}
		}
		if loc.Kind == KindStatic {
			return Decision{Action: ActionStatic, Reason: "location " + loc.raw, Location: loc}
		}
		if loc.Kind == KindRespond {
			return Decision{Action: ActionRespond, Reason: "location " + loc.raw, Location: loc}
		}
		full := loc.Pool.Backends()
		cands := backendsAccepting(full, req.Method)
		if len(cands) == 0 {
			if len(full) == 0 {
				return Decision{Action: ActionReject, Reason: "location " + loc.raw + ": empty pool",
					Location: loc, Status: http.StatusBadGateway}
			}
			return Decision{Action: ActionReject, Reason: "location " + loc.raw + ": method not allowed",
				Location: loc, Status: http.StatusMethodNotAllowed, AllowedMethods: allowedMethods(full)}
		}
		b := loc.Selector.Select(cands, req)
		if b == nil {
			return Decision{Action: ActionReject, Reason: "location " + loc.raw + ": empty pool",
				Location: loc, Status: http.StatusBadGateway}
		}
		return Decision{Action: ActionForward, Reason: "round-robin", Backend: b, Location: loc, Candidates: cands}
	}

	pool := e.globalPool()
	if len(pool) == 0 {
		return Decision{Action: ActionReject, Reason: "no backends configured"}
	}
	cands := backendsAccepting(pool, req.Method)
	if len(cands) == 0 {
		return Decision{Action: ActionReject, Reason: "method not allowed",
			Status: http.StatusMethodNotAllowed, AllowedMethods: allowedMethods(pool)}
	}
	b := e.globalSelector().Select(cands, req)
	if b == nil {
		return Decision{Action: ActionReject, Reason: "no backend available"}
	}
	return Decision{Action: ActionForward, Reason: "round-robin", Backend: b, Candidates: cands}
}

// backendsAccepting returns the subset of pool that accepts method, preserving
// order. When every backend accepts (the common no-methods case) it returns the
// input slice unchanged — no allocation and identical behavior to unfiltered
// routing.
func backendsAccepting(pool []*Backend, method string) []*Backend {
	for i, b := range pool {
		if b.Accepts(method) {
			continue
		}
		// First rejecter: build a filtered copy from what we've already scanned.
		filtered := make([]*Backend, 0, len(pool))
		filtered = append(filtered, pool[:i]...)
		for _, b2 := range pool[i+1:] {
			if b2.Accepts(method) {
				filtered = append(filtered, b2)
			}
		}
		return filtered
	}
	return pool
}

// allowedMethods is the sorted union of the methods the pool's backends accept.
// Used to set the Allow header on a 405. In the 405 path every backend is
// method-constrained, so the union is finite.
func allowedMethods(pool []*Backend) []string {
	set := map[string]struct{}{}
	for _, b := range pool {
		for m := range b.methods {
			set[m] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
