# Backends

A backend is a configured upstream server that Switchyard can proxy requests to. Backends are defined once in the global `backends` array and referenced by ID from location blocks.

---

## Configuration

```json
{
    "backends": [
        { "id": "api1", "url": "http://127.0.0.1:9001" },
        { "id": "api2", "url": "http://127.0.0.1:9002" }
    ]
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `id` | string | value of `url` | Unique name used to reference this backend from locations |
| `url` | string | required | Full upstream URL including scheme and host |
| `max_connections` | int | `0` (unlimited) | Cap on concurrent in-flight requests to this backend |
| `timeouts` | object | project value | Per-backend `request` / `tls_handshake` timeouts |
| `transport` | object | project value | Per-backend idle-pool tuning |
| `disable_keep_alive` | bool | `false` | Force a fresh connection per request |

The last four are documented in full in [config-reference.md#connection-limits--timeouts](config-reference.md#connection-limits--timeouts).

### Uniqueness

Both `id` and `url` must be unique across all backends. Duplicates are rejected at startup.

### ID defaults

If `id` is omitted, it defaults to the backend's `url`. When referencing backends by ID in location blocks, use the explicit ID if one was set, otherwise use the full URL string.

---

## Selection: Round-Robin

When a request is routed to a pool of backends (either the global pool or a location's own pool), Switchyard selects the backend using **round-robin**: an atomic counter is incremented per request and taken modulo the pool size.

The counter is an `atomic.Uint64`, so no lock is acquired and there is no contention under concurrent requests. Each location maintains its own independent counter, so two locations sharing a backend do not interfere with each other's rotation.

---

## Error Handling

Backend errors — unreachable host, connection reset, timeout — are caught by a custom `ErrorHandler` installed on each backend's reverse proxy. The handler:

1. Logs the error in Switchyard's operational log format
2. Returns **502 Bad Gateway** to the client

This ensures backend failures produce a consistent, predictable response rather than the default Go behavior (which can produce inconsistent error pages).

---

## Implementation

Each configured backend is compiled into an internal `backend` struct wrapping a `httputil.ReverseProxy`. The reverse proxy handles:

- Connection pooling and keep-alives
- HTTP/1.1 and HTTP/2 proxying
- `X-Forwarded-For` header maintenance (added automatically; no need to set it in `set_headers`)
- Hop-by-hop header stripping

**Per-backend tuned transport.** `New` builds each backend its own `http.Transport` from the merged transport settings (backend override → project default → built-in). The defaults raise the idle-connection limits well above the standard-library defaults, because `http.DefaultTransport`'s `MaxIdleConnsPerHost` of **2** throttles a reverse proxy under concurrent load.

These are **configurable in JSON** at the project level (defaults) and per backend (override) — see [config-reference.md#transport](config-reference.md#transport). SDK users can alternatively replace the transport for all backends via `Proxy.Transport` before serving (see [extending.md](extending.md#concurrency--tuning)).

| Setting | Default | Purpose |
|---------|---------|---------|
| `MaxIdleConnsPerHost` | `256` | Idle keep-alive connections kept **per backend** (stdlib default is 2 — the key fix). |
| `MaxIdleConns` | `512` | Idle keep-alive connections kept across all backends. |
| `IdleConnTimeout` | `90s` | How long an idle connection is pooled before closing. |
| dial timeout | `5s` | Max time to establish a TCP connection to a backend. |
| dial keep-alive | `30s` | TCP keep-alive probe interval. |
| `TLSHandshakeTimeout` | `10s` | Max TLS handshake time for `https` backends. |
| `ExpectContinueTimeout` | `1s` | Wait for `100 Continue` before sending the body. |
| `ForceAttemptHTTP2` | `true` | Negotiate HTTP/2 to backends when available. |
| `Proxy` | env | Honors `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`. |

**Left unbounded by default** (Go zero value = off), so be aware:

- The transport's `MaxConnsPerHost` is unset — the transport itself won't cap connections. Concurrency is bounded instead by the semaphore behind `max_connections` (project/location/backend) and the [overflow](config-reference.md#overflow) policy.
- `ResponseHeaderTimeout` is unset. To bound how long a request may take against a slow/hung upstream, set `timeouts.request` (project or per backend) — off by default so long downloads, SSE, and WebSocket upgrades are not cut off.

Each backend's transport is wrapped by a thin `proxyTransport`: it delegates to the backend's transport (or a global `Proxy.Transport` override) and records the round-trip start time, response time, and backend status code into the per-request log record. The recording is a no-op for requests with no log record on their context, so non-logged requests incur no overhead.

---

## Connection Limits & Capacity

Each backend can cap its concurrent in-flight requests via `max_connections` (a semaphore; `0` = unlimited). Locations and the whole project have their own independent caps — a request must have room at every scope, or the [overflow](config-reference.md#overflow) policy (reject / queue, with a configurable response) applies. See [config-reference.md#connection-limits--timeouts](config-reference.md#connection-limits--timeouts).

A backend exposes its capacity so a **custom `BackendSelector` can distribute by load** (the default round-robin ignores it):

- `Backend.MaxConns() int` — the configured cap (0 = unlimited).
- `Backend.InFlight() int` — requests currently being served by this backend.

See [extending.md](extending.md#concurrency--tuning) for a capacity-aware selector.

---

## Without Locations

When no `locations` are configured, all requests are distributed across the global backend pool using a single shared round-robin counter on the `Proxy` struct.

When `locations` are configured, the global pool is not used directly. Instead, each location defines its own pool via its `backends` list.
