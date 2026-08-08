# Extending Switchyard (SDK)

Switchyard can be used two ways, from the **same codebase**:

- **Mode A — config-only (nginx-style).** Run the prebuilt `Switchyard` binary against a `switchyard.json`. Zero Go code. This is [the turnkey binary](../README.md#quick-start).
- **Mode B — SDK.** Import Switchyard as a Go library, override any pipeline stage with your own logic, and compile your own binary. You never fork or edit Switchyard's source — your customizations live entirely in your own package.

This document covers Mode B.

---

## The model: config + overridable logic

Every core stage of the pipeline is an **interface** with a **config-driven default implementation**. This mirrors Django's `list_display` (a declarative config) versus `get_list_display` (the overridable method that produces it): keep the config-driven default, or replace the logic when you need to.

You override a stage in one of two ways:

1. **Replace it** — assign your own implementation of the interface.
2. **Extend it** — embed the default struct, override just the method you care about, and delegate to the embedded default for everything else (Go's closest analog to subclassing).

`New(cfg)` wires every stage to its default, reproducing the turnkey binary's behavior exactly. You then reassign fields before calling `ListenAndServe`.

```go
import sw "github.com/Ramin-RX7/Switchyard/switchyard"

cfg, _ := sw.LoadConfig("switchyard.json")
p, _ := sw.New(cfg)          // all defaults wired from config
p.Selector = &MySelector{}   // override the global backend selector
p.ListenAndServe(cfg.Listen)
```

---

## The pluggable surface

All nine core stages are pluggable. Each is an interface with a config-driven default, assigned to an exported field on `Proxy` (global) and/or `Location` (per-location).

| Stage | Interface | Default | Where you set it |
|-------|-----------|---------|------------------|
| Decide (routing) | `Decider { Decide(req Request) Decision }` | `DefaultDecider` | `p.Decider` |
| Act (side effect) | `Actor { Act(w, r, req Request, d Decision) }` | `DefaultActor` | `p.Actor` |
| Location detection | `Router { Match(req Request) *Location }` | `DefaultRouter` | `p.Router` |
| Backend selection | `BackendSelector { Select(pool []*Backend, req Request) *Backend }` | `RoundRobinSelector` | `p.Selector` (global pool), `loc.Selector` (per location) |
| Backend list | `BackendPool { Backends() []*Backend }` | `StaticPool` | `p.Pool` (global pool), `loc.Pool` (per location) |
| set_headers | `HeaderApplier { Apply(req Request, r *http.Request) }` | `TemplateHeaderSetter` | `p.Headers` (global), `loc.Headers` (per location) |
| Media serving | `StaticServer { Serve(w, r, req Request) }` | `FileServer` | `loc.Static` (per static location) |
| Response generation | `ResponseGenerator { Generate(w, r, req Request) }` | `TemplateResponder` | `loc.Responder` (response locations), `p.NotFound` / `p.BadGateway` (global error responses) |
| Logging | `Logger { Log(rec *LogRecord); NeedsRequestBody() bool; NeedsResponseBody() bool }` | `FormatLogger` | `p.Logger` (global), `loc.Logger` (per location) |

### Two levels: global and per-location

Most stages exist at two levels, matching how config stacks:

- **Global** — `p.Selector`, `p.Pool`, `p.Headers`, `p.Logger`, plus the top-level `p.Decider`/`p.Actor`/`p.Router`. The global `Selector`/`Pool` are used only when no `locations` are configured.
- **Per-location** — `loc.Selector`, `loc.Pool`, `loc.Headers`, `loc.Logger`, `loc.Static` for each entry in `p.Locations`. Each location has its own instances, so overriding one location never affects the others.

Find the location you want to customize via its configured path:

```go
for _, loc := range p.Locations {
    if loc.Path() == "/api/" {
        loc.Selector = &stickyByIP{}
    }
}
```

---

## Worked example: a custom backend selector

Replace round-robin with sticky-by-client-IP affinity on one location, keeping round-robin everywhere else. This is the runnable example at [`examples/custom-selector`](../examples/custom-selector/main.go).

```go
package main

import (
	"flag"
	"hash/fnv"
	"log"
	"net"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// stickyByIP embeds the default RoundRobinSelector so it can fall back to plain
// rotation — the "override one thing, keep the rest" pattern.
type stickyByIP struct {
	sw.RoundRobinSelector
}

func (s *stickyByIP) Select(pool []*sw.Backend, req sw.Request) *sw.Backend {
	if len(pool) == 0 {
		return nil
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	if host == "" {
		return s.RoundRobinSelector.Select(pool, req) // delegate to the default
	}
	h := fnv.New32a()
	h.Write([]byte(host))
	return pool[int(h.Sum32())%len(pool)]
}

func main() {
	configPath := flag.String("config", "switchyard.json", "path to configuration file")
	flag.Parse()

	cfg, err := sw.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("switchyard: %v", err)
	}
	p, err := sw.New(cfg)
	if err != nil {
		log.Fatalf("switchyard: %v", err)
	}

	for _, loc := range p.Locations {
		if loc.Kind == sw.KindProxy && loc.Path() == "/api/" {
			loc.Selector = &stickyByIP{}
		}
	}

	if err := p.ListenAndServe(cfg.Listen); err != nil {
		log.Fatalf("switchyard: server stopped: %v", err)
	}
}
```

Build and run it exactly like the turnkey binary:

```bash
go run ./examples/custom-selector -config switchyard.json
```

---

## Overriding other stages

**Whole-Decider replacement** — take full control of routing while reusing the default for the cases you don't care about:

```go
type myDecider struct {
	*sw.DefaultDecider // the instance New already built
}

func (d *myDecider) Decide(req sw.Request) sw.Decision {
	if req.Header.Get("X-Canary") == "1" {
		// custom routing...
	}
	return d.DefaultDecider.Decide(req) // fall back to built-in routing
}

p.Decider = &myDecider{p.Decider.(*sw.DefaultDecider)}
```

**Custom logger** — implement `Logger` to emit structured logs, metrics, or spans. Return `false` from both `NeedsRequestBody`/`NeedsResponseBody` unless you actually read the bodies, so the proxy skips buffering them:

```go
type jsonLogger struct{}

func (jsonLogger) Log(rec *sw.LogRecord)      { /* marshal rec fields to JSON */ }
func (jsonLogger) NeedsRequestBody() bool     { return false }
func (jsonLogger) NeedsResponseBody() bool    { return false }

p.Logger = jsonLogger{}
```

`LogRecord`'s fields (`Req`, `Backend`, timing, `Status`, `AppStatus`, bodies, `RespHeader`) are exported for exactly this.

**Extend set_headers** — embed the config-built `TemplateHeaderSetter`, keep its headers, and add your own. This example (the runnable [`examples/request-id`](../examples/request-id/main.go)) injects an incrementing `X-Request-Id`:

```go
type withRequestID struct {
	*sw.TemplateHeaderSetter // the config-built default (may be nil)
	n atomic.Uint64
}

func (h *withRequestID) Apply(req sw.Request, r *http.Request) {
	if h.TemplateHeaderSetter != nil {
		h.TemplateHeaderSetter.Apply(req, r) // keep all configured set_headers
	}
	r.Header.Set("X-Request-Id", strconv.FormatUint(h.n.Add(1), 10))
}

base, _ := p.Headers.(*sw.TemplateHeaderSetter) // nil if no set_headers in config
p.Headers = &withRequestID{TemplateHeaderSetter: base}
```

**Custom backend pool** — implement `BackendPool` to feed selection a dynamic set (health-checked, service-discovery-backed). Set `p.Pool` (global) or `loc.Pool` (per location); it's called per request, so keep it cheap and concurrency-safe.

**Custom response generator** — implement `ResponseGenerator` to produce Switchyard's own responses (status + headers + body). The default `TemplateResponder` is config-driven; replace it to emit richer error payloads, structured JSON, metrics, etc. Assign it globally to `p.NotFound` (no-match 404) or `p.BadGateway` (backend-unavailable/empty-pool 502), or per response-location to `loc.Responder`. These fields are read **live**, so overrides after `New()` take effect. This example swaps the 502 for a JSON error:

```go
type jsonError struct{}

func (jsonError) Generate(w http.ResponseWriter, _ *http.Request, req sw.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	fmt.Fprintf(w, `{"error":"upstream unavailable","path":%q}`, req.URI)
}

p.BadGateway = jsonError{} // no-match 404 → p.NotFound; response location → loc.Responder
```

(The `overflow` reject response stays config-driven; for full programmatic control over it, override the `Actor`.)

**Custom router / actor / static server** — assign `p.Router` (e.g. host- or header-based routing), `p.Actor` (e.g. add retries or response rewriting), or `loc.Static` (e.g. serve from an embedded FS) the same way.

---

## Mounting inside an existing server

`Handler()` returns an `http.Handler`, so you can mount Switchyard in an existing mux or middleware chain instead of using `ListenAndServe`:

```go
mux := http.NewServeMux()
mux.Handle("/", p.Handler())
```

---

## Concurrency & tuning

Go's `net/http` serves every request in its own goroutine, so Switchyard is concurrent by default. A few knobs and rules matter under load:

- **Immutability contract.** Configure the `Proxy`/`Location` fields *before* serving. Once `ListenAndServe`/`Handler` is serving, treat the `Proxy` as read-only — mutating a field concurrently with live requests is a data race. (The only field written during requests is the round-robin counter, which is atomic.)

- **Transport (connection pooling & timeouts).** Most tuning is configurable in JSON per project/backend — idle-pool limits, TLS-handshake and overall request timeouts, keep-alive (see [config-reference.md](config-reference.md#connection-limits--timeouts)). `New` builds each backend its own tuned transport. To force one custom `http.RoundTripper` for **all** backends from Go, set the global override before serving:

  ```go
  p.Transport = &http.Transport{ /* custom TLS, dial, proxy, … */ } // nil = per-backend transports
  ```

  The override late-binds (the shim reads `p.Transport` per request) and timing/status logging still works through it.

- **Connection caps & backpressure.** `max_connections` at the project, location, and backend scopes cap concurrent in-flight requests (independent, nested). The project cap is `Proxy.MaxInFlight`; over-capacity behavior (reject/queue + response) is the `overflow` config. From Go:

  ```go
  p.MaxInFlight = 500 // 0 = unlimited (default); or set config's max_connections
  ```

- **Capacity-aware selection.** A custom `BackendSelector` can distribute by load using each backend's live capacity (the default round-robin ignores it). This is the runnable [`examples/least-loaded`](../examples/least-loaded/main.go):

  ```go
  func (s *leastLoaded) Select(pool []*sw.Backend, _ sw.Request) *sw.Backend {
      best := pool[0]
      for _, b := range pool[1:] {
          // prefer the backend with the most spare capacity
          if spare(b) > spare(best) { best = b }
      }
      return best
  }
  // spare(b) = b.MaxConns() - b.InFlight(); treat MaxConns()==0 as unlimited.
  ```

  This differs from `overflow: reroute`: capacity-aware *selection* picks a good backend up front on every request; `reroute` only kicks in *after* the chosen backend is found full. They compose.

- **Custom over-capacity response.** Beyond the `overflow` status/body config, an SDK user can override the `Actor` for full control (custom body, headers, metrics) when a cap is hit.

- **Graceful shutdown.** `ListenAndServe` traps `SIGINT`/`SIGTERM`, stops accepting new connections, and drains in-flight requests (15s deadline) before returning `nil`. If you use `Handler()` in your own server, call `srv.Shutdown(ctx)` yourself.

Verify concurrency safety with `make race` (`go test -race ./...`); the suite includes a hundreds-of-parallel-requests test and the connection-cap behaviors.
