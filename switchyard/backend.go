package switchyard

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
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
	lim            *limiter      // concurrency cap (nil = unlimited)
	requestTimeout time.Duration // per-request upstream deadline (0 = none)
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

		s := cfg.resolveBackend(bc)
		b := &Backend{
			ID:             id,
			URL:            raw,
			proxy:          httputil.NewSingleHostReverseProxy(target),
			transport:      buildTransport(s),
			lim:            newLimiter(s.maxConns),
			requestTimeout: s.requestTimeout,
		}
		// Handle backend failures (unreachable host, reset connection, etc.)
		// explicitly: log them in Switchyard's format and return a consistent
		// 502 instead of relying on the default handler.
		b.proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("switchyard: backend %s failed: %v", b.URL, err)
			http.Error(w, "switchyard: backend unavailable", http.StatusBadGateway)
		}
		backends = append(backends, b)
	}
	return backends, nil
}
