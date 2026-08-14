# set_headers — Header Injection

Switchyard has two mirror-image header-injection features:

- **[`set_headers`](#request-headers-set_headers)** — sets headers on the **request** forwarded to a backend (analogous to nginx's `proxy_set_header`).
- **[`set_response_headers`](#response-headers-set_response_headers)** — sets headers on the **response** returned to the client (analogous to nginx's `add_header`).

They share the same shape (a header-name → [template](concepts.md#template) map), the same [variable](variables.md) support, and the same global/location [stacking](concepts.md#stacking) rules. The only difference is the direction and the `Host` special case (request-only).

---

## Request headers (`set_headers`)

`set_headers` injects or overrides HTTP headers on requests before they are forwarded to a backend. It is analogous to nginx's `proxy_set_header` directive.

It can be set at two levels:
- **Global** (`set_headers` at the top level) — applies to every proxied request
- **Location** (`set_headers` inside a location block) — applies only to requests matched by that location, stacked on top of the global headers

---

## Configuration

`set_headers` is a JSON object where each key is a header name and each value is a [template](concepts.md#template) string.

```json
{
    "set_headers": {
        "X-Real-IP":         "$remote_addr",
        "X-Forwarded-Proto": "$scheme",
        "X-Forwarded-Host":  "$host",
        "X-Static-Header":   "literal-value"
    }
}
```

Values may contain [variable](variables.md) references using `$name` or `${name}` syntax. Literal text is allowed. A value with no `$` is passed through as-is.

---

## Variable Substitution

All [variables](variables.md) are available in header values. They resolve against the [request snapshot](concepts.md#request-snapshot) — the original incoming request before any header mutation.

```json
"set_headers": {
    "X-Client-IP":   "$remote_addr",
    "X-Client-Port": "$remote_port",
    "X-Method":      "$request_method",
    "X-Origin":      "${scheme}://${host}"
}
```

Variable names are validated at startup. An unknown variable name causes an immediate exit.

---

## Special Case: `Host` Header

Setting the `Host` header is special in Go's HTTP implementation. When `"Host"` is a key in `set_headers`, Switchyard updates `r.Host` directly in addition to the header map. This ensures the upstream receives the correct `Host` value.

```json
"set_headers": {
    "Host": "$host"
}
```

---

## Stacking

When both a global `set_headers` and a location `set_headers` are configured, they are applied in order:

1. Global `set_headers` headers are applied first
2. Location `set_headers` headers are applied on top

**Conflict resolution:** when the same header name appears in both global and location `set_headers`, the location value replaces the global value for that header. All other global headers are retained.

**Example:**

```json
{
    "set_headers": {
        "X-Real-IP": "$remote_addr",
        "X-Source":  "global"
    },
    "locations": [
        {
            "path": "/api/",
            "backends": ["api1"],
            "set_headers": {
                "X-Source": "api-location",
                "X-Route":  "api"
            }
        }
    ]
}
```

A request to `/api/anything` gets all three headers:
- `X-Real-IP: <client IP>` (from global, no conflict)
- `X-Source: api-location` (location wins over global)
- `X-Route: api` (location-only)

---

## Implementation Notes

- All header values are rendered from templates first (resolving all variables against the original request snapshot), then applied to the outgoing request in a single pass. No header rule's output can affect another rule's variable resolution.
- `set_headers` mutations are applied to the forwarded `*http.Request`. They never affect the [request snapshot](concepts.md#request-snapshot), so logged header values always reflect what the client sent, not what was forwarded.
- `X-Forwarded-For` is managed automatically by Go's `httputil.ReverseProxy`. There is no need to set it manually.

---

## What `set_headers` Does Not Do

- It does not modify response headers (headers returned to the client). For that, use [`set_response_headers`](#response-headers-set_response_headers).
- It does not remove headers sent by the client — it only adds or overrides.

---

## Response headers (`set_response_headers`)

`set_response_headers` is the exact mirror of `set_headers`, but it sets headers on the **response** returned to the client instead of the request forwarded to the backend. It is analogous to nginx's `add_header` directive.

Like `set_headers`, it can be set at two levels:
- **Global** (`set_response_headers` at the top level) — applies to every response
- **Location** (`set_response_headers` inside a location block) — applies only to responses for requests matched by that location, stacked on top of the global response headers

### Configuration

`set_response_headers` is a JSON object where each key is a header name and each value is a [template](concepts.md#template) string. It accepts the same [variable](variables.md) references (`$name` / `${name}`) as `set_headers` — resolved against the [request snapshot](concepts.md#request-snapshot) (e.g. `$remote_addr`, `$scheme`, `$host`, `$uri`, `$request_method`, `$time_iso8601`, `$http_*`). Unknown variable names are rejected at startup ([fail-fast](concepts.md#fail-fast)).

```json
{
    "set_response_headers": {
        "X-Served-By":      "switchyard",
        "X-Request-Scheme": "$scheme"
    },
    "locations": [
        {
            "path": "/api/",
            "backends": ["api1"],
            "set_response_headers": {
                "X-Served-By":   "api-tier",
                "Cache-Control": "no-store"
            }
        }
    ]
}
```

### Set / override semantics

Each configured header is **set** on the response: if the underlying handler already produced that header, the configured value overrides it; all other headers the handler produced are retained. This mirrors the set/override behavior of `set_headers`.

There is **no `Host` special case** — that applies only to the request side.

### Stacking

When both a global `set_response_headers` and a location `set_response_headers` are configured, they are applied in order (mirroring `set_headers`):

1. Global `set_response_headers` are applied first
2. The matched location's `set_response_headers` are applied on top

**Conflict resolution:** when the same header name appears in both, the location value replaces the global value for that header; all other global headers are retained.

In the example above, a response to `/api/anything` carries:
- `X-Served-By: api-tier` (location wins over the global `switchyard`)
- `X-Request-Scheme: http` (from global, no conflict)
- `Cache-Control: no-store` (location-only)

### Applies to all response types

`set_response_headers` applies uniformly to every response that goes through routing:

- **Proxied** (backend) responses
- **Static** file responses (`type: "static"`)
- **Switchyard-generated** responses — the `type: "response"` location and the built-in error responses (404 `not_found`, 502 `backend_error`, 403 `forbidden`, 405 `method_not_allowed`)

It is implemented with a lazy `ResponseWriter` wrapper that injects the headers just before the status line is written, so streaming responses and WebSocket upgrades keep working (it forwards `Flush`/`Hijack`).

**Scope note:** the pre-routing global-overflow `503` (project-wide [`max_connections`](config-reference.md#connection-limits--timeouts) backpressure) is not covered by `set_response_headers` — that reject carries its own [`overflow.headers`](config-reference.md#overflow).
