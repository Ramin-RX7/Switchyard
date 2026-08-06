# Architecture

## The Three-Stage Pipeline

Every HTTP request Switchyard receives passes through three stages in order. These stages are kept strictly separate by design: the first two produce values, the third acts on them.

```
Incoming HTTP request
        │
        ▼
┌───────────────────┐
│  1. Capture       │  captureRequest()  →  Request
│     request.go    │  (immutable snapshot)
└────────┬──────────┘
         │
         ▼
┌───────────────────┐
│  2. Decide        │  p.Decider.Decide()  →  Decision
│     decide.go     │  (pure, no I/O; pluggable)
└────────┬──────────┘
         │
         ▼
┌───────────────────┐
│  3. Act           │  Proxy.handleRequest() → p.Actor.Act()
│     actor.go      │  (all side effects happen here; pluggable)
└───────────────────┘
        │
        ├── ActionForward → backend.proxy.ServeHTTP()
        ├── ActionStatic  → http.FileServer
        └── ActionReject  → http.Error()
```

---

### Stage 1 — Capture (`request.go`)

**Function:** `captureRequest(r *http.Request) Request`

Converts the live `*http.Request` into an immutable `Request` value. This snapshot is what all subsequent stages read — never the raw request directly.

Key behaviors:
- Detects scheme from TLS presence (`https` if `r.TLS != nil`, else `http`)
- **Clones headers** so that `set_headers` mutations on the forwarded request never affect the logged copy or variable resolution
- Records `ReceivedAt` timestamp
- Constructs the full URL string from scheme, host, and request URI

After this stage, the `Request` value is never modified.

---

### Stage 2 — Decide (`decide.go`)

**Interface:** `Decider { Decide(req Request) Decision }` — default `DefaultDecider`

A pure function: no network I/O, no file access, no side effects. Returns a `Decision` describing what should happen. Routing logic lives exclusively here. `Proxy.handle` calls `p.Decider.Decide(req)`, so the whole stage is **pluggable** — SDK users can replace or extend it (see [extending.md](extending.md)).

The default (`DefaultDecider`) has two paths:

**With locations configured** — matches `req.Path` against each location in order (first match wins):
- A `proxy` location: selects a backend from its own pool via `loc.Selector` (default round-robin) → `ActionForward`
- A `static` location: → `ActionStatic`
- No match: → `ActionReject` with status 404
- A `proxy` location matched but its pool is empty: → `ActionReject` with status 502

**Without locations** — selection over the global pool via `p.Selector` (default round-robin):
- At least one backend: → `ActionForward`
- No backends at all: → `ActionReject` with status 502

The `Decision` records the chosen `Action`, the selected `*Backend` (if any), and the matched `*Location` (if any). Downstream stages read from this value. Backend selection itself is a nested pluggable stage — the `BackendSelector` interface — so you can change load balancing without touching the decider.

---

### Stage 3 — Act (`actor.go`, orchestrated by `proxy.go`)

**Functions:** `Proxy.handleRequest()` → `p.Actor.Act()` (default `DefaultActor` in `actor.go`)

The only stage that touches the outside world, and it is **pluggable** via the `Actor` interface. `handleRequest` (in `proxy.go`) sets up logging and delegates the side effect to `p.Actor.Act`. Two paths depending on whether logging is configured:

**Logging path** (when any logger applies):
1. Wraps the `http.ResponseWriter` in a `statusWriter` to capture status and optionally buffer the response body
2. Attaches a `LogRecord` to the request context so the `loggingTransport` on backends can record round-trip timing
3. Optionally reads and restores the request body if any logger references `request_body`
4. Calls `p.Actor.Act()` to perform the action
5. After the response is complete, renders the shared `LogRecord` through every applicable logger (global and location)

**Fast path** (no logging configured):
- Logs a single built-in operational line
- Calls `p.Actor.Act()` directly with the original `ResponseWriter`

**`DefaultActor.Act()`** performs three things before forwarding:
1. Calls `applyStackedHeaders()` — global `HeaderApplier` then location `HeaderApplier`; same key → location wins
2. Dispatches on the decision's action:
   - `ActionForward` → `backend.proxy.ServeHTTP(w, r)`
   - `ActionStatic` → `loc.Static.Serve(w, r, req)` (default `FileServer` strips prefix and serves)
   - `ActionReject` → `http.Error(w, reason, status)`

---

## Why This Separation Matters

**Testability.** `decide` has no side effects, so it can be tested with plain function calls and no HTTP infrastructure. The inputs are a `Request` value; the output is a `Decision` value.

**Clarity.** All routing logic is in one place (the `Decider`). All network I/O is in one place (the `Actor`). A reader never has to check whether routing might cause a side effect or whether a side effect might change routing.

**Extension safety.** Adding a new backend type or action type follows a fixed pattern: one new constant, one new case in `DefaultDecider.Decide`, one new case in `DefaultActor.Act`. Each step is independently verifiable.

---

## Extending Switchyard

There are two audiences:

**SDK users** import Switchyard and override a stage without editing its source. Each pluggable stage is an interface with a config-driven default; you assign your own implementation (or embed the default and override one method) before serving. See [extending.md](extending.md) for the full surface and worked examples.

**Contributors** adding a new action type to the core follow a fixed pattern, in order:

1. Add a new `Action` constant in `decision.go`
2. Add a compiled location kind in `location.go` (`compileLocations`) if the new action requires a new location type
3. Add a case in `DefaultDecider.Decide` (`decide.go`) that returns a `Decision` with the new action
4. Add a case in `DefaultActor.Act` (`actor.go`) that performs the side effect

Each step can be compiled and reviewed in isolation before moving to the next.

---

## Package Layout

Switchyard is an importable library (`package switchyard`, import path `github.com/Ramin-RX7/Switchyard/switchyard`) living in the `switchyard/` directory. The `cmd/switchyard` binary is a thin consumer of it — the config-only turnkey mode. Both usage modes run off the same code.

| File | Responsibility |
|------|---------------|
| `cmd/switchyard/main.go` | Turnkey binary: flag parsing → `LoadConfig` → `New` → `ListenAndServe` |
| `switchyard/config.go` | JSON config schema and `LoadConfig` |
| `switchyard/request.go` | Stage 1: request snapshot (`captureRequest`) |
| `switchyard/decision.go` | `Decision` type and `Action` constants |
| `switchyard/decide.go` | Stage 2: `Decider` interface + `DefaultDecider` |
| `switchyard/router.go` | `Router` interface + `DefaultRouter` (location detection) |
| `switchyard/selector.go` | `BackendSelector` interface + `RoundRobinSelector` |
| `switchyard/pool.go` | `BackendPool` interface + `StaticPool` (backend list) |
| `switchyard/actor.go` | Stage 3: `Actor` interface + `DefaultActor` (side effects) |
| `switchyard/static.go` | `StaticServer` interface + `FileServer` (media serving) |
| `switchyard/proxy.go` | `Proxy` struct, `New`, `Handler`/`ListenAndServe`, `handleRequest` |
| `switchyard/backend.go` | Backend construction, error handling |
| `switchyard/location.go` | Location compilation, path matching |
| `switchyard/vars.go` | Variable resolution + `HeaderApplier`/`TemplateHeaderSetter` (set_headers) |
| `switchyard/logging.go` | `Logger` interface + `FormatLogger`, `loggingTransport`, `statusWriter` |
