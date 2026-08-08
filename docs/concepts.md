# Concepts

This glossary defines the terms used throughout Switchyard's configuration, documentation, and source code.

---

## Backend

A configured upstream server that Switchyard can forward requests to. Each backend has an `id` (a short name used to reference it elsewhere in the config) and a `url` (the full address including scheme and host). Backends are the targets of all proxying. See [backends.md](backends.md).

## Location

A path-based routing block, similar to nginx's `location` directive. Locations are defined in an ordered list; the first one whose `path` matches the incoming request URI wins. A location can forward to a private pool of backends (`type: "proxy"`), serve files from disk (`type: "static"`), or return a Switchyard-generated canned response (`type: "response"`). When no locations are configured, all requests use global round-robin over all backends. See [routing.md](routing.md).

## Decision

The output of Switchyard's passive routing stage. A `Decision` records what should happen to a request — which action to take (`forward`, `static`, `respond`, or `reject`), which backend was selected, and which location matched — without actually doing anything. No network I/O happens during decision-making. See [architecture.md](architecture.md).

## Action

One of four values that a `Decision` can carry:

- **`forward`** — proxy the request to a backend
- **`static`** — serve a file from disk
- **`respond`** — return a Switchyard-generated response (a `type: "response"` location — see [Response Generator](#response-generator))
- **`reject`** — return an HTTP error to the client (404 when no location matched, 502 when a proxy location has no backends)

## Request Snapshot

The immutable internal `Request` value captured at the very start of every request, before any routing or forwarding happens. All inspection logic (routing, logging, variable resolution, header injection) reads from this snapshot, not from the live `*http.Request`. Headers are cloned at capture time so that later mutations made by `set_headers` never affect the logged copy or variable values. See [architecture.md](architecture.md).

## Variable

An nginx-style `$name` placeholder that resolves to a value derived from the request snapshot at runtime. Variables can be referenced in `set_headers` value templates and in log format strings (via `{var.NAME}`). All available variables are listed in [variables.md](variables.md).

## Template

A string that contains one or more variable references. Templates support two syntaxes: `$name` and `${name}`. They are compiled once at startup; an unknown variable name causes Switchyard to exit immediately rather than producing silently wrong output at runtime. Templates are used in `set_headers` values.

## Response Generator

The abstract stage that produces Switchyard's *own* HTTP responses — a status, a set of headers, and a body, with `$variable` substitution applied to the headers and body. It backs the `type: "response"` location and the built-in error responses (502 backend-unavailable, 404 no-match) as well as the `overflow` reject response. Each of these is overridable: via config (`response`, `backend_error`, `not_found`, `overflow`) and, for SDK users, by replacing the generator (`loc.Responder`, `p.BadGateway`, `p.NotFound`). See [config-reference.md#response](config-reference.md#response).

## Round-Robin

The backend selection algorithm. Each pool of backends (the global pool or a location's own pool) maintains an atomic counter. The counter is incremented on every request and taken modulo the pool size to pick a backend. The counter is lock-free (`atomic.Uint64`), so no synchronization overhead occurs under concurrent load.

## Stacking

The behavior where location-level settings *augment* global settings rather than replacing them. Specifically:

- A matched request fires **both** the global logger and the location's own logger (if each is configured). Neither overrides the other.
- A matched request has global `set_headers` applied first, then the location's `set_headers` on top. When the same header name appears in both, the location value wins; all other global headers are retained.

Stacking means global configuration is a baseline that every request goes through. Location configuration adds to it for matched requests.

## Connection Limit (`max_connections`)

A cap on the number of **concurrent in-flight requests** to a scope, enforced by a counting semaphore. It can be set independently on the whole project, a location, and a backend; the caps are **nested** — a request must have a free slot at *every* applicable scope to proceed. "Connections" here means concurrent requests (with keep-alive, actual TCP connections may be fewer). A backend's cap and live count are exposed (`Backend.MaxConns`/`InFlight`) so a custom selector can distribute by capacity. See [config-reference.md](config-reference.md#connection-limits--timeouts).

## Overflow

What happens when a `max_connections` cap is reached. The `overflow` policy chooses `reject` (fail immediately), `queue` (wait a bounded time for a slot, then reject), or `reroute` (when a backend is full, try the other backends in the pool before falling back to queue/reject). It also configures the reject response (`status`, `headers`, `body` — the body and header values may contain [variables](variables.md)), and governs every scope's cap, including the project-wide one. See [config-reference.md](config-reference.md#overflow).

## Fail-Fast

Switchyard validates the entire configuration at startup before accepting any traffic. If a backend URL is invalid, a regex fails to compile, a static root directory does not exist, a variable name is unknown, a log format field is misspelled, a duration is malformed, or a limit is negative, Switchyard exits immediately with an error. No partial startup, no lazy validation.
