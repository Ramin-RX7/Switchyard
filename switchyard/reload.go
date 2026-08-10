package switchyard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// errReloading is the cancellation cause a force reload attaches to the retired
// generation's context. The backend ErrorHandler checks for it so a
// reload-aborted request reports 503 rather than a backend failure.
var errReloading = errors.New("switchyard: reloading")

// generation is one live configuration behind the reloadable Server: the
// compiled handler for a Proxy plus a context whose cancellation (on a force
// reload) aborts the requests still running against it. inflight tracks those
// requests so a drained, superseded generation can release its context.
type generation struct {
	proxy      *Proxy
	handler    http.Handler
	ctx        context.Context
	cancel     context.CancelCauseFunc
	inflight   atomic.Int64
	superseded atomic.Bool
}

// Server runs a Proxy with hot configuration reload. Build is called once at
// startup and again on every reload to (re)construct the Proxy, so a config-only
// operator returns New(LoadConfig(path)) and an SDK user returns a Proxy with
// their overrides re-applied. A reload swaps the live Proxy atomically:
//
//   - graceful (the default): requests already in flight finish on the old
//     Proxy; new requests use the new one. No connections are dropped.
//   - force: the in-flight requests are cancelled first (best-effort 503), then
//     the swap happens.
//
// A Build error (e.g. an invalid new config) is logged and the current Proxy
// keeps serving — a bad reload never takes the server down.
//
// The listen address and the client-facing http.Server timeouts are fixed for
// the life of the process (they come from the initial Proxy); changing them
// needs a full restart. Everything a reload rebuilds — backends, locations,
// headers, responders, limits, access control, routing — takes effect live.
type Server struct {
	Addr    string                 // listen address (fixed once Run starts)
	PidFile string                 // optional; written on Run, removed on exit
	Build   func() (*Proxy, error) // required; constructs a fully-configured Proxy

	cur atomic.Pointer[generation]
}

func (s *Server) newGeneration(p *Proxy) *generation {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &generation{proxy: p, handler: p.Handler(), ctx: ctx, cancel: cancel}
}

// Start builds the initial Proxy and returns the reloadable handler. Use it to
// mount Switchyard (with reload) inside your own server; Run wraps it with an
// http.Server, a pid file, and signal handling.
func (s *Server) Start() (http.Handler, error) {
	if s.Build == nil {
		return nil, errors.New("switchyard: Server.Build must be set")
	}
	p, err := s.Build()
	if err != nil {
		return nil, err
	}
	s.cur.Store(s.newGeneration(p))
	return s, nil
}

// ServeHTTP dispatches each request to the current generation, deriving a
// request context from that generation so a force reload can cancel it. A
// request reads the generation once at entry and uses it for its whole life, so
// a graceful reload never disturbs work already in progress.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g := s.cur.Load()
	g.inflight.Add(1)
	defer func() {
		if g.inflight.Add(-1) == 0 && g.superseded.Load() {
			g.cancel(nil) // last request on a retired generation drained
		}
	}()

	ctx, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)
	// Propagate a force reload's cancellation (with its cause) into this request.
	stop := context.AfterFunc(g.ctx, func() { cancel(context.Cause(g.ctx)) })
	defer stop()

	g.handler.ServeHTTP(w, r.WithContext(ctx))
}

// Reload rebuilds the Proxy and swaps it in. On a graceful reload in-flight
// requests keep running on the old generation; on a force reload they are
// cancelled immediately. A Build error leaves the current generation in place.
// It is safe to call concurrently with serving and can be triggered by an SDK
// user directly (Run wires it to SIGHUP/SIGUSR2).
func (s *Server) Reload(force bool) {
	p, err := s.Build()
	if err != nil {
		log.Printf("switchyard: reload failed, keeping current config: %v", err)
		return
	}
	old := s.cur.Swap(s.newGeneration(p))
	if old != nil {
		old.superseded.Store(true)
		switch {
		case force:
			old.cancel(errReloading) // abort in-flight now
		case old.inflight.Load() == 0:
			old.cancel(nil) // already idle; release its context
		}
	}
	mode := "graceful"
	if force {
		mode = "force"
	}
	log.Printf("switchyard: reloaded config (%s), %d backend(s), %d location(s)", mode, len(p.Pool.Backends()), len(p.Locations))
}

// Run builds the initial Proxy, starts the HTTP server, writes the pid file (if
// configured), and blocks handling reload signals (SIGHUP graceful, SIGUSR2
// force) and shutdown signals (SIGINT/SIGTERM, draining in-flight up to 15s).
func (s *Server) Run() error {
	h, err := s.Start()
	if err != nil {
		return err
	}
	p := s.cur.Load().proxy // initial proxy, for server timeouts + startup log

	if s.PidFile != "" {
		if err := os.WriteFile(s.PidFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
			return fmt.Errorf("write pid file: %w", err)
		}
		defer os.Remove(s.PidFile)
	}

	srv := p.newHTTPServer(s.Addr, h)

	shutdownErr := make(chan error, 1)
	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGHUP, syscall.SIGUSR2, os.Interrupt, syscall.SIGTERM)
		for sig := range sigs {
			switch sig {
			case syscall.SIGHUP:
				s.Reload(false)
			case syscall.SIGUSR2:
				s.Reload(true)
			default: // SIGINT / SIGTERM
				log.Printf("switchyard: shutting down, draining in-flight requests…")
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				shutdownErr <- srv.Shutdown(ctx)
				cancel()
				return
			}
		}
	}()

	log.Printf("switchyard: listening on %s, %d backend(s), %d location(s) [reloadable]", srv.Addr, len(p.Pool.Backends()), len(p.Locations))
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return <-shutdownErr
}

// SignalReload tells a running Server (identified by its pid file) to reload.
// force selects a force reload (SIGUSR2) over a graceful one (SIGHUP). It is the
// mechanism behind the `switchyard reload` command.
func SignalReload(pidFile string, force bool) error {
	pid, err := ReadPidFile(pidFile)
	if err != nil {
		return err
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	sig := syscall.SIGHUP
	if force {
		sig = syscall.SIGUSR2
	}
	return proc.Signal(sig)
}

// ReadPidFile reads the PID written by a running Server.
func ReadPidFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid pid file %q: %w", path, err)
	}
	return pid, nil
}
