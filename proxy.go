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
	"strings"
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
	backends  []*backend
	locations []*location   // empty => global round-robin over all backends
	logger    *Logger       // nil when no custom logging is configured
	headers   *headerSetter // nil when no set_headers are configured
	next      atomic.Uint64
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

	var headers *headerSetter
	if len(cfg.SetHeaders) > 0 {
		hs, err := newHeaderSetter(cfg.SetHeaders)
		if err != nil {
			return nil, err
		}
		headers = hs
	}

	// Backend round-trip timing is needed when any logging is configured,
	// globally or on any location. The transport is a no-op without a logRecord
	// on the request context, so installing it on every backend when any logging
	// exists is cheap and correct even for backends shared across locations.
	anyLogging := logger != nil
	for _, lc := range cfg.Locations {
		if lc.Logging != nil {
			anyLogging = true
		}
	}

	p := &Proxy{logger: logger, headers: headers}
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
		// When any logging is enabled, wrap the transport so the backend
		// round-trip timing and status code can be recorded per request.
		if anyLogging {
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

	if len(cfg.Locations) > 0 {
		byID := make(map[string]*backend, len(p.backends))
		for _, b := range p.backends {
			byID[b.id] = b
		}
		locs, err := compileLocations(cfg.Locations, byID)
		if err != nil {
			return nil, err
		}
		p.locations = locs
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

// decide interprets the request and records what should happen to it. It is
// passive: string/regexp matching and an atomic counter increment only, no I/O
// and no forwarding. With locations configured, the request is matched against
// them in order (first match wins) and routing happens within the matched
// location's pool; otherwise a global round-robin over all backends is used.
func (p *Proxy) decide(req Request) Decision {
	if len(p.locations) > 0 {
		for _, loc := range p.locations {
			if !loc.matches(req.Path) {
				continue
			}
			if loc.kind == "static" {
				return Decision{Action: "static", Reason: "location " + loc.raw, Location: loc}
			}
			b := loc.selectBackend()
			if b == nil {
				return Decision{Action: "reject", Reason: "location " + loc.raw + ": empty pool",
					Location: loc, Status: http.StatusBadGateway}
			}
			return Decision{Action: "forward", Reason: "round-robin", Backend: b, Location: loc}
		}
		return Decision{Action: "reject", Reason: "no matching location", Status: http.StatusNotFound}
	}

	if len(p.backends) == 0 {
		return Decision{Action: "reject", Reason: "no backends configured"}
	}
	i := p.next.Add(1) - 1
	b := p.backends[int(i%uint64(len(p.backends)))]
	return Decision{Action: "forward", Reason: "round-robin", Backend: b}
}

// handleRequest acts on the decision. It is the only stage with side effects.
// When custom logging applies (globally or for the matched location), it wraps
// the response to capture status/body, threads a logRecord through the request
// context for backend timing, performs the action, then renders the single
// shared record through every applicable logger. Otherwise it takes the fast
// path: the built-in operational log line and a plain response writer.
func (p *Proxy) handleRequest(w http.ResponseWriter, r *http.Request, req Request, d Decision) {
	loggers := p.loggersFor(d)
	if len(loggers) == 0 {
		log.Printf("switchyard: %s -> %s", req, d)
		p.act(w, r, req, d)
		return
	}

	rec := &logRecord{req: req, backend: d.Backend}
	if anyNeedsRequestBody(loggers) {
		captureBody(r, rec)
	}
	sw := &statusWriter{ResponseWriter: w}
	if anyNeedsResponseBody(loggers) {
		sw.body = &bytes.Buffer{}
	}
	r = r.WithContext(context.WithValue(r.Context(), recordKey, rec))

	p.act(sw, r, req, d)

	rec.endTime = time.Now()
	rec.status = sw.status
	rec.respHeader = sw.Header()
	if sw.body != nil {
		rec.responseBody = sw.body.Bytes()
	}
	for _, l := range loggers {
		l.log(rec)
	}
}

// loggersFor returns the loggers that apply to a request: the global logger and
// the matched location's logger, each included only when configured. Both fire;
// neither overrides the other.
func (p *Proxy) loggersFor(d Decision) []*Logger {
	var ls []*Logger
	if p.logger != nil {
		ls = append(ls, p.logger)
	}
	if d.Location != nil && d.Location.logger != nil {
		ls = append(ls, d.Location.logger)
	}
	return ls
}

func anyNeedsRequestBody(ls []*Logger) bool {
	for _, l := range ls {
		if l.needsRequestBody() {
			return true
		}
	}
	return false
}

func anyNeedsResponseBody(ls []*Logger) bool {
	for _, l := range ls {
		if l.needsResponseBody() {
			return true
		}
	}
	return false
}

// act performs the side effect selected by the decision, applying any stacked
// headers before forwarding or serving.
func (p *Proxy) act(w http.ResponseWriter, r *http.Request, req Request, d Decision) {
	switch d.Action {
	case "forward":
		p.applyStackedHeaders(req, r, d.Location)
		d.Backend.proxy.ServeHTTP(w, r)
	case "static":
		p.applyStackedHeaders(req, r, d.Location)
		p.serveStatic(w, r, req, d.Location)
	default: // "reject"
		status := d.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		http.Error(w, "switchyard: "+d.Reason, status)
	}
}

// serveStatic serves the request from the location's file root, stripping the
// configured prefix from the path first. http.FileServer/http.Dir handle
// content types, ranges, and path-traversal protection.
func (p *Proxy) serveStatic(w http.ResponseWriter, r *http.Request, req Request, loc *location) {
	upath := req.Path
	if loc.stripPrefix != "" {
		upath = strings.TrimPrefix(upath, loc.stripPrefix)
	}
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = upath
	loc.fileServer.ServeHTTP(w, r2)
}

// applyStackedHeaders applies the global set_headers then the location's, so
// for a shared header name the location wins while all other global headers are
// retained. Values render against the original request snapshot either way.
func (p *Proxy) applyStackedHeaders(req Request, r *http.Request, loc *location) {
	if p.headers != nil {
		p.headers.apply(req, r)
	}
	if loc != nil && loc.headers != nil {
		loc.headers.apply(req, r)
	}
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
