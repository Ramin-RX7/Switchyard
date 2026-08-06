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

All eight core stages are pluggable. Each is an interface with a config-driven default, assigned to an exported field on `Proxy` (global) and/or `Location` (per-location).

| Stage | Interface | Default | Where you set it |
|-------|-----------|---------|------------------|
| Decide (routing) | `Decider { Decide(req Request) Decision }` | `DefaultDecider` | `p.Decider` |
| Act (side effect) | `Actor { Act(w, r, req Request, d Decision) }` | `DefaultActor` | `p.Actor` |
| Location detection | `Router { Match(req Request) *Location }` | `DefaultRouter` | `p.Router` |
| Backend selection | `BackendSelector { Select(pool []*Backend, req Request) *Backend }` | `RoundRobinSelector` | `p.Selector` (global pool), `loc.Selector` (per location) |
| Backend list | `BackendPool { Backends() []*Backend }` | `StaticPool` | `p.Pool` (global pool), `loc.Pool` (per location) |
| set_headers | `HeaderApplier { Apply(req Request, r *http.Request) }` | `TemplateHeaderSetter` | `p.Headers` (global), `loc.Headers` (per location) |
| Media serving | `StaticServer { Serve(w, r, req Request) }` | `FileServer` | `loc.Static` (per static location) |
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

**Custom router / actor / static server** — assign `p.Router` (e.g. host- or header-based routing), `p.Actor` (e.g. add retries or response rewriting), or `loc.Static` (e.g. serve from an embedded FS) the same way.

---

## Mounting inside an existing server

`Handler()` returns an `http.Handler`, so you can mount Switchyard in an existing mux or middleware chain instead of using `ListenAndServe`:

```go
mux := http.NewServeMux()
mux.Handle("/", p.Handler())
```
