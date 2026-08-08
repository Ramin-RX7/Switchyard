# Configuration Reference

Switchyard is configured via a single JSON file (default: `switchyard.json`). Pass a different path with the `-config` flag. All validation is [fail-fast](concepts.md#fail-fast): the process exits on the first error before serving any traffic.

Connection limits and timeouts are configurable at three scopes — project (top-level), per [Backend](#backend), and per [Location](#location) — via the fields below. Durations are integer numbers of seconds (e.g. `30`); `0`/omitted means "no limit". See [Connection limits & timeouts](#connection-limits--timeouts). (These can also be overridden in Go via `Proxy.Transport` / `Proxy.MaxInFlight` — see [extending.md](extending.md#concurrency--tuning).)

---

## Top-Level Fields

| Field | Type | Default | Required |
|-------|------|---------|----------|
| `listen` | string | `:8091` | no |
| `backends` | array of [Backend](#backend) | — | yes |
| `locations` | array of [Location](#location) | — | no |
| `set_headers` | object (string → string) | — | no |
| `logging` | [Logging](#logging) | — | no |
| `max_connections` | int | `0` (unlimited) | no |
| `timeouts` | [Timeouts](#timeouts) | — | no |
| `transport` | [Transport](#transport) | — | no |
| `server` | [Server](#server) | — | no |
| `overflow` | [Overflow](#overflow) | — | no |
| `backend_error` | [Response](#response) | — | no |
| `not_found` | [Response](#response) | — | no |

### `listen`

The address the server listens on. Any format accepted by Go's `net.Listen` is valid: `:8091`, `0.0.0.0:8091`, `127.0.0.1:9000`. Defaults to `:8091` if omitted.

### `backends`

The global registry of upstream servers. Every backend defined here is available to be referenced by name in location blocks. At least one backend is required.

### `locations`

An ordered list of [Location](#location) blocks. When present, incoming requests are matched against locations top-to-bottom (first match wins) instead of using global round-robin. When absent, all requests are distributed round-robin across all backends.

### `set_headers`

A map of header names to [template](concepts.md#template) values applied to every forwarded request, before it is sent to the backend. Values may reference [variables](variables.md). See [set-headers.md](set-headers.md).

### `logging`

Custom log configuration applied to every request. When absent, Switchyard writes a built-in operational log line. See [logging.md](logging.md).

---

## Backend

Defined in the `backends` array.

| Field | Type | Default | Required |
|-------|------|---------|----------|
| `id` | string | value of `url` | no |
| `url` | string | — | yes |
| `max_connections` | int | `0` (unlimited) | no |
| `timeouts` | [Timeouts](#timeouts) | project value | no |
| `transport` | [Transport](#transport) | project value | no |
| `disable_keep_alive` | bool | `false` | no |

The last four override the project defaults for this backend; see [Connection limits & timeouts](#connection-limits--timeouts).

### `id`

A unique name for this backend. Used to reference the backend from location blocks' `backends` list. If omitted, defaults to the backend's `url`. Must be unique across all backends.

### `url`

The full URL of the upstream server. Must include scheme and host (e.g. `http://127.0.0.1:9001`). Must be unique across all backends.

**Example:**

```json
{
    "id": "api",
    "url": "http://127.0.0.1:9001"
}
```

---

## Location

Defined in the `locations` array.

| Field | Type | Default | Required |
|-------|------|---------|----------|
| `path` | string | — | yes |
| `regex` | bool | `false` | no |
| `type` | string | `"proxy"` | no |
| `backends` | array of string | — | required for `type: "proxy"` |
| `root` | string | — | required for `type: "static"` |
| `response` | [Response](#response) | — | required for `type: "response"` |
| `strip_prefix` | string | `path` (non-regex only) | no |
| `set_headers` | object (string → string) | — | no |
| `logging` | [Logging](#logging) | — | no |
| `max_connections` | int | `0` (unlimited) | no |

### `path`

The path to match. By default, this is a **prefix**: any request whose URI starts with `path` matches. If `regex` is `true`, this is a Go regular expression matched against the full request path.

### `regex`

When `true`, `path` is compiled as a Go regular expression. When `false` (default), `path` is treated as a literal prefix. Regex compilation failures are caught at startup.

### `type`

What to do when this location matches:

- `"proxy"` (default) — forward to one of the backends in the location's `backends` list
- `"static"` — serve files from the directory specified by `root`
- `"response"` — return a Switchyard-generated canned response defined by `response` (see [Response](#response))

### `backends`

A list of backend IDs (from the global `backends` registry) that form this location's pool. This location uses its own independent round-robin counter over this pool. Required when `type` is `"proxy"`. At least one entry is required (validated at startup).

### `root`

The filesystem directory from which to serve files. Required when `type` is `"static"`. The directory must exist at startup (validated at startup).

### `response`

The canned response returned when this location matches. Required when `type` is `"response"`. Its shape is a [Response](#response) (`status`/`headers`/`body`), and its `headers` and `body` may contain [variables](variables.md).

### `strip_prefix`

A path prefix that is removed from the request URI before looking up the file in `root`. For example, a request to `/media/logo.png` with `strip_prefix: "/media/"` and `root: "/var/www"` looks up `/var/www/logo.png`.

For non-regex locations, this defaults to the value of `path`, which means the location path is stripped automatically. Set it explicitly to override this behavior or to use an empty string to disable stripping.

Has no effect for `type: "proxy"` locations.

### `set_headers`

Location-specific headers applied to forwarded requests in addition to the global `set_headers`. When the same header name appears in both global and location `set_headers`, the location value wins; all other global headers are retained. See [set-headers.md](set-headers.md).

### `logging`

A location-specific logger that fires in addition to (not instead of) the global logger. Both loggers receive the same request record and render independently. See [logging.md](logging.md).

### `max_connections`

Caps concurrent in-flight requests routed through this location (`0` = unlimited). Independent of the backend and project caps — see [Connection limits & timeouts](#connection-limits--timeouts).

---

## Logging

Used as the value of the top-level `logging` field or a location's `logging` field.

| Field | Type | Default | Required |
|-------|------|---------|----------|
| `outputs` | array of string | `["console"]` | no |
| `file` | string | — | required when `"file"` is in `outputs` |
| `format` | string | — | yes |

### `outputs`

Where log lines are written. Accepted values:

- `"console"` — standard output
- `"file"` — the file specified by `file`

Both can be listed together: `["console", "file"]`.

### `file`

Path to the log file. The file is opened in append mode with permissions `0644`. Required when `"file"` appears in `outputs`.

### `format`

The log line template. Uses `{field}` syntax for placeholders. See [logging.md](logging.md) for the full list of available fields. The format is compiled at startup; unknown field names cause an immediate exit.

---

## Connection limits & timeouts

Capacity and timeout controls exist at three scopes. **`max_connections`** (a cap on concurrent in-flight requests) can be set on the **project**, a **location**, and a **backend**; the three are **independent, nested caps** — a request must have room at every applicable scope or it hits the [overflow](#overflow) policy. **Timeouts and transport tuning** are set on the **project** (as defaults) and overridden per **backend**; `server` timeouts are project-only.

`max_connections` defines "connections" as **max concurrent in-flight requests** to that scope (a semaphore); with keep-alive the real TCP-connection count may be lower. Omitting all of these reproduces Switchyard's built-in tuned defaults.

### Timeouts

Upstream (backend-facing) timeouts. Used at project scope (default) and per backend (override).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `request` | int (seconds) | `0` (none) | Whole per-request deadline to the backend. `0` = no deadline (streaming-safe). |
| `tls_handshake` | int (seconds) | `10` | Max TLS handshake time for `https` backends. |

> `request` is a single overall timeout today; it is modelled as an object so it can later split into separate send/receive timeouts without breaking configs.

### Transport

Keep-alive connection-pool tuning. Project scope (default) and per backend (override).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_idle_conns` | int | `512` | Idle keep-alive connections kept across all backends. |
| `max_idle_conns_per_host` | int | `256` | Idle keep-alive connections kept per backend (stdlib default is 2 — this is the throughput-critical one). |
| `idle_conn_timeout` | int (seconds) | `90` | How long an idle connection is pooled before closing. |

The per-backend `disable_keep_alive: true` forces a fresh connection per request to that backend.

### Server

Client-facing `http.Server` timeouts. **Project-only** (there is one server accepting client connections).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `read_header_timeout` | int (seconds) | `10` | Max time to read request headers. |
| `read_timeout` | int (seconds) | `0` (off) | Max time to read the whole client request. |
| `write_timeout` | int (seconds) | `0` (off) | Max time to write the response. **Caps total response time — leave `0` for streaming/large responses.** |
| `idle_timeout` | int (seconds) | `60` | Keep-alive idle timeout for client connections. |

### Overflow

What happens when a `max_connections` cap (any scope) is reached. Project-wide.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `strategy` | string | `"reject"` | `"reject"`, `"queue"`, or `"reroute"` (see below). |
| `queue_timeout` | int (seconds) | `0` | Wait for a free slot before failing (used by `"queue"`, and as `"reroute"`'s all-full fallback). |
| `status` | int | `503` | HTTP status for the reject response. |
| `headers` | object (string → string) | — | Extra headers for the reject response. Values may contain [variables](variables.md). |
| `body` | string | `switchyard: capacity reached` | Body for the reject response. May contain [variables](variables.md). |

**Strategies:**
- `reject` — fail immediately with the reject response.
- `queue` — wait up to `queue_timeout` for a slot, then reject.
- `reroute` — when the selected **backend** is full, try the other backends in the matched pool; if all are full, fall back to `queue_timeout` (if set) then reject. (The location and project caps have no alternates, so they use the queue/reject fallback.)

> For full control over the over-capacity response, an SDK user can override the `Actor`; for capacity-aware distribution (rather than reroute-on-overflow), a custom `BackendSelector` can route by `Backend.MaxConns`/`InFlight` — see [extending.md](extending.md#concurrency--tuning) and [`examples/least-loaded`](../examples/least-loaded/main.go).

---

## Response

A **Response** (`ResponseConfig`) describes an HTTP response that Switchyard generates itself — status, headers, and body — rather than proxying or serving from disk. It backs the `response` location type and the project-level error overrides below. Header values and the body may contain [variables](variables.md); they are validated at startup.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `status` | int | `200` (locations) | HTTP status code for the response. |
| `headers` | object (string → string) | — | Response headers. Values may contain [variables](variables.md). |
| `body` | string | — | Response body. May contain [variables](variables.md). |

If no `Content-Type` header is set, Switchyard defaults it to `text/plain; charset=utf-8`.

### `type: "response"` locations

A location with `type: "response"` returns the canned response in its `response` block whenever it matches — useful for health checks, canned status endpoints, or maintenance pages.

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

### `backend_error` and `not_found`

The two top-level fields override Switchyard's built-in error responses. Each is a [Response](#response):

| Field | Triggered when | Status | Default body |
|-------|----------------|--------|--------------|
| `backend_error` | An upstream is unreachable, or a proxy location has an empty/no-available backend pool. | `502` | `switchyard: backend unavailable` |
| `not_found` | No location matches the request. | `404` | `switchyard: no matching location` |

Both are optional; omit them to keep the built-in defaults. SDK users can replace the generators entirely (`p.BadGateway` / `p.NotFound`, and `loc.Responder` for a response location) — see [extending.md](extending.md#the-pluggable-surface).

---

## Annotated Example

```json
{
    "listen": ":8091",

    "max_connections": 500,
    "timeouts": {
        "request": 30,
        "tls_handshake": 5
    },
    "transport": {
        "max_idle_conns": 512,
        "max_idle_conns_per_host": 128,
        "idle_conn_timeout": 90
    },
    "server": {
        "read_header_timeout": 10,
        "idle_timeout": 60
    },
    "overflow": {
        "strategy": "reroute",
        "queue_timeout": 2,
        "status": 503,
        "headers": {
            "Content-Type": "application/json",
            "Retry-After": "2"
        },
        "body": "{\"error\":\"capacity reached, retry shortly\",\"at\":\"$time_iso8601\"}"
    },

    "backend_error": {
        "status": 502,
        "headers": { "Content-Type": "application/json" },
        "body": "{\"error\":\"upstream unavailable\",\"path\":\"$uri\"}"
    },
    "not_found": {
        "status": 404,
        "headers": { "Content-Type": "application/json" },
        "body": "{\"error\":\"no route\",\"path\":\"$uri\"}"
    },

    "backends": [
        { "id": "api1",     "url": "http://127.0.0.1:9001", "max_connections": 100 },
        { "id": "api2",     "url": "http://127.0.0.1:9002", "max_connections": 100, "timeouts": { "request": 10 } },
        { "id": "frontend", "url": "http://127.0.0.1:9003", "disable_keep_alive": true }
    ],

    "set_headers": {
        "X-Real-IP":         "$remote_addr",
        "X-Forwarded-Proto": "$scheme",
        "X-Forwarded-Host":  "$host"
    },

    "logging": {
        "outputs": ["console"],
        "format": "{receive_time} {method} {path} backend={backend_id} status={status} dur={request_duration}"
    },

    "locations": [
        {
            "path": "/api/",
            "backends": ["api1", "api2"],
            "max_connections": 150,
            "set_headers": {
                "X-Route": "api"
            },
            "logging": {
                "outputs": ["file"],
                "file": "api.log",
                "format": "API {method} {path} backend={backend_id} status={status} app_dur={app_duration}"
            }
        },
        {
            "path": "/media/",
            "type": "static",
            "root": "/tmp/media"
        },
        {
            "path": "/health",
            "type": "response",
            "response": {
                "status": 200,
                "headers": { "Content-Type": "application/json" },
                "body": "{\"status\":\"ok\",\"time\":\"$time_iso8601\"}"
            }
        },
        {
            "path": "/",
            "backends": ["frontend"]
        }
    ]
}
```

**What this config does:**

- Listens on `:8091`
- Caps project-wide concurrency at 500 in-flight requests; on overflow, reroutes to other backends and (if all full) waits up to 2s before rejecting with a JSON `503`
- Sets a 30s upstream request deadline and a 5s TLS-handshake timeout (project defaults)
- Overrides the built-in `502` (`backend_error`) and `404` (`not_found`) responses with JSON bodies
- Injects three headers into every forwarded request (global `set_headers`)
- Logs every request to the console in a custom format (global `logging`)
- `/api/*` — round-robin between `api1` and `api2`; adds an extra header and writes an additional log line to `api.log`
- `/media/*` — serves files from `/tmp/media`; a request to `/media/logo.png` serves `/tmp/media/logo.png`
- `/health` — returns a canned JSON response with the request-receipt time (`type: "response"`)
- `/*` — catch-all, forwards to `frontend`
