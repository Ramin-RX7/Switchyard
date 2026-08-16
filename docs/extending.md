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

All eleven core stages are pluggable. Each is an interface with a config-driven default, assigned to an exported field on `Proxy` (global) and/or `Location` (per-location).

| Stage | Interface | Default | Where you set it |
|-------|-----------|---------|------------------|
| Decide (routing) | `Decider { Decide(req Request) Decision }` | `DefaultDecider` | `p.Decider` |
| Act (side effect) | `Actor { Act(w, r, req Request, d Decision) }` | `DefaultActor` | `p.Actor` |
| Location detection | `Router { Match(req Request) *Location }` | `DefaultRouter` | `p.Router` |
| Backend selection | `BackendSelector { Select(pool []*Backend, req Request) *Backend }` | `RoundRobinSelector` | `p.Selector` (global pool), `loc.Selector` (per location) |
| Backend list | `BackendPool { Backends() []*Backend }` | `StaticPool` | `p.Pool` (global pool), `loc.Pool` (per location) |
| set_headers | `HeaderApplier { Apply(req Request, r *http.Request) }` | `TemplateHeaderSetter` | `p.Headers` (global), `loc.Headers` (per location) |
| set_response_headers | `ResponseHeaderApplier { Apply(req Request, h http.Header) }` | `TemplateResponseHeaderSetter` | `p.ResponseHeaders` (global), `loc.ResponseHeaders` (per location) |
| Media serving | `StaticServer { Serve(w, r, req Request) }` | `FileServer` | `loc.Static` (per static location) |
| Access control | `AccessController { Allow(req Request) bool }` | `IPAccessControl` | `p.Access` (global), `loc.Access` (per location); `nil` = unrestricted |
| Response generation | `ResponseGenerator { Generate(w, r, req Request) }` | `TemplateResponder` | `loc.Responder` (response locations), `p.NotFound` / `p.BadGateway` / `p.MethodNotAllowed` / `p.Forbidden` (global error responses) |
| Logging | `Logger { Log(rec *LogRecord); NeedsRequestBody() bool; NeedsResponseBody() bool }` | `FormatLogger` | `p.Logger` (global), `loc.Logger` (per location) |

### Two levels: global and per-location

Most stages exist at two levels, matching how config stacks:

- **Global** — `p.Selector`, `p.Pool`, `p.Headers`, `p.ResponseHeaders`, `p.Logger`, plus the top-level `p.Decider`/`p.Actor`/`p.Router`. The global `Selector`/`Pool` are used only when no `locations` are configured.
- **Per-location** — `loc.Selector`, `loc.Pool`, `loc.Headers`, `loc.ResponseHeaders`, `loc.Logger`, `loc.Static` for each entry in `p.Locations`. Each location has its own instances, so overriding one location never affects the others.

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

**Extend set_response_headers** — the response-side mirror. Implement `ResponseHeaderApplier` to stamp headers onto the outgoing response (proxied, static, and generated alike); it is handed the response `http.Header` just before the status line is written, so it works with streaming and WebSocket upgrades. Set it globally via `p.ResponseHeaders` or per location via `loc.ResponseHeaders`; both are read **live**, so overrides after `New()` take effect. Embed the config-built `TemplateResponseHeaderSetter` to keep the configured `set_response_headers` and add your own:

```go
type withServerTiming struct {
	*sw.TemplateResponseHeaderSetter // the config-built default (may be nil)
}

func (h *withServerTiming) Apply(req sw.Request, out http.Header) {
	if h.TemplateResponseHeaderSetter != nil {
		h.TemplateResponseHeaderSetter.Apply(req, out) // keep all configured set_response_headers
	}
	out.Set("X-Server-Timing", "sw")
}

base, _ := p.ResponseHeaders.(*sw.TemplateResponseHeaderSetter) // nil if no set_response_headers in config
p.ResponseHeaders = &withServerTiming{TemplateResponseHeaderSetter: base}
```

**Custom backend pool** — implement `BackendPool` to feed selection a dynamic set (health-checked, service-discovery-backed). Set `p.Pool` (global) or `loc.Pool` (per location); it's called per request, so keep it cheap and concurrency-safe.

**Custom response generator** — implement `ResponseGenerator` to produce Switchyard's own responses (status + headers + body). The default `TemplateResponder` is config-driven; replace it to emit richer error payloads, structured JSON, metrics, etc. Assign it globally to `p.NotFound` (no-match 404), `p.BadGateway` (backend-unavailable/empty-pool 502), `p.MethodNotAllowed` (location matched but no backend accepts the request method, 405), or `p.Forbidden` (location matched but access control denied the client, 403), or per response-location to `loc.Responder`. These fields are read **live**, so overrides after `New()` take effect. This example swaps the 502 for a JSON error:

```go
type jsonError struct{}

func (jsonError) Generate(w http.ResponseWriter, _ *http.Request, req sw.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	fmt.Fprintf(w, `{"error":"upstream unavailable","path":%q}`, req.URI)
}

p.BadGateway = jsonError{} // no-match 404 → p.NotFound; 405 → p.MethodNotAllowed; 403 → p.Forbidden; response location → loc.Responder
```

(The `overflow` reject response stays config-driven; for full programmatic control over it, override the `Actor`.)

**Custom access control** — implement `AccessController` to gate requests by any predicate. The built-in default (`IPAccessControl`, driven by a [`whitelist`/`blacklist`](config-reference.md#whitelist--blacklist)) matches on the connecting peer's `RemoteAddr`; replace it to do token checks, geo lookups, or — the common case behind a load balancer — trust the real client IP from `X-Forwarded-For`. `Allow` returns `false` to deny (Switchyard then rejects with the `forbidden` 403 responder). Assign it **globally** via `p.Access` (checked before location matching, gating every request) or **per location** via `loc.Access` (`nil` means unrestricted at that tier); the two stack (a request must pass both). The example below gates one location; assign the same value to `p.Access` to gate the whole proxy instead:

```go
// xffAllowlist trusts the left-most X-Forwarded-For hop and allows only a fixed set.
type xffAllowlist struct {
	allowed map[string]bool
}

func (a *xffAllowlist) Allow(req sw.Request) bool {
	xff := req.Header.Get("X-Forwarded-For")
	ip := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
	if ip == "" {
		return false // no forwarded IP → fail closed
	}
	return a.allowed[ip]
}

for _, loc := range p.Locations {
	if loc.Path() == "/admin/" {
		loc.Access = &xffAllowlist{allowed: map[string]bool{"203.0.113.7": true}}
	}
}
```

To layer on top of the config-driven default instead of replacing it, embed `*sw.IPAccessControl` and delegate when your own check passes — the same "override one thing, keep the rest" pattern used above.

**Custom router / actor / static server** — assign `p.Router` (e.g. host- or header-based routing), `p.Actor` (e.g. add retries or response rewriting), or `loc.Static` (e.g. serve from an embedded FS) the same way.

---

## Mounting inside an existing server

`Handler()` returns an `http.Handler`, so you can mount Switchyard in an existing mux or middleware chain instead of using `ListenAndServe`:

```go
mux := http.NewServeMux()
mux.Handle("/", p.Handler())
```

---

## Hot reload (the `Server` type)

`Proxy.ListenAndServe` is the simple, non-reloadable path. For zero-downtime config reload — swapping the live `Proxy` in-process without restarting or dropping connections — use the exported **`Server`** type instead:

```go
srv := &sw.Server{
    Addr:    cfg.Listen,
    PidFile: "switchyard.pid",            // optional; "" disables the pid file
    Build: func() (*sw.Proxy, error) {    // called at start AND on every reload
        c, err := sw.LoadConfig(path)
        if err != nil {
            return nil, err
        }
        p, err := sw.New(c)
        if err != nil {
            return nil, err
        }
        // Re-apply any SDK overrides here so reload preserves them:
        p.Selector = &MySelector{}
        return p, nil
    },
}
srv.Run() // blocks: serves + handles reload/shutdown signals
```

**The `Build` contract.** `Build` is the key idea. It is invoked once at startup and again on **every reload**, so config changes are picked up and — crucially for SDK users — your stage overrides are **re-applied inside it**. Anything you set on `p` outside `Build` is lost on the next reload; put it in `Build`. If `Build` returns an error (e.g. an invalid new config), the reload is rejected and logged and the currently-running `Proxy` keeps serving — a bad reload never takes the server down.

**Reload semantics.** A reload builds a new `Proxy` and atomically swaps it in.

- **Graceful (default)** — in-flight requests keep running on the old `Proxy` until they finish; new requests use the new one. Zero downtime.
- **Force** — same swap, but in-flight requests are cancelled first (best-effort **503**).

See [architecture.md](architecture.md#configuration-reload-hot-reload) for the generation/atomic-swap mechanism and the two modes in full.

**Other exported members:**

| Member | Purpose |
|--------|---------|
| `Server.Run() error` | Blocks: serves and handles reload (`SIGHUP`/`SIGUSR2`) and shutdown (`SIGINT`/`SIGTERM`) signals. The turnkey behavior. |
| `Server.Start() (http.Handler, error)` | Initialize (calls `Build` once) and return the reloadable `http.Handler` to mount in your own `http.Server` — the reload-aware analog of `Proxy.Handler()`. |
| `Server.Reload(force bool) error` | Trigger a reload programmatically, in-process — no signal needed. |
| `sw.SignalReload(pidFile string, force bool) error` | Signal a **running** server (reads its pid file and sends `SIGHUP`/`SIGUSR2`). This is what the `switchyard reload [--force]` CLI uses. |
| `sw.ReadPidFile(path string) (int, error)` | Read the pid recorded by a running server. |

`Proxy.ListenAndServe(addr)` still exists for the simple, non-reloadable case; reach for `Server` only when you want hot reload.

---

## Backend health checks

Health checks ([config-reference.md#health](config-reference.md#health)) mark a backend unhealthy so retry's `skip_unhealthy` excludes it from selection. Two SDK touch-points:

- **The flag** — `Backend.SetHealthy(bool)` / `Backend.Healthy()` read and drive the health flag directly. A custom checker (querying a service registry, an orchestrator API, your own metrics) can call `SetHealthy` from any goroutine; it is safe for concurrent use and logs nothing (the built-in detectors log their own transitions). Reach a backend via `p.Pool.Backends()` or a location's `loc.Pool.Backends()`.
- **Active probers** — `Proxy.StartHealthChecks(ctx)` launches the config-driven active probe goroutines; each stops when `ctx` is cancelled. The `Server` type calls this automatically for every generation (bound to the generation's lifetime), so the turnkey binary and any `Server`-based SDK setup get active checks for free. If you mount a bare `Proxy.Handler()` without `Server`, call `StartHealthChecks(ctx)` yourself to enable active checks — pass a context you cancel on shutdown. Passive checks need no start-up (they observe traffic inline).

Health state is per-`*Backend` and in-memory, so it resets when a reload rebuilds the backends (as the round-robin counter does).

---

## Rate limiting: algorithm & storage

Rate limiting ([config-reference.md#rate-limiting](config-reference.md#rate-limiting)) exposes **two** independent SDK seams, both read live from the `Proxy`:

- **`p.RateLimiter`** — the algorithm. The default is `TokenBucketLimiter`. The interface is stateless across keys (all state lives in the store), so one instance serves every rule:

  ```go
  type RateLimiter interface {
      Allow(ctx context.Context, store RateLimitStore, key string, rate float64, burst int) (Allowance, error)
  }
  ```
  Replace it to change the whole limiting strategy (sliding window, GCRA, leaky bucket, a call to an external limiter service, …). Rule parameters (`rate`, `burst`, the composite key) are passed in per request; your algorithm keeps its per-key state in whatever `store` it is handed.

- **`p.RateLimitStore`** — the storage, behind one **uniform interface** regardless of backend:

  ```go
  type RateLimitStore interface {
      Get(ctx, key) (state []byte, exists bool, now time.Time, err error)
      SetIfAbsent(ctx, key string, state []byte, ttl time.Duration) (ok bool, err error)
      CompareAndSwap(ctx, key string, oldState, newState []byte, ttl time.Duration) (ok bool, err error)
  }
  ```
  The default is in-memory (`NewMemoryRateLimitStore`, no external dependency). A Redis/memcached store implements the same three primitives — `Get` (with the store's clock), `SetIfAbsent` (SET NX PX), and `CompareAndSwap` (a WATCH/MULTI or Lua CAS) — and drops straight in. The value is an opaque `[]byte`, so the algorithm owns its state shape and the store need not know the algorithm.

The two compose freely: keep the default token bucket but back it with Redis (swap only the store), or keep in-memory storage but change the algorithm (swap only the limiter).

**Reload & persistence.** The default in-memory store is rebuilt per generation, so counters reset on reload (like the round-robin/health state). A store you want to survive reloads (e.g. a Redis client) should be constructed **once** and returned as the same instance from every `Server.Build` call, then assigned to `p.RateLimitStore` inside `Build`. Across multiple processes, an in-memory store counts per-process (effective limit N× the configured value); use a shared store for a fleet-wide limit.

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

- **Method-aware selection.** `Backend.Accepts(method string) bool` reports whether a backend accepts a given HTTP method (always true when the backend has no `methods` restriction). A custom `BackendSelector`/`Decider` can use it to stay method-correct. The built-in path already filters the pool by method before selection, and `overflow: reroute` only ever considers method-eligible backends. See [routing.md#method-routing](routing.md#method-routing).

- **Custom over-capacity response.** Beyond the `overflow` status/body config, an SDK user can override the `Actor` for full control (custom body, headers, metrics) when a cap is hit.

- **Graceful shutdown.** `ListenAndServe` traps `SIGINT`/`SIGTERM`, stops accepting new connections, and drains in-flight requests (15s deadline) before returning `nil`. If you use `Handler()` in your own server, call `srv.Shutdown(ctx)` yourself.

Verify concurrency safety with `make race` (`go test -race ./...`); the suite includes a hundreds-of-parallel-requests test and the connection-cap behaviors.
