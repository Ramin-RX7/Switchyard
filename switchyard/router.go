package switchyard

// Router selects which location (if any) handles a request. It is the pluggable
// "location detection" stage. The default is DefaultRouter (first-match over the
// configured locations); an SDK user may supply their own — e.g. host-based or
// header-based routing — by assigning Proxy.Router.
//
// Match returns the chosen location, or nil when none applies. It must stay
// passive: no I/O, matching only.
type Router interface {
	Match(req Request) *Location
}

// DefaultRouter is the built-in Router: it returns the first location whose
// path matches, in slice order, matching Switchyard's config semantics.
type DefaultRouter struct {
	Locations []*Location
}

// Match returns the first matching location, or nil if none match.
func (r *DefaultRouter) Match(req Request) *Location {
	for _, loc := range r.Locations {
		if loc.Matches(req.Path) {
			return loc
		}
	}
	return nil
}
