package switchyard

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// Backend is a configured upstream that Switchyard can forward requests to.
// ID and URL are exported so custom BackendSelectors and Loggers can inspect
// them. MaxConns/InFlight expose this backend's capacity so a custom selector
// can distribute by load; proxy/transport/lim are internal.
type Backend struct {
	ID  string
	URL string

	proxy          *httputil.ReverseProxy
	transport      *http.Transport
	lim            *limiter            // concurrency cap (nil = unlimited)
	requestTimeout time.Duration       // per-request upstream deadline (0 = none)
	methods        map[string]struct{} // accepted HTTP methods, upper-cased (nil/empty = all)
	healthy        atomic.Bool         // liveness flag; retry's skip_unhealthy excludes false
}

// Healthy reports whether this backend is currently considered healthy. New
// backends start healthy. The retry stage's skip_unhealthy excludes unhealthy
// backends from selection.
func (b *Backend) Healthy() bool { return b.healthy.Load() }

// SetHealthy sets this backend's health flag. It is the hook a health checker
// (config-driven or an SDK-supplied prober) toggles; the flag is read live by the
// retry stage. Safe for concurrent use.
func (b *Backend) SetHealthy(v bool) { b.healthy.Store(v) }

// Accepts reports whether this backend serves the given HTTP method. A backend
// with no configured methods accepts every method. Matching is
// case-insensitive. Exported so custom selectors/deciders can be method-aware.
func (b *Backend) Accepts(method string) bool {
	if len(b.methods) == 0 {
		return true
	}
	_, ok := b.methods[strings.ToUpper(method)]
	return ok
}

// MaxConns is this backend's configured max concurrent in-flight requests
// (0 = unlimited). Useful for capacity-aware custom BackendSelectors.
func (b *Backend) MaxConns() int { return b.lim.capacity() }

// InFlight is the number of requests currently being served by this backend.
func (b *Backend) InFlight() int { return b.lim.count() }

// buildBackends constructs a reverse proxy for each configured backend, merging
// per-backend settings over the project defaults. It validates that every url
// includes a scheme and host and that urls and ids are each unique. An
// unspecified id defaults to the url. Each backend gets its own tuned transport
// (installed onto the ReverseProxy by New via proxyTransport). Validation fails
// fast so misconfiguration surfaces at startup.
func buildBackends(cfg Config) ([]*Backend, error) {
	var backends []*Backend
	seenURL := make(map[string]bool)
	seenID := make(map[string]bool)
	for _, bc := range cfg.Backends {
		raw := bc.URL
		if raw == "" {
			return nil, fmt.Errorf("backend must include a url")
		}
		target, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("parse backend %q: %w", raw, err)
		}
		if target.Scheme == "" || target.Host == "" {
			return nil, fmt.Errorf("backend %q must include scheme and host", raw)
		}
		if seenURL[raw] {
			return nil, fmt.Errorf("duplicate backend url %q", raw)
		}
		seenURL[raw] = true

		// An unspecified id defaults to the backend's url.
		id := bc.ID
		if id == "" {
			id = raw
		}
		if seenID[id] {
			return nil, fmt.Errorf("duplicate backend id %q", id)
		}
		seenID[id] = true

		methods, err := methodSet(bc.Methods)
		if err != nil {
			return nil, fmt.Errorf("backend %q: %w", raw, err)
		}
		s := cfg.resolveBackend(bc)
		b := &Backend{
			ID:             id,
			URL:            raw,
			proxy:          httputil.NewSingleHostReverseProxy(target),
			transport:      buildTransport(s),
			lim:            newLimiter(s.maxConns),
			requestTimeout: s.requestTimeout,
			methods:        methods,
		}
		b.healthy.Store(true) // backends start healthy
		// The ErrorHandler for backend failures (unreachable host, reset
		// connection, etc.) is installed by New once the Proxy exists, so it can
		// route through the configurable p.BadGateway responder.
		backends = append(backends, b)
	}
	return backends, nil
}

// methodSet normalizes a configured methods list into an upper-cased set. It
// returns nil for an empty list (meaning "accept all") and fails fast on a
// blank entry.
func methodSet(methods []string) (map[string]struct{}, error) {
	if len(methods) == 0 {
		return nil, nil
	}
	set := make(map[string]struct{}, len(methods))
	for _, m := range methods {
		m = strings.ToUpper(strings.TrimSpace(m))
		if m == "" {
			return nil, fmt.Errorf("methods: entries must not be empty")
		}
		set[m] = struct{}{}
	}
	return set, nil
}
