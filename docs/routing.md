# Routing

## Overview

When `locations` are configured in `switchyard.json`, Switchyard routes each request by matching its path against the location list from top to bottom. The first matching location handles the request. If no location matches, Switchyard returns **404** — produced by the configurable [`not_found`](config-reference.md#response) responder (with a sensible default).

When no `locations` are configured, all requests are distributed round-robin across all backends (global pool).

---

## Path Matching

Each location has a `path` field. By default it is a **prefix**: the location matches any request whose URI begins with `path`. Set `"regex": true` to use a Go regular expression instead, which is matched against the full request path.

```json
{ "path": "/api/" }
```
Matches: `/api/users`, `/api/v2/items`, `/api/`
Does not match: `/apiv2/`, `/other/`

```json
{ "path": "^/api/v[0-9]+/", "regex": true }
```
Matches: `/api/v1/users`, `/api/v2/items`
Does not match: `/api/users`, `/api/vX/`

**First-match wins.** Order the `locations` array from most specific to least specific. A catch-all like `"path": "/"` should go last.

---

## Location Types

### `type: "proxy"` (default)

Forwards the request to one backend from the location's own backend pool.

```json
{
    "path": "/api/",
    "backends": ["api1", "api2"]
}
```

- `backends` is a list of backend IDs from the global `backends` registry.
- Each location maintains its own independent round-robin counter over its pool. Two locations sharing a backend rotate independently.
- An empty `backends` list is rejected at startup.
- A matched proxy location with no reachable backend returns **502** — produced by the configurable [`backend_error`](config-reference.md#response) responder (with a sensible default).

See [backends.md](backends.md) for how round-robin selection works.

### `type: "static"`

Serves files from a local directory using Go's `http.FileServer`.

```json
{
    "path": "/media/",
    "type": "static",
    "root": "/var/www/media"
}
```

- `root` specifies the directory to serve from. The directory must exist at startup.
- `http.FileServer` handles content-type detection, `If-Modified-Since`, range requests, and path-traversal protection automatically.

### `type: "response"`

Returns a Switchyard-generated canned response — status, headers, and body — without proxying or touching disk. Useful for health checks, canned status endpoints, and maintenance pages.

```json
{
    "path": "/health",
    "type": "response",
    "response": {
        "status": 200,
        "headers": { "Content-Type": "application/json" },
        "body": "{\"status\":\"ok\",\"time\":\"$time_iso8601\"}"
    }
}
```

The `response` block is a [Response](config-reference.md#response) with:

- `status` — HTTP status code (default `200` for a location).
- `headers` — response headers; values may contain [variables](variables.md).
- `body` — response body; may contain [variables](variables.md).

If no `Content-Type` header is set, Switchyard defaults it to `text/plain; charset=utf-8`.

---

## Method routing

Routing happens in two stages: first **path → location** (as above), then **method → backend** within the matched location's pool.

Each backend may declare a `methods` list — the HTTP methods it accepts (see [config-reference.md#backend](config-reference.md#backend)). A backend with no `methods` (omitted or empty) accepts **any** method. Matching is **case-insensitive**; only listed methods match (there is no implicit HEAD-from-GET).

Within a matched proxy location, the request is routed only to backends whose `methods` include the request method. Round-robin then rotates over that method-filtered subset, using the location's existing shared counter. Because the counter is shared across all methods, under interleaved methods a single method's distribution across its backends can be slightly uneven — but selection is always a valid, method-accepting backend.

If a location matches but **no** backend in its pool accepts the request method, Switchyard returns **405 Method Not Allowed** — produced by the configurable [`method_not_allowed`](config-reference.md#response) responder (with a sensible default). The 405 carries an `Allow` header listing the sorted union of methods the location's backends accept.

```json
{
    "backends": [
        { "id": "reader", "url": "http://127.0.0.1:9001", "methods": ["GET", "HEAD"] },
        { "id": "writer", "url": "http://127.0.0.1:9002", "methods": ["POST", "PUT", "DELETE"] }
    ],
    "locations": [
        { "path": "/api/", "backends": ["reader", "writer"] }
    ]
}
```

For a request to `/api/...`:
- `GET` or `HEAD` → forwarded to `reader`.
- `POST`, `PUT`, or `DELETE` → forwarded to `writer`.
- Any other method (e.g. `PATCH`) → **405** with `Allow: DELETE, GET, HEAD, POST, PUT`.

---

## IP access control

A location may restrict which clients reach it by IP, via two optional fields — `whitelist` and `blacklist` (see [config-reference.md#whitelist--blacklist](config-reference.md#whitelist--blacklist)). Each is an array of entries; every entry is a single IP address or a CIDR range, IPv4 or IPv6 (e.g. `"203.0.113.7"`, `"192.0.2.0/24"`, `"2001:db8::/32"`).

Access control is per-location and is evaluated **right after the location matches**, before method routing and backend selection — so it applies equally to `proxy`, `static`, and `response` locations. Denial is a pure routing decision; nothing is forwarded or served.

**Evaluation order** (blacklist wins over whitelist):

1. If the client IP is in the `blacklist` → **deny**.
2. Otherwise, if a non-empty `whitelist` is configured → allow only if the IP is in it (a client address that cannot be parsed fails closed → deny).
3. Otherwise → allow.

A whitelist is enforced only when non-empty. Omitting both (or leaving both empty) means every client is accepted (the default, unchanged behavior).

When a client is denied, Switchyard returns **403 Forbidden** — produced by the configurable [`forbidden`](config-reference.md#backend_error-not_found-method_not_allowed-and-forbidden) responder (with a sensible default). Malformed entries are rejected at startup.

The client IP is the connecting peer's address (`RemoteAddr`). If Switchyard sits behind a trusted load balancer where the real client IP is in `X-Forwarded-For`, the built-in check sees the load balancer's address; supply a custom `AccessController` via the SDK to consult `X-Forwarded-For` instead — see [extending.md#the-pluggable-surface](extending.md#the-pluggable-surface).

```json
{
    "locations": [
        {
            "path": "/api/",
            "backends": ["api1", "api2"],
            "blacklist": ["192.0.2.0/24", "10.0.0.0/8"]
        },
        {
            "path": "/admin/",
            "backends": ["admin"],
            "whitelist": ["203.0.113.0/24", "198.51.100.7"]
        }
    ]
}
```

- `/api/*` — every client is allowed **except** those in `192.0.2.0/24` or `10.0.0.0/8`, which get a `403`.
- `/admin/*` — **only** clients in `203.0.113.0/24` or the single address `198.51.100.7` are allowed; everyone else gets a `403`.

---

## `strip_prefix` for Static Locations

Before looking up a file in `root`, Switchyard strips a prefix from the incoming request path. By default (for non-regex locations), `strip_prefix` is set to the location's `path`.

**Example (default behavior):**

```json
{
    "path": "/media/",
    "type": "static",
    "root": "/var/www"
}
```

A request to `/media/logo.png` → strips `/media/` → looks up `/var/www/logo.png`.

**Override strip_prefix:**

```json
{
    "path": "/media/",
    "type": "static",
    "root": "/var/www",
    "strip_prefix": "/media/assets/"
}
```

A request to `/media/assets/logo.png` → strips `/media/assets/` → looks up `/var/www/logo.png`.

**Disable stripping (serve from root as-is):**

```json
{
    "path": "/media/",
    "type": "static",
    "root": "/var/www",
    "strip_prefix": ""
}
```

A request to `/media/logo.png` → no stripping → looks up `/var/www/media/logo.png`.

For regex locations, `strip_prefix` is not set automatically and defaults to no stripping unless explicitly specified.

---

## No-Match Behavior

When no location matches the request path, Switchyard returns **404 Not Found**. This is the only path that produces a 404 from the routing stage. The 404 response is produced by the configurable [`not_found`](config-reference.md#response) responder (default body `switchyard: no matching location`); an unreachable/empty proxy pool produces a 502 via the [`backend_error`](config-reference.md#response) responder.

Note the distinction from **405 Method Not Allowed**: a 405 means a location *did* match on path but no backend in its pool accepts the request method (see [Method routing](#method-routing)), whereas a 404 means no location matched the path at all. All three responses can be overridden — see [config-reference.md#response](config-reference.md#response).

---

## Stacking: Headers and Logging

Location-level `set_headers` and `logging` augment the global configuration rather than replacing it. See [concepts.md#stacking](concepts.md#stacking) for the definition.

In practice:
- A matched request gets global `set_headers` applied, then location `set_headers` on top. Same key → location wins.
- A matched request fires the global logger *and* the location's own logger (if both are configured). Each produces a separate log line from the same request record.

---

## Example: Full Location Config

```json
{
    "locations": [
        {
            "path": "^/api/v[0-9]+/",
            "regex": true,
            "backends": ["api1", "api2"],
            "set_headers": { "X-Route": "versioned-api" },
            "logging": {
                "outputs": ["file"],
                "file": "api.log",
                "format": "{method} {path} {status} {app_duration}"
            }
        },
        {
            "path": "/static/",
            "type": "static",
            "root": "/var/www/static"
        },
        {
            "path": "/",
            "backends": ["frontend"]
        }
    ]
}
```

Request to `/api/v2/users`:
- Matches the first location (regex `^/api/v[0-9]+/`)
- Round-robin between `api1` and `api2`
- Sets `X-Route: versioned-api` (plus any global headers)
- Fires both the global logger and the location's `api.log` logger

Request to `/static/style.css`:
- Matches the second location
- Serves `/var/www/static/style.css`

Request to `/`:
- Matches the catch-all
- Forwards to `frontend`

Request to `/unknown`:
- Matches the catch-all `/` (prefix match)
- Forwards to `frontend`
