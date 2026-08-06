package switchyard

// BackendPool provides the set of backends a request may be forwarded to. It is
// the pluggable "backend list" stage. The default is StaticPool (the backends
// from config), but an SDK user may supply their own — e.g. a health-checked
// pool that omits unreachable backends, or one fed by service discovery.
//
// Backends is called on every routed request, so implementations should be
// cheap and safe for concurrent use.
type BackendPool interface {
	Backends() []*Backend
}

// StaticPool is the default BackendPool: a fixed list built once from config.
type StaticPool struct {
	list []*Backend
}

// NewStaticPool returns a BackendPool over a fixed slice of backends.
func NewStaticPool(backends []*Backend) *StaticPool {
	return &StaticPool{list: backends}
}

// Backends returns the fixed backend list.
func (p *StaticPool) Backends() []*Backend { return p.list }
