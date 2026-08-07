package switchyard

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"time"
)

// DefaultListen is the address used when Config.Listen is empty.
const DefaultListen = ":8091"

// Proxy forwards incoming requests to configured backends. Its exported fields
// are the pluggable stages: assign your own implementations after New (and
// before serving) to override the built-in behavior without editing Switchyard.
//
//	Decider  — routing/selection logic (default: DefaultDecider)
//	Actor    — performs the side effect of a decision (default: DefaultActor)
//	Router   — location detection (default: DefaultRouter)
//	Logger   — global request logger (default: FormatLogger from config, or nil)
//	Headers  — global set_headers (default: TemplateHeaderSetter, or nil)
//	Selector — backend selection for the global pool when no locations are set
//	Pool     — the global backend pool (default: StaticPool from config)
//	Locations — the compiled location blocks; each carries its own pluggable stages
type Proxy struct {
	Decider   Decider
	Actor     Actor
	Router    Router
	Logger    Logger          // global logger; nil when no custom logging is configured
	Headers   HeaderApplier   // global set_headers; nil when none configured
	Selector  BackendSelector // global-pool selection (used only when no locations)
	Pool      BackendPool     // global backend pool (used only when no locations)
	Locations []*Location
}

// New builds a Proxy from configuration, preparing a reverse proxy for each
// backend and wiring the default implementation of every pluggable stage. The
// returned Proxy reproduces Switchyard's turnkey behavior exactly; SDK users
// override a stage by reassigning the corresponding field before serving.
func New(cfg Config) (*Proxy, error) {
	var logger Logger
	if cfg.Logging != nil {
		l, err := newLogger(*cfg.Logging)
		if err != nil {
			return nil, err
		}
		logger = l
	}

	var headers HeaderApplier
	if len(cfg.SetHeaders) > 0 {
		hs, err := newHeaderSetter(cfg.SetHeaders)
		if err != nil {
			return nil, err
		}
		headers = hs
	}

	// Backend round-trip timing is needed when any logging is configured,
	// globally or on any location. The transport is a no-op without a LogRecord
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

	p := &Proxy{Logger: logger, Headers: headers, Pool: NewStaticPool(backends), Selector: &RoundRobinSelector{}}

	if len(cfg.Locations) > 0 {
		byID := make(map[string]*Backend, len(backends))
		for _, b := range backends {
			byID[b.ID] = b
		}
		locs, err := compileLocations(cfg.Locations, byID)
		if err != nil {
			return nil, err
		}
		p.Locations = locs
	}

	p.Router = &DefaultRouter{Locations: p.Locations}
	p.Actor = &DefaultActor{env: p}
	p.Decider = &DefaultDecider{env: p}
	return p, nil
}

// The following methods let *Proxy satisfy the narrow routingEnv/headerEnv
// interfaces that DefaultDecider/DefaultActor depend on. They read live fields
// so overrides assigned after New still take effect.
func (p *Proxy) hasLocations() bool              { return len(p.Locations) > 0 }
func (p *Proxy) match(req Request) *Location     { return p.Router.Match(req) }
func (p *Proxy) globalPool() []*Backend          { return p.Pool.Backends() }
func (p *Proxy) globalSelector() BackendSelector { return p.Selector }

// Handler returns an http.Handler that serves the proxy. Use it to mount
// Switchyard inside an existing server or middleware chain.
func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", p.handle)
	return mux
}

// ListenAndServe starts an HTTP server on addr (falling back to DefaultListen
// when empty) and serves the proxy with defensive header/idle timeouts. Body
// timeouts are intentionally left unset so slow or large proxied responses are
// not cut off.
func (p *Proxy) ListenAndServe(addr string) error {
	if addr == "" {
		addr = DefaultListen
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           p.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("switchyard: listening on %s, %d backend(s), %d location(s)", addr, len(p.Pool.Backends()), len(p.Locations))
	return srv.ListenAndServe()
}

// handle is the HTTP entry point: capture the request into the internal form,
// decide what to do with it, then handle it.
func (p *Proxy) handle(w http.ResponseWriter, r *http.Request) {
	req := captureRequest(r)
	d := p.Decider.Decide(req)
	p.handleRequest(w, r, req, d)
}

// handleRequest acts on the decision. It is the only stage with side effects.
// When custom logging applies (globally or for the matched location), it wraps
// the response to capture status/body, threads a LogRecord through the request
// context for backend timing, performs the action, then renders the single
// shared record through every applicable logger. Otherwise it takes the fast
// path: the built-in operational log line and a plain response writer.
func (p *Proxy) handleRequest(w http.ResponseWriter, r *http.Request, req Request, d Decision) {
	loggers := p.loggersFor(d)
	if len(loggers) == 0 {
		log.Printf("switchyard: %s -> %s", req, d)
		p.Actor.Act(w, r, req, d)
		return
	}

	rec := &LogRecord{Req: req, Backend: d.Backend}
	if anyNeedsRequestBody(loggers) {
		captureBody(r, rec)
	}
	sw := &statusWriter{ResponseWriter: w}
	if anyNeedsResponseBody(loggers) {
		sw.body = &bytes.Buffer{}
	}
	r = r.WithContext(context.WithValue(r.Context(), recordKey, rec))

	p.Actor.Act(sw, r, req, d)

	rec.EndTime = time.Now()
	rec.Status = sw.status
	rec.RespHeader = sw.Header()
	if sw.body != nil {
		rec.ResponseBody = sw.body.Bytes()
	}
	for _, l := range loggers {
		l.Log(rec)
	}
}

// loggersFor returns the loggers that apply to a request: the global logger and
// the matched location's logger, each included only when configured. Both fire;
// neither overrides the other.
func (p *Proxy) loggersFor(d Decision) []Logger {
	var ls []Logger
	if p.Logger != nil {
		ls = append(ls, p.Logger)
	}
	if d.Location != nil && d.Location.Logger != nil {
		ls = append(ls, d.Location.Logger)
	}
	return ls
}

// applyStackedHeaders applies the global set_headers then the location's, so
// for a shared header name the location wins while all other global headers are
// retained. Values render against the original request snapshot either way. It
// is used by the Actor (kept on Proxy since it reads the global Headers stage).
func (p *Proxy) applyStackedHeaders(req Request, r *http.Request, loc *Location) {
	if p.Headers != nil {
		p.Headers.Apply(req, r)
	}
	if loc != nil && loc.Headers != nil {
		loc.Headers.Apply(req, r)
	}
}
