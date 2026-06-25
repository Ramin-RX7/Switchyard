package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"
)

// backend is a configured upstream that Switchyard can forward requests to.
type backend struct {
	id    string
	url   string
	proxy *httputil.ReverseProxy
}

// Proxy forwards incoming requests to configured backends.
type Proxy struct {
	backends []*backend
	logger   *Logger // nil when no custom logging is configured
	next     atomic.Uint64
}

// newProxy builds a Proxy from configuration, preparing a reverse proxy for
// each backend URL.
func newProxy(cfg Config) (*Proxy, error) {
	var logger *Logger
	if cfg.Logging != nil {
		l, err := newLogger(*cfg.Logging)
		if err != nil {
			return nil, err
		}
		logger = l
	}

	p := &Proxy{logger: logger}
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

		b := &backend{
			id:    id,
			url:   raw,
			proxy: httputil.NewSingleHostReverseProxy(target),
		}
		// When custom logging is enabled, wrap the transport so the backend
		// round-trip timing and status code can be recorded per request.
		if logger != nil {
			b.proxy.Transport = &loggingTransport{base: http.DefaultTransport}
		}
		// Handle backend failures (unreachable host, reset connection, etc.)
		// explicitly: log them in Switchyard's format and return a consistent
		// 502 instead of relying on the default handler.
		b.proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("switchyard: backend %s failed: %v", b.url, err)
			http.Error(w, "switchyard: backend unavailable", http.StatusBadGateway)
		}
		p.backends = append(p.backends, b)
	}
	return p, nil
}

// handle is the HTTP entry point: capture the request into the internal form,
// decide what to do with it, then handle it.
func (p *Proxy) handle(w http.ResponseWriter, r *http.Request) {
	req := captureRequest(r)
	d := p.decide(req)
	p.handleRequest(w, r, req, d)
}

// decide selects a backend for the request using a deterministic round-robin
// rule. It is passive: it performs no I/O and triggers no forwarding.
func (p *Proxy) decide(req Request) Decision {
	if len(p.backends) == 0 {
		return Decision{Action: "reject", Reason: "no backends configured"}
	}
	i := p.next.Add(1) - 1
	b := p.backends[int(i%uint64(len(p.backends)))]
	return Decision{Action: "forward", Reason: "round-robin", Backend: b}
}

// handleRequest acts on the decision: it forwards the request to the selected
// backend, or reports that no backend is available.
func (p *Proxy) handleRequest(w http.ResponseWriter, r *http.Request, req Request, d Decision) {
	if p.logger == nil {
		log.Printf("switchyard: %s -> %s", req, d)
		if d.Backend == nil {
			http.Error(w, "switchyard: no backend available", http.StatusBadGateway)
			return
		}
		d.Backend.proxy.ServeHTTP(w, r)
		return
	}

	rec := &logRecord{req: req, backend: d.Backend}
	if p.logger.needsRequestBody() {
		captureBody(r, rec)
	}

	sw := &statusWriter{ResponseWriter: w}
	if p.logger.needsResponseBody() {
		sw.body = &bytes.Buffer{}
	}
	if d.Backend == nil {
		http.Error(sw, "switchyard: no backend available", http.StatusBadGateway)
	} else {
		r = r.WithContext(context.WithValue(r.Context(), recordKey, rec))
		d.Backend.proxy.ServeHTTP(sw, r)
	}

	rec.endTime = time.Now()
	rec.status = sw.status
	rec.respHeader = sw.Header()
	if sw.body != nil {
		rec.responseBody = sw.body.Bytes()
	}
	p.logger.log(rec)
}

// captureBody reads the request body into rec and restores it so the backend
// still receives the full body. Used only when the log format references it.
func captureBody(r *http.Request, rec *logRecord) {
	if r.Body == nil {
		return
	}
	data, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return
	}
	rec.requestBody = data
	r.Body = io.NopCloser(bytes.NewReader(data))
}

// contextKey is a private type for context values to avoid collisions.
type contextKey int

const recordKey contextKey = iota

// loggingTransport records when a request is sent to the backend, when the
// response returns, and the backend's status code, into the logRecord carried
// on the request context.
type loggingTransport struct {
	base http.RoundTripper
}

func (t *loggingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec, _ := r.Context().Value(recordKey).(*logRecord)
	if rec != nil {
		rec.forwardTime = time.Now()
	}
	resp, err := t.base.RoundTrip(r)
	if rec != nil {
		rec.appRespTime = time.Now()
		if resp != nil {
			rec.appStatus = resp.StatusCode
		}
	}
	return resp, err
}

// statusWriter wraps an http.ResponseWriter to capture the status code sent to
// the client. It forwards Flush and Hijack so streaming and connection upgrades
// (e.g. WebSockets) through the reverse proxy keep working.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
	body   *bytes.Buffer // non-nil when the response body should be captured
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	if w.body != nil {
		w.body.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("switchyard: response writer does not support hijacking")
}
