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

When any logging is configured (globally or on any location), each backend's transport is wrapped in a `loggingTransport` that records the round-trip start time, response time, and backend status code into the per-request log record. The wrapper is a no-op for requests that have no log record on their context, so non-logged requests incur no overhead.

---

## Without Locations

When no `locations` are configured, all requests are distributed across the global backend pool using a single shared round-robin counter on the `Proxy` struct.

When `locations` are configured, the global pool is not used directly. Instead, each location defines its own pool via its `backends` list.
