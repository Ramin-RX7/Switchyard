# Features

A complete inventory of what Switchyard implements today. Each feature is marked with:

- **⚙️ Config** — configurable from `switchyard.json` (no code).
- **🧩 SDK** — a Go interface you can swap to replace the default logic entirely.

Architecturally, every request flows through a three-stage pipeline — **Capture** (an immutable `Request` snapshot) → **Decide** (pure routing, no I/O) → **Act** (the only side-effecting stage). All behavior sits behind **11 pluggable stages** (an interface plus a config-driven default), so the config-only binary and the SDK run off identical code. `New(cfg)` reproduces the turnkey binary's behavior exactly; SDK overrides are additive.

See [config-reference.md](config-reference.md) for every JSON field and [extending.md](extending.md) for the SDK.

---

## 1. Reverse proxy / backends

Forwards requests to upstream servers; each backend wraps `httputil.ReverseProxy` with lock-free round-robin selection (`atomic.Uint64`), automatic `X-Forwarded-For`, a per-backend tuned `http.Transport`, and a custom error handler (unreachable upstream → configurable **502**).

- **⚙️ Config:** `backends[]` (`id`, `url`); per-backend `max_connections`, `timeouts`, `transport`, `disable_keep_alive`, `methods`.
- **🧩 SDK:** `BackendSelector` (default `RoundRobinSelector`) via `p.Selector` / `loc.Selector`; `BackendPool` (default `StaticPool`) via `p.Pool` / `loc.Pool` — e.g. health-checked or service-discovery pools. Global transport override via `p.Transport`. `Backend.MaxConns()` / `InFlight()` / `Accepts()` are exposed for capacity- and method-aware selectors.

## 2. Location routing (nginx-style)

Ordered `locations`, first-match-wins; prefix or **regex** matching. Three location types: `proxy` (own backend pool + independent round-robin), `static` (serve files), `response` (canned response). No match → configurable **404**.

- **⚙️ Config:** `locations[]` with `path`, `regex`, `type`, `backends`, `root`, `strip_prefix`, `response`.
- **🧩 SDK:** `Router` (default `DefaultRouter`) via `p.Router` for host/header-based routing; whole-routing override via `Decider` (`p.Decider`).

## 3. Multiple backends per location

Each location carries its own pool and its own round-robin counter, so locations sharing a backend rotate independently.

- **⚙️ Config:** `locations[].backends: ["api1", "api2"]`.
- **🧩 SDK:** per-location `loc.Pool` / `loc.Selector`.

## 4. Method-based routing

After path→location, backends are filtered by HTTP method; each backend may declare accepted `methods` (empty = any, matched case-insensitively). If no backend accepts the method → **405** with an auto-generated `Allow` header. Reroute stays within method-eligible backends.

- **⚙️ Config:** `backends[].methods`; `method_not_allowed` response.
- **🧩 SDK:** `Backend.Accepts(method)` for method-aware selectors; `p.MethodNotAllowed` (ResponseGenerator).

## 5. IP access control (whitelist / blacklist)

Allow/deny by client IP (single IP or CIDR, IPv4/IPv6) at **two tiers**: a project-wide top-level `whitelist`/`blacklist` and a per-location one. Within a tier: blacklist wins; a non-empty whitelist is allow-list-only; empty = unrestricted. The tiers **stack (AND)** — a request must pass both. The global tier is evaluated first, before location matching (so it gates paths that match no location); the per-location tier right after the location matches, so it applies to proxy, static, and response locations alike. Denial at either → configurable **403**.

- **⚙️ Config:** top-level `whitelist` / `blacklist` (global); `locations[].whitelist` / `blacklist` (per-location); `forbidden` response.
- **🧩 SDK:** `AccessController` (default `IPAccessControl`) via `p.Access` (global) / `loc.Access` (per-location) — e.g. an `X-Forwarded-For`-aware, token, or geo check.

## 6. Connection limits & overflow (backpressure)

`max_connections` (a concurrent in-flight cap) is enforced independently and nested at **project / location / backend** scopes via counting semaphores. Over-capacity behavior is configurable.

- **⚙️ Config:** `max_connections` (top-level / backend / location); `overflow` = `strategy` (`reject` | `queue` | `reroute`), `queue_timeout`, plus a configurable reject response (`status`, `headers`, variable-capable `body`).
- **🧩 SDK:** `p.MaxInFlight`; a capacity-aware `BackendSelector` (runnable [`examples/least-loaded`](../examples/least-loaded/main.go)); a custom `Actor` for full over-capacity control.

## 7. Timeout handling & transport tuning

Upstream and client-facing timeouts, plus keep-alive pool tuning. Durations are plain **integer seconds** in JSON.

- **⚙️ Config:** `timeouts` (`request`, `tls_handshake`) at project + per-backend; `transport` (`max_idle_conns`, `max_idle_conns_per_host`, `idle_conn_timeout`); `server` (`read_header_timeout`, `read_timeout`, `write_timeout`, `idle_timeout`).
- **🧩 SDK:** `p.Transport` global `http.RoundTripper` override; per-request deadline via the Actor.

## 8. Response generation (Switchyard-produced responses)

An abstract generator produces status + headers + body with `$variable` substitution. It powers the `type: "response"` location **and** every built-in error response, each with a sensible default and individually overridable.

- **⚙️ Config:** `locations[].response`; top-level `backend_error` (502), `not_found` (404), `method_not_allowed` (405), `forbidden` (403), and the `overflow` reject.
- **🧩 SDK:** `ResponseGenerator` (default `TemplateResponder`) via `loc.Responder`, and `p.NotFound` / `p.BadGateway` / `p.MethodNotAllowed` / `p.Forbidden`.

## 9. Variables

nginx-style `$name` / `${name}` placeholders resolved from the request snapshot: `$remote_addr`, `$remote_port`, `$host`, `$scheme`, `$request_method`, `$request_uri`, `$uri`, `$args` / `$query_string`, `$http_*` (any request header), `$time_iso8601`, `$time_unix`. Validated at startup (an unknown variable fails fast).

- **⚙️ Config:** usable in `set_headers`, `set_response_headers`, `response`/error bodies and headers, and log `{var.NAME}` fields.
- **🧩 SDK:** custom appliers / responders can compute arbitrary values.

## 10. Request header injection (`set_headers`)

Sets headers on the request before forwarding, with variables. Global and location `set_headers` **stack** (location wins on a conflict, other globals retained); `Host` is special-cased.

- **⚙️ Config:** `set_headers` (top-level + per-location).
- **🧩 SDK:** `HeaderApplier` (default `TemplateHeaderSetter`) via `p.Headers` / `loc.Headers` (runnable [`examples/request-id`](../examples/request-id/main.go)).

## 11. Response header injection (`set_response_headers`)

The response-side mirror: sets headers on the client response (proxied, static, generated, and error responses), with the same variables, Set/override + global/location stacking. Streaming- and WebSocket-safe (the lazy writer forwards `Flush`/`Hijack`).

- **⚙️ Config:** `set_response_headers` (top-level + per-location).
- **🧩 SDK:** `ResponseHeaderApplier` (default `TemplateResponseHeaderSetter`) via `p.ResponseHeaders` / `loc.ResponseHeaders`.

## 12. Logging

Optional structured access logging with a `{field}` / `{group.param}` format, compiled and validated at startup. Global and location loggers both fire; request/response bodies are buffered only when the format references them.

- **⚙️ Config:** `logging` (`format`, `outputs`: console/file, `file`) at top-level + per-location; timing fields (`request_duration`, `app_duration`, and more).
- **🧩 SDK:** `Logger` (default `FormatLogger`) via `p.Logger` / `loc.Logger` — emit JSON, metrics, or spans (`NeedsRequestBody` / `NeedsResponseBody` to skip buffering).

## 13. Hot configuration reload (zero-downtime)

In-process atomic Proxy swap. **Graceful:** in-flight requests finish on the old config, new requests use the new one. **Force:** in-flight requests are cancelled (best-effort 503). An invalid new config is rejected and the running config keeps serving (fail-safe). The listen address and `server` timeouts need a full restart; everything else reloads live.

- **⚙️ Config / CLI:** `switchyard reload [--force]` (via the pid file, default `switchyard.pid`; `-pidfile` to relocate), or `SIGHUP` / `SIGUSR2`; `make reload` / `make force-reload`.
- **🧩 SDK:** `Server{Addr, PidFile, Build}` with `Run()` / `Start()` / `Reload(force)`; plus `SignalReload` and `ReadPidFile`. `Build` is re-invoked on every reload so SDK overrides re-apply.

## 14. Lifecycle: fail-fast validation & graceful shutdown

The entire config is validated before serving (a bad URL, regex, static root, variable, duration, or negative limit exits immediately). On `SIGINT` / `SIGTERM` the server stops accepting and drains in-flight requests (15s) before exiting, and removes the pid file.

- **⚙️ Config:** implicit (every field is validated).
- **🧩 SDK:** `Actor` (default `DefaultActor`) for retries/response rewriting; mount via `Handler()` in your own server and call `srv.Shutdown` yourself.

---

## The 11 pluggable stages at a glance

| Stage | Interface | Default | Config | Set via |
|-------|-----------|---------|--------|---------|
| Decide (routing) | `Decider` | `DefaultDecider` | — | `p.Decider` |
| Act (side effects) | `Actor` | `DefaultActor` | — | `p.Actor` |
| Location detection | `Router` | `DefaultRouter` | `locations` | `p.Router` |
| Backend selection | `BackendSelector` | `RoundRobinSelector` | (per pool) | `p.Selector`, `loc.Selector` |
| Backend pool | `BackendPool` | `StaticPool` | `backends` | `p.Pool`, `loc.Pool` |
| Request headers | `HeaderApplier` | `TemplateHeaderSetter` | `set_headers` | `p.Headers`, `loc.Headers` |
| Response headers | `ResponseHeaderApplier` | `TemplateResponseHeaderSetter` | `set_response_headers` | `p.ResponseHeaders`, `loc.ResponseHeaders` |
| Static serving | `StaticServer` | `FileServer` | `type: "static"`, `root` | `loc.Static` |
| Access control | `AccessController` | `IPAccessControl` | `whitelist` / `blacklist` | `p.Access`, `loc.Access` |
| Response generation | `ResponseGenerator` | `TemplateResponder` | `response`, error responses | `loc.Responder`, `p.NotFound` / `BadGateway` / `MethodNotAllowed` / `Forbidden` |
| Logging | `Logger` | `FormatLogger` | `logging` | `p.Logger`, `loc.Logger` |

**Two ways to run, one codebase:** the config-only turnkey binary (`./Switchyard -config switchyard.json`) and the SDK (import the package, override any stage, compile your own binary). See [architecture.md](architecture.md) and [extending.md](extending.md).
