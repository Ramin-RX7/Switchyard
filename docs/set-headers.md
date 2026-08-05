# set_headers — Request Header Injection

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

- It does not modify response headers (headers returned from the backend to the client). For response header manipulation, that would be a separate feature.
- It does not remove headers sent by the client — it only adds or overrides.
