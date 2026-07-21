package main

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

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

	backends, err := buildBackends(cfg.Backends, anyLogging)
	if err != nil {
		return nil, err
	}

	p := &Proxy{logger: logger, headers: headers, backends: backends}

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
			if loc.kind == kindStatic {
				return Decision{Action: ActionStatic, Reason: "location " + loc.raw, Location: loc}
			}
			b := loc.selectBackend()
			if b == nil {
				return Decision{Action: ActionReject, Reason: "location " + loc.raw + ": empty pool",
					Location: loc, Status: http.StatusBadGateway}
			}
			return Decision{Action: ActionForward, Reason: "round-robin", Backend: b, Location: loc}
		}
		return Decision{Action: ActionReject, Reason: "no matching location", Status: http.StatusNotFound}
	}

	if len(p.backends) == 0 {
		return Decision{Action: ActionReject, Reason: "no backends configured"}
	}
	i := p.next.Add(1) - 1
	b := p.backends[int(i%uint64(len(p.backends)))]
	return Decision{Action: ActionForward, Reason: "round-robin", Backend: b}
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

// act performs the side effect selected by the decision, applying any stacked
// headers before forwarding or serving.
func (p *Proxy) act(w http.ResponseWriter, r *http.Request, req Request, d Decision) {
	switch d.Action {
	case ActionForward:
		p.applyStackedHeaders(req, r, d.Location)
		d.Backend.proxy.ServeHTTP(w, r)
	case ActionStatic:
		p.applyStackedHeaders(req, r, d.Location)
		p.serveStatic(w, r, req, d.Location)
	default: // ActionReject
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
