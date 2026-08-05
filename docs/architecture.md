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
│  2. Decide        │  Proxy.decide()    →  Decision
│     proxy.go      │  (pure, no I/O)
└────────┬──────────┘
         │
         ▼
┌───────────────────┐
│  3. Act           │  Proxy.handleRequest() + Proxy.act()
│     proxy.go      │  (all side effects happen here)
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

### Stage 2 — Decide (`proxy.go`)

**Function:** `Proxy.decide(req Request) Decision`

A pure function: no network I/O, no file access, no side effects. Returns a `Decision` describing what should happen. Routing logic lives exclusively here.

Two paths:

**With locations configured** — matches `req.Path` against each location in order (first match wins):
- A `proxy` location: selects a backend from its own pool via round-robin → `ActionForward`
- A `static` location: → `ActionStatic`
- No match: → `ActionReject` with status 404
- A `proxy` location matched but its pool is empty: → `ActionReject` with status 502

**Without locations** — global round-robin over all backends:
- At least one backend: → `ActionForward`
- No backends at all: → `ActionReject` with status 502

The `Decision` records the chosen `Action`, the selected `*backend` (if any), and the matched `*location` (if any). Downstream stages read from this value.

---

### Stage 3 — Act (`proxy.go`)

**Functions:** `Proxy.handleRequest()` → `Proxy.act()`

The only stage that touches the outside world. Two paths depending on whether logging is configured:

**Logging path** (when any logger applies):
1. Wraps the `http.ResponseWriter` in a `statusWriter` to capture status and optionally buffer the response body
2. Attaches a `logRecord` to the request context so the `loggingTransport` on backends can record round-trip timing
3. Optionally reads and restores the request body if any logger references `request_body`
4. Calls `act()` to perform the action
5. After the response is complete, renders the shared `logRecord` through every applicable logger (global and location)

**Fast path** (no logging configured):
- Logs a single built-in operational line
- Calls `act()` directly with the original `ResponseWriter`

**`act()`** performs three things before forwarding:
1. Calls `applyStackedHeaders()` — global `set_headers` then location `set_headers`; same key → location wins
2. Dispatches on the decision's action:
   - `ActionForward` → `backend.proxy.ServeHTTP(w, r)`
   - `ActionStatic` → strips prefix from path, calls `loc.fileServer.ServeHTTP(w, r)`
   - `ActionReject` → `http.Error(w, reason, status)`

---

## Why This Separation Matters

**Testability.** `decide` has no side effects, so it can be tested with plain function calls and no HTTP infrastructure. The inputs are a `Request` value; the output is a `Decision` value.

**Clarity.** All routing logic is in one place (`decide`). All network I/O is in one place (`act`). A reader never has to check whether routing might cause a side effect or whether a side effect might change routing.

**Extension safety.** Adding a new backend type or action type follows a fixed pattern: one new constant, one new case in `decide`, one new case in `act`. Each step is independently verifiable.

---

## Extending Switchyard: Adding a New Action Type

The canonical extension pattern, in order:

1. Add a new `Action` constant in `decision.go`
2. Add a compiled location kind in `location.go` (`compileLocations`) if the new action requires a new location type
3. Add a case in `Proxy.decide` (`proxy.go`) that returns a `Decision` with the new action
4. Add a case in `Proxy.act` (`proxy.go`) that performs the side effect

Each step can be compiled and reviewed in isolation before moving to the next.

---

## File Map

| File | Responsibility |
|------|---------------|
| `main.go` | Entry point, server setup, config loading |
| `config.go` | JSON config schema and loading |
| `request.go` | Stage 1: request snapshot (`captureRequest`) |
| `decision.go` | `Decision` type and `Action` constants |
| `proxy.go` | Stage 2 (`decide`) + Stage 3 (`handleRequest`, `act`) |
| `backend.go` | Backend construction, round-robin, error handling |
| `location.go` | Location compilation, path matching, static serving |
| `vars.go` | Variable resolution and `set_headers` template engine |
| `logging.go` | Log format compilation, output writers, `loggingTransport`, `statusWriter` |
