package switchyard

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// Backend is a configured upstream that Switchyard can forward requests to.
// ID and URL are exported so custom BackendSelectors and Loggers can inspect
// them; proxy is the internal reverse-proxy handler.
type Backend struct {
	ID    string
	URL   string
	proxy *httputil.ReverseProxy
}

// buildBackends constructs a reverse proxy for each configured backend,
// validating that every url includes a scheme and host and that urls and ids
// are each unique. An unspecified id defaults to the url. When logging is true,
// each backend's transport is wrapped so per-request round-trip timing and
// status can be recorded. Validation fails fast so misconfiguration surfaces at
// startup.
func buildBackends(cfgs []BackendConfig, logging bool) ([]*Backend, error) {
	var backends []*Backend
	seenURL := make(map[string]bool)
	seenID := make(map[string]bool)
	for _, bc := range cfgs {
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

		b := &Backend{
			ID:    id,
			URL:   raw,
			proxy: httputil.NewSingleHostReverseProxy(target),
		}
		// When any logging is enabled, wrap the transport so the backend
		// round-trip timing and status code can be recorded per request.
		if logging {
			b.proxy.Transport = &loggingTransport{base: http.DefaultTransport}
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
