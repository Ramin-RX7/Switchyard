package switchyard

import (
	"net"
	"net/http"
	"time"
)

// buildTransport builds a backend's HTTP transport from its resolved settings.
// It mirrors http.DefaultTransport but with Switchyard's tuned idle-connection
// limits and the configured TLS-handshake timeout / keep-alive toggle. The
// stdlib default of MaxIdleConnsPerHost = 2 throttles a proxy under load, which
// is why these are raised.
func buildTransport(s backendSettings) *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: defaultDialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          s.maxIdleConns,
		MaxIdleConnsPerHost:   s.maxIdleConnsPerHost,
		IdleConnTimeout:       s.idleConnTimeout,
		TLSHandshakeTimeout:   s.tlsHandshake,
		ExpectContinueTimeout: 1 * time.Second,
		DisableKeepAlives:     s.disableKeepAlive,
	}
}

// proxyTransport is installed on every backend's ReverseProxy. It delegates to
// the backend's own transport (base) — or, if an SDK user set Proxy.Transport,
// that global override wins for all backends. It also records backend
// round-trip timing/status into the LogRecord on the request context (a no-op
// when no logging is active). Safe for concurrent use: it only reads pointers
// and writes to the per-request LogRecord owned by the calling goroutine.
type proxyTransport struct {
	p    *Proxy
	base http.RoundTripper
}

func (t *proxyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt := t.base
	if t.p != nil && t.p.Transport != nil {
		rt = t.p.Transport // global SDK override
	}
	if rt == nil {
		rt = http.DefaultTransport
	}
	rec, _ := r.Context().Value(recordKey).(*LogRecord)
	if rec != nil {
		rec.ForwardTime = time.Now()
	}
	resp, err := rt.RoundTrip(r)
	if rec != nil {
		rec.AppRespTime = time.Now()
		if resp != nil {
			rec.AppStatus = resp.StatusCode
		}
	}
	return resp, err
}
