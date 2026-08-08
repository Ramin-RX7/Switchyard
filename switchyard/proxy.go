package switchyard

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
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
//	Transport — the shared http.RoundTripper used for all backends (tuned default)
//	MaxInFlight — optional ceiling on concurrent requests (0 = unlimited)
//
// Fields are read live per request, so assign any overrides after New and
// BEFORE serving. Once ListenAndServe/Handler is serving, treat the Proxy as
// immutable — mutating a field concurrently with requests is a data race.
type Proxy struct {
	Decider   Decider
	Actor     Actor
	Router    Router
	Logger    Logger          // global logger; nil when no custom logging is configured
	Headers   HeaderApplier   // global set_headers; nil when none configured
	Selector  BackendSelector // global-pool selection (used only when no locations)
	Pool      BackendPool     // global backend pool (used only when no locations)
	Locations []*Location

	// Transport, when non-nil, is a global override used to reach ALL backends.
	// By default it is nil: New builds a tuned per-backend transport from config
	// (idle-pool limits, TLS-handshake timeout, keep-alive). Set this before
	// serving to force one custom http.RoundTripper for every backend.
	Transport http.RoundTripper

	// MaxInFlight, when > 0, caps concurrently-served requests project-wide;
	// excess requests are handled by the overflow policy (reject/queue). It is
	// set from the config's top-level max_connections. 0 (default) = unlimited.
	MaxInFlight int

	// resolved config-derived settings (set in New).
	overflow       overflowPolicy
	srvReadHeader  time.Duration
	srvReadTimeout time.Duration
	srvWriteout    time.Duration
	srvIdle        time.Duration
}

// New builds a Proxy from configuration, preparing a reverse proxy for each
// backend and wiring the default implementation of every pluggable stage. The
// returned Proxy reproduces Switchyard's turnkey behavior exactly; SDK users
// override a stage by reassigning the corresponding field before serving.
func New(cfg Config) (*Proxy, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

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

	backends, err := buildBackends(cfg)
	if err != nil {
		return nil, err
	}

	rh, rt, wt, it := cfg.serverTimeouts()
	p := &Proxy{
		Logger:         logger,
		Headers:        headers,
		Pool:           NewStaticPool(backends),
		Selector:       &RoundRobinSelector{},
		MaxInFlight:    ptrInt(cfg.MaxConnections, 0),
		overflow:       cfg.overflowPolicy(),
		srvReadHeader:  rh,
		srvReadTimeout: rt,
		srvWriteout:    wt,
		srvIdle:        it,
	}
	// Install each backend's own transport via the shim (which also records
	// timing and honors a global Proxy.Transport override at request time).
	for _, b := range backends {
		b.proxy.Transport = &proxyTransport{p: p, base: b.transport}
	}

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
	p.Actor = &DefaultActor{env: p, overflow: p.overflow}
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

// forwardPool returns the pool a forward may reroute within: the matched
// location's pool, or the global pool when no location applied.
func (p *Proxy) forwardPool(d Decision) []*Backend {
	if d.Location != nil {
		return d.Location.Pool.Backends()
	}
	return p.Pool.Backends()
}

// Handler returns an http.Handler that serves the proxy. Use it to mount
// Switchyard inside an existing server or middleware chain. When MaxInFlight is
// set, the returned handler enforces the project-wide concurrency ceiling via
// the overflow policy.
func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", p.handle)
	if p.MaxInFlight > 0 {
		return p.globalLimit(mux)
	}
	return mux
}

// globalLimit wraps h with the project-wide in-flight ceiling. On overflow it
// applies the configured policy (reject immediately, or queue up to the
// configured wait) and the configured reject status/body.
func (p *Proxy) globalLimit(h http.Handler) http.Handler {
	lim := newLimiter(p.MaxInFlight)
	o := p.overflow
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !o.acquire(r.Context(), lim) {
			o.reject(w)
			return
		}
		defer lim.release()
		h.ServeHTTP(w, r)
	})
}

// ListenAndServe starts an HTTP server on addr (falling back to DefaultListen
// when empty) and serves the proxy with defensive header/idle timeouts. Body
// timeouts are intentionally left unset so slow or large proxied responses are
// not cut off. It shuts down gracefully on SIGINT/SIGTERM, draining in-flight
// requests (up to a 15s deadline) before returning nil.
func (p *Proxy) ListenAndServe(addr string) error {
	if addr == "" {
		addr = DefaultListen
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           p.Handler(),
		ReadHeaderTimeout: p.srvReadHeader,
		ReadTimeout:       p.srvReadTimeout,
		WriteTimeout:      p.srvWriteout,
		IdleTimeout:       p.srvIdle,
	}

	shutdownErr := make(chan error, 1)
	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
		<-sigs
		log.Printf("switchyard: shutting down, draining in-flight requests…")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		shutdownErr <- srv.Shutdown(ctx)
	}()

	log.Printf("switchyard: listening on %s, %d backend(s), %d location(s)", addr, len(p.Pool.Backends()), len(p.Locations))
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err // failed to bind / unexpected error
	}
	return <-shutdownErr // wait for Shutdown to finish draining
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
