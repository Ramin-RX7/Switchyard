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
//	ResponseHeaders — global set_response_headers (default: TemplateResponseHeaderSetter, or nil)
//	Selector — backend selection for the global pool when no locations are set
//	Pool     — the global backend pool (default: StaticPool from config)
//	Access   — global IP access control (default: IPAccessControl from config, or nil)
//	Locations — the compiled location blocks; each carries its own pluggable stages
//	NotFound  — response when no location matched (default: 404 TemplateResponder)
//	BadGateway — response when an upstream is unreachable or a pool is empty (default: 502)
//	MethodNotAllowed — response when no backend accepts the request method (default: 405)
//	Forbidden — response when a location's access control denies the client (default: 403)
//	Transport — the shared http.RoundTripper used for all backends (tuned default)
//	MaxInFlight — optional ceiling on concurrent requests (0 = unlimited)
//
// Fields are read live per request, so assign any overrides after New and
// BEFORE serving. Once ListenAndServe/Handler is serving, treat the Proxy as
// immutable — mutating a field concurrently with requests is a data race.
type Proxy struct {
	Decider         Decider
	Actor           Actor
	Router          Router
	Logger          Logger                // global logger; nil when no custom logging is configured
	Headers         HeaderApplier         // global set_headers; nil when none configured
	ResponseHeaders ResponseHeaderApplier // global set_response_headers; nil when none configured
	Selector        BackendSelector       // global-pool selection (used only when no locations)
	Pool            BackendPool           // global backend pool (used only when no locations)
	Access          AccessController      // global IP access control; nil = unrestricted
	Locations       []*Location

	// NotFound / BadGateway / MethodNotAllowed / Forbidden generate the built-in
	// error responses (404 when no location matched; 502 when an upstream is
	// unreachable or a pool is empty; 405 when a location matched but no backend
	// accepts the request method; 403 when a location's access control denies the
	// client). New wires config-driven defaults; reassign before serving to
	// override.
	NotFound         ResponseGenerator
	BadGateway       ResponseGenerator
	MethodNotAllowed ResponseGenerator
	Forbidden        ResponseGenerator

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
	retry          retryPolicy
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

	var responseHeaders ResponseHeaderApplier
	if len(cfg.SetResponseHeaders) > 0 {
		rh, err := newResponseHeaderSetter(cfg.SetResponseHeaders)
		if err != nil {
			return nil, err
		}
		responseHeaders = rh
	}

	var access AccessController
	if len(cfg.Whitelist) > 0 || len(cfg.Blacklist) > 0 {
		ac, err := newIPAccessControl(cfg.Whitelist, cfg.Blacklist)
		if err != nil {
			return nil, err
		}
		access = ac
	}

	backends, err := buildBackends(cfg)
	if err != nil {
		return nil, err
	}

	overflow, err := cfg.overflowPolicy()
	if err != nil {
		return nil, err
	}
	retry, err := cfg.retryPolicy()
	if err != nil {
		return nil, err
	}
	badGateway, err := responderOf(cfg.BackendError, http.StatusBadGateway, "switchyard: backend unavailable")
	if err != nil {
		return nil, err
	}
	notFound, err := responderOf(cfg.NotFound, http.StatusNotFound, "switchyard: no matching location")
	if err != nil {
		return nil, err
	}
	methodNotAllowed, err := responderOf(cfg.MethodNotAllowed, http.StatusMethodNotAllowed, "switchyard: method not allowed")
	if err != nil {
		return nil, err
	}
	forbidden, err := responderOf(cfg.Forbidden, http.StatusForbidden, "switchyard: forbidden")
	if err != nil {
		return nil, err
	}

	rh, rt, wt, it := cfg.serverTimeouts()
	p := &Proxy{
		Logger:           logger,
		Headers:          headers,
		ResponseHeaders:  responseHeaders,
		Pool:             NewStaticPool(backends),
		Selector:         &RoundRobinSelector{},
		Access:           access,
		NotFound:         notFound,
		BadGateway:       badGateway,
		MethodNotAllowed: methodNotAllowed,
		Forbidden:        forbidden,
		MaxInFlight:      ptrInt(cfg.MaxConnections, 0),
		overflow:         overflow,
		retry:            retry,
		srvReadHeader:    rh,
		srvReadTimeout:   rt,
		srvWriteout:      wt,
		srvIdle:          it,
	}
	// Install each backend's own transport via the shim (which also records
	// timing and honors a global Proxy.Transport override at request time), plus
	// a failure handler that logs and renders the configurable BadGateway
	// response (read live so an SDK override of p.BadGateway takes effect).
	for _, b := range backends {
		b := b
		b.proxy.Transport = &proxyTransport{p: p, base: b.transport}
		// ModifyResponse converts a retryable upstream status into an error so the
		// response is discarded (nothing written to the client) and ErrorHandler
		// runs — the idiomatic pre-body-write retry hook. A no-op when the request
		// carries no retryState (retry disabled for its scope).
		b.proxy.ModifyResponse = func(resp *http.Response) error {
			if st, ok := resp.Request.Context().Value(retryKey).(*retryState); ok {
				return st.onResponse(resp.StatusCode)
			}
			return nil
		}
		b.proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			// A retry-active request routes every outcome through its retryState;
			// the Actor loop owns all client writes, so record and return here.
			if st, ok := r.Context().Value(retryKey).(*retryState); ok {
				if context.Cause(r.Context()) == errReloading {
					st.outcome = outcomeTerminalReload
					return
				}
				st.onError(err)
				return
			}
			if context.Cause(r.Context()) == errReloading {
				// The request was aborted by a force reload, not a backend
				// failure — report 503 (best-effort; a response already
				// streaming cannot have its status changed).
				http.Error(w, "switchyard: reloading", http.StatusServiceUnavailable)
				return
			}
			log.Printf("switchyard: backend %s failed: %v", b.URL, err)
			p.BadGateway.Generate(w, r, captureRequest(r))
		}
	}

	if len(cfg.Locations) > 0 {
		byID := make(map[string]*Backend, len(backends))
		for _, b := range backends {
			byID[b.ID] = b
		}
		locs, err := compileLocations(cfg.Locations, byID, cfg.Retry)
		if err != nil {
			return nil, err
		}
		p.Locations = locs
	}

	p.Router = &DefaultRouter{Locations: p.Locations}
	p.Actor = &DefaultActor{env: p, overflow: p.overflow, retry: p.retry}
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
func (p *Proxy) globalAccess() AccessController  { return p.Access }

// forwardPool returns the pool a forward may reroute within. It prefers the
// decision's method-eligible Candidates (so reroute never lands on a backend
// that rejects the method), falling back to the matched location's pool, or the
// global pool when no location applied.
func (p *Proxy) forwardPool(d Decision) []*Backend {
	if d.Candidates != nil {
		return d.Candidates
	}
	if d.Location != nil {
		return d.Location.Pool.Backends()
	}
	return p.Pool.Backends()
}

// forwardSelector returns the BackendSelector a forward reselects with on retry:
// the matched location's, or the global one when no location applied. Read live
// so SDK overrides of p.Selector / loc.Selector take effect.
func (p *Proxy) forwardSelector(d Decision) BackendSelector {
	if d.Location != nil {
		return d.Location.Selector
	}
	return p.Selector
}

// notFoundResponder / badGatewayResponder / methodNotAllowedResponder expose the
// global error responders to DefaultActor, reading them live so overrides
// assigned after New take effect.
func (p *Proxy) notFoundResponder() ResponseGenerator         { return p.NotFound }
func (p *Proxy) badGatewayResponder() ResponseGenerator       { return p.BadGateway }
func (p *Proxy) methodNotAllowedResponder() ResponseGenerator { return p.MethodNotAllowed }
func (p *Proxy) forbiddenResponder() ResponseGenerator        { return p.Forbidden }

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
			o.reject(w, r, captureRequest(r))
			return
		}
		defer lim.release()
		h.ServeHTTP(w, r)
	})
}

// newHTTPServer builds the client-facing http.Server for this proxy with the
// config-derived timeouts, defaulting addr to DefaultListen. The handler is
// passed in so the reloadable Server can supply its own dispatcher.
func (p *Proxy) newHTTPServer(addr string, h http.Handler) *http.Server {
	if addr == "" {
		addr = DefaultListen
	}
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: p.srvReadHeader,
		ReadTimeout:       p.srvReadTimeout,
		WriteTimeout:      p.srvWriteout,
		IdleTimeout:       p.srvIdle,
	}
}

// ListenAndServe starts an HTTP server on addr (falling back to DefaultListen
// when empty) and serves the proxy with defensive header/idle timeouts. Body
// timeouts are intentionally left unset so slow or large proxied responses are
// not cut off. It shuts down gracefully on SIGINT/SIGTERM, draining in-flight
// requests (up to a 15s deadline) before returning nil.
func (p *Proxy) ListenAndServe(addr string) error {
	srv := p.newHTTPServer(addr, p.Handler())

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

	log.Printf("switchyard: listening on %s, %d backend(s), %d location(s)", srv.Addr, len(p.Pool.Backends()), len(p.Locations))
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
	// Inject response headers (set_response_headers) just before the status line
	// is written, for every action and on both the fast and logging paths.
	if apply := p.responseHeaderFunc(req, d); apply != nil {
		w = &responseHeaderWriter{ResponseWriter: w, apply: apply}
	}

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

// applyStackedResponseHeaders is the response-side mirror of applyStackedHeaders:
// global set_response_headers first, then the matched location's on top, so a
// shared header name is won by the location while other globals are retained.
func (p *Proxy) applyStackedResponseHeaders(req Request, h http.Header, loc *Location) {
	if p.ResponseHeaders != nil {
		p.ResponseHeaders.Apply(req, h)
	}
	if loc != nil && loc.ResponseHeaders != nil {
		loc.ResponseHeaders.Apply(req, h)
	}
}

// responseHeaderFunc returns the closure that applies the response headers for
// this request (global stacked with the matched location's), or nil when none
// are configured — so handleRequest only wraps the writer when needed.
func (p *Proxy) responseHeaderFunc(req Request, d Decision) func(http.Header) {
	if p.ResponseHeaders == nil && (d.Location == nil || d.Location.ResponseHeaders == nil) {
		return nil
	}
	return func(h http.Header) { p.applyStackedResponseHeaders(req, h, d.Location) }
}
