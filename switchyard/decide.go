package switchyard

import "net/http"

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
		b := loc.Selector.Select(loc.Pool.Backends(), req)
		if b == nil {
			return Decision{Action: ActionReject, Reason: "location " + loc.raw + ": empty pool",
				Location: loc, Status: http.StatusBadGateway}
		}
		return Decision{Action: ActionForward, Reason: "round-robin", Backend: b, Location: loc}
	}

	pool := e.globalPool()
	if len(pool) == 0 {
		return Decision{Action: ActionReject, Reason: "no backends configured"}
	}
	b := e.globalSelector().Select(pool, req)
	if b == nil {
		return Decision{Action: ActionReject, Reason: "no backend available"}
	}
	return Decision{Action: ActionForward, Reason: "round-robin", Backend: b}
}
