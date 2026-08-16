# Configuration Reference

Switchyard is configured via a single JSON file (default: `switchyard.json`). Pass a different path with the `-config` flag. All validation is [fail-fast](concepts.md#fail-fast): the process exits on the first error before serving any traffic.

### Running the binary

Start the server with `./Switchyard -config switchyard.json`. It listens on [`listen`](#listen) and writes a **pid file** (default `switchyard.pid`; override with `-pidfile PATH`, or `-pidfile ""` to disable). On `SIGINT`/`SIGTERM` it drains in-flight requests (15s) and removes the pid file.

**Hot reload.** Reload the config without restarting the process or dropping connections:

```bash
./Switchyard reload            # graceful: in-flight requests drain on the old config; new requests use the new one
./Switchyard reload --force    # force: cancel in-flight requests (best-effort 503), then swap
```

`reload` reads the pid file (`-pidfile PATH` to point elsewhere) and signals the running server. The reload re-runs full validation; an invalid new config is rejected and logged, and the running config keeps serving. All fields below reload live **except** [`listen`](#listen) and the [`server`](#server) timeouts, which belong to the running `http.Server` and require a full restart to change. See [architecture.md](architecture.md#configuration-reload-hot-reload) for the mechanism and [extending.md](extending.md#hot-reload-the-server-type) for the SDK equivalent.

Connection limits and timeouts are configurable at three scopes — project (top-level), per [Backend](#backend), and per [Location](#location) — via the fields below. Durations are integer numbers of seconds (e.g. `30`); `0`/omitted means "no limit". See [Connection limits & timeouts](#connection-limits--timeouts). (These can also be overridden in Go via `Proxy.Transport` / `Proxy.MaxInFlight` — see [extending.md](extending.md#concurrency--tuning).)

---

## Top-Level Fields

| Field | Type | Default | Required |
|-------|------|---------|----------|
| `listen` | string | `:8091` | no |
| `backends` | array of [Backend](#backend) | — | yes |
| `locations` | array of [Location](#location) | — | no |
| `set_headers` | object (string → string) | — | no |
| `set_response_headers` | object (string → string) | — | no |
| `logging` | [Logging](#logging) | — | no |
| `max_connections` | int | `0` (unlimited) | no |
| `timeouts` | [Timeouts](#timeouts) | — | no |
| `transport` | [Transport](#transport) | — | no |
| `server` | [Server](#server) | — | no |
| `overflow` | [Overflow](#overflow) | — | no |
| `retry` | [Retry](#retry) | — | no |
| `health` | [Health](#health) | — | no (per-backend default) |
| `rate_limit` | [Rate limiting](#rate-limiting) | — | no |
| `backend_error` | [Response](#response) | — | no |
| `not_found` | [Response](#response) | — | no |
| `method_not_allowed` | [Response](#response) | — | no |
| `forbidden` | [Response](#response) | — | no |
| `whitelist` | array of string | — | no |
| `blacklist` | array of string | — | no |

### `listen`

The address the server listens on. Any format accepted by Go's `net.Listen` is valid: `:8091`, `0.0.0.0:8091`, `127.0.0.1:9000`. Defaults to `:8091` if omitted.

### `backends`

The global registry of upstream servers. Every backend defined here is available to be referenced by name in location blocks. At least one backend is required.

### `locations`

An ordered list of [Location](#location) blocks. When present, incoming requests are matched against locations top-to-bottom (first match wins) instead of using global round-robin. When absent, all requests are distributed round-robin across all backends.

### `set_headers`

A map of header names to [template](concepts.md#template) values applied to every forwarded request, before it is sent to the backend. Values may reference [variables](variables.md). See [set-headers.md](set-headers.md).

### `set_response_headers`

A map of header names to [template](concepts.md#template) values set on every response returned to the client — proxied, static, and Switchyard-generated alike. The mirror of [`set_headers`](#set_headers) on the response side. Values may reference [variables](variables.md). See [set-headers.md](set-headers.md#response-headers-set_response_headers).

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
| `methods` | array of string | — (accepts all) | no |
| `health` | [Health](#health) | — | no (field-merged over the top-level `health`) |

The `timeouts`/`transport`/`disable_keep_alive` fields override the project defaults for this backend; see [Connection limits & timeouts](#connection-limits--timeouts).

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

### `methods`

An optional list of HTTP method names this backend accepts (e.g. `["GET", "HEAD"]`). Matching is **case-insensitive** — config values are normalized to upper-case. Only listed methods match; there is no implicit HEAD-from-GET. When `methods` is omitted or empty, the backend accepts **any** method.

Within a matched location, a request is routed only to backends whose `methods` include the request method. If a location matches but **no** backend in its pool accepts the request method, Switchyard returns **405 Method Not Allowed** — produced by the [`method_not_allowed`](#response) responder, with an `Allow` header listing the sorted union of methods the location's backends accept. See [routing.md#method-routing](routing.md#method-routing). An empty-string entry in `methods` is rejected at startup.

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
| `set_response_headers` | object (string → string) | — | no |
| `logging` | [Logging](#logging) | — | no |
| `max_connections` | int | `0` (unlimited) | no |
| `retry` | [Retry](#retry) | — | no (field-merged over the global `retry`) |
| `rate_limit` | [Rate limiting](#rate-limiting) | — | no (this location's tier) |
| `whitelist` | array of string | — | no |
| `blacklist` | array of string | — | no |

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

### `set_response_headers`

Location-specific response headers applied in addition to the global `set_response_headers`. When the same header name appears in both, the location value wins; all other global headers are retained. See [set-headers.md](set-headers.md#response-headers-set_response_headers).

### `logging`

A location-specific logger that fires in addition to (not instead of) the global logger. Both loggers receive the same request record and render independently. See [logging.md](logging.md).

### `max_connections`

Caps concurrent in-flight requests routed through this location (`0` = unlimited). Independent of the backend and project caps — see [Connection limits & timeouts](#connection-limits--timeouts).

### whitelist / blacklist

IP access control. Each is an array of strings; every entry is either a single IP address or a CIDR range, IPv4 or IPv6 (e.g. `"203.0.113.7"`, `"192.0.2.0/24"`, `"2001:db8::/32"`).

- `blacklist` — client IPs/ranges that are **denied**.
- `whitelist` — when non-empty, **only** listed IPs/ranges are allowed.

These fields exist at **two tiers**:

- **Top-level** (`whitelist`/`blacklist` on the root config) — a project-wide gate evaluated **before** location matching, so it applies to every request, including paths that match no location and configs with no locations at all.
- **Per-location** (`whitelist`/`blacklist` on a [Location](#location)) — evaluated **right after** the location matches, before method routing and backend selection.

The tiers **stack (AND)**: a request must pass the top-level gate *and* the matched location's gate. The top-level tier is checked first; if it denies, the location is never consulted.

**Evaluation order** within a tier (blacklist takes precedence over whitelist):

1. If the client IP is in the `blacklist` → **deny**.
2. Otherwise, if a `whitelist` is configured (non-empty) → allow only if the IP is in it (a client address that cannot be parsed fails closed → deny).
3. Otherwise → allow.

A whitelist is enforced only when non-empty. Omitting both at a tier (or leaving both empty) means **no restriction** at that tier — every client is accepted (the default).

The client IP is the connecting peer's address (`RemoteAddr`). If Switchyard runs behind a trusted load balancer or proxy where the real client IP is carried in `X-Forwarded-For`, the built-in check will see the load balancer's address, not the end client's. In that case supply a custom `AccessController` via the SDK that consults `X-Forwarded-For` — the top-level tier is `Proxy.Access`, the per-location tier is `loc.Access` — see [extending.md](extending.md#the-pluggable-surface).

When a client is denied, Switchyard returns **403 Forbidden**, produced by the configurable [`forbidden`](#backend_error-not_found-method_not_allowed-and-forbidden) responder. See [routing.md#ip-access-control](routing.md#ip-access-control).

Malformed entries (an invalid IP/CIDR, or an empty string) are rejected at startup ([fail-fast](concepts.md#fail-fast)).

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

Client-facing `http.Server` timeouts. **Project-only** (there is one server accepting client connections). **Not hot-reloadable** — these belong to the running `http.Server`, so changing them requires a full restart (see [Running the binary](#running-the-binary)).

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

## Retry

Retries a failed forward on another backend. Available at the **top level** and **per-location** (`locations[].retry`); a location's block is **field-merged** over the global one — each field set on the location wins, every unset field inherits the global value (or the built-in default). Distinct from [Overflow](#overflow) `reroute`, which reacts to capacity, not failure.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `attempts` | int | `0` | Number of retries **beyond** the first try. `0` (default) disables retry. |
| `on_connection_error` | bool | `true` | Retry when the backend is unreachable / resets. Applies to **any** HTTP method. |
| `on_status` | array of int | `[]` | Upstream status codes that trigger a retry. Idempotent methods only, unless `retry_non_idempotent`. |
| `retry_non_idempotent` | bool | `false` | Allow status-based retry of non-idempotent methods (POST/PATCH). |
| `retry_same_backend` | bool | `true` | `true`: normal selection, the just-tried backend may be reselected. `false`: exclude already-tried backends. |
| `skip_unhealthy` | bool | `true` | Exclude backends flagged unhealthy (`Backend.SetHealthy(false)`). Falls back to the full set if all are unhealthy. |
| `max_body_bytes` | int | `1048576` | Cap for buffering the request body for replay; a larger body makes the request single-attempt. |
| `backoff` | [Backoff](#backoff) | see below | Inter-attempt delay policy. |
| `response` | [Response](#response) | — | Optional response returned when retries are exhausted (see below). |

**Triggers.** A retry fires on any of: (1) a connection error (`on_connection_error`); (2) an upstream status in `on_status` (subject to the idempotency gate); (3) selection skipping an unhealthy backend (`skip_unhealthy`). The health flag itself is set via the SDK hook `Backend.SetHealthy(bool)` (a config-driven health checker is a separate feature).

**On exhaustion.** When retries run out, the client receives — by default — the real final upstream response (e.g. the actual `503`); if every attempt was a connection error (no response), the [`backend_error`](#backend_error-not_found-method_not_allowed-and-forbidden) `502` is rendered. Set `retry.response` to override either with a Switchyard-generated response (status + headers + body, with [variables](variables.md)).

Retries occur only before any byte reaches the client, so streaming and WebSocket upgrades are unaffected. The number of retries performed is available as the `{retries}` [log field](logging.md).

### Backoff

The delay between attempts. `delay(n)` is the wait before retry number `n` (`n = 1` for the first retry); with `jitter`, the computed delay `d` is replaced by a uniform random value in `[0, d]` (full jitter).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `strategy` | string | `"exponential"` | `"none"`, `"constant"`, or `"exponential"` (see below). |
| `base_ms` | int (ms) | `50` | Base delay in milliseconds. |
| `max_ms` | int (ms) | `2000` | Maximum delay (caps `"exponential"`). |
| `jitter` | bool | `true` | Apply full jitter to the computed delay. |

**Strategies:**
- `none` — retry immediately, no wait. `base_ms` / `max_ms` / `jitter` are ignored.
- `constant` — wait `base_ms` before every retry. `max_ms` is ignored.
- `exponential` — wait `min(max_ms, base_ms · 2^(n-1))` before retry `n`.

```json
{
    "retry": {
        "attempts": 2,
        "on_connection_error": true,
        "on_status": [502, 503, 504],
        "retry_same_backend": false,
        "backoff": { "strategy": "exponential", "base_ms": 50, "max_ms": 2000, "jitter": true }
    }
}
```

---

## Rate limiting

Throttles requests by a composite key using a token bucket. Declared at the **top level** (global tier, checked
before routing so it also guards no-match/404 traffic) and/or per-[Location](#location) (that location's tier);
both tiers are enforced (a request must pass each). Distinct from [`max_connections`](#connection-limits--timeouts)
— that caps concurrent in-flight requests, this caps requests per unit time. Enabled when `rate > 0`.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `key` | array of string | `["ip"]` | Composite bucket key. Each entry is `"ip"`, `"method"`, `"path"`, or `"header:<NAME>"`; the resolved values are joined. Changing any dimension is a separate bucket. |
| `rate` | int | — (required) | Requests allowed per `period`. `0`/absent disables the tier. |
| `period` | int (seconds) | `1` | Window the `rate` refills over ⇒ `rate/period` tokens/sec. |
| `burst` | int | `= rate` | Bucket capacity — the maximum instantaneous burst. |
| `methods` | array of string | — (all) | Restrict the rule to these HTTP methods (case-insensitive). Empty = every method. |
| `headers` | string | `"on-reject"` | RateLimit-* header emission: `"off"`, `"on-reject"` (only on 429), or `"always"` (on allowed responses too). |
| `status` | int | `429` | Status for the reject response. |
| `response_headers` | object (string → string) | — | Extra headers on the reject response. Values may contain [variables](variables.md). |
| `body` | string | `switchyard: rate limit exceeded` | Reject response body. May contain [variables](variables.md). |

On rejection the response is a [Response](#response)-style body with the configured `status`, always a
`Retry-After` header, and (unless `headers: "off"`) `RateLimit-Limit`/`RateLimit-Remaining`/`RateLimit-Reset`.
State is in-memory by default (no external dependency) and resets on reload, like the round-robin counter.

Both the **algorithm** and the **storage** are SDK-swappable behind uniform interfaces (`RateLimiter` /
`RateLimitStore`) — see [extending.md](extending.md#rate-limiting-algorithm--storage). A distributed store (Redis
etc.) implements the same interface; without one, each process counts independently (the effective limit is
N× the configured value across N instances).

```json
{
    "rate_limit": {
        "key": ["header:X-Api-Key", "path"],
        "rate": 100,
        "period": 60,
        "burst": 20,
        "methods": ["POST", "PUT", "DELETE"],
        "headers": "always"
    }
}
```

---

## Health

Automatic backend health detection. Declared at the **top level** (defaults) and/or per-[Backend](#backend);
a backend's block **field-merges** over the top-level one (each set field wins, unset inherits). A backend
flagged unhealthy is excluded from selection by retry's [`skip_unhealthy`](#retry) (default on). Two independent
detectors, either or both:

### Passive (`health.passive`)

Ejects a backend from real traffic. Enabled when `count > 0` and `window > 0`.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `statuses` | array of int | `[500, 502, 503, 504]` | Response statuses counted as failures. Connection errors always count (client/reload cancellations do not). |
| `count` | int | `3` | Failures within `window` that trip the backend to unhealthy. |
| `window` | int (seconds) | `60` | Sliding window over which failures are counted. |
| `cooldown` | int (seconds) | `30` | How long the backend stays unhealthy before auto-recovering. **Used only when no active check is configured** (otherwise the prober owns recovery). |

### Active (`health.active`)

Probes a health endpoint on an interval. Enabled when `path` is non-empty.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `path` | string | — (required to enable) | Health endpoint path, joined onto the backend URL. |
| `method` | string | `"GET"` | HTTP method for the probe. |
| `interval` | int (seconds) | `10` | Time between probe cycles. |
| `timeout` | int (seconds) | `2` | Per-probe request timeout. |
| `expected_status` | int | `200` | A cycle passes iff the probe returns this status. |
| `retries` | int | `1` | Immediate retries within a cycle before the cycle counts as failed. |
| `unhealthy_threshold` | int | `3` | Consecutive failed cycles before marking unhealthy. |
| `healthy_threshold` | int | `2` | Consecutive passed cycles before marking healthy. |
| `host` | string | — | Optional `Host` header on the probe request. |

**Recovery.** With an active check configured, it is the sole authority on recovery — passive may eject
immediately, but the flag is only restored by `healthy_threshold` consecutive good probe cycles (no cooldown
timer runs). Without an active check, passive restores the backend after `cooldown` (half-open: traffic returns
and re-tests). Every transition is logged (`switchyard: backend <url> marked (un)healthy (<reason>)`). Health
state is in-memory and resets on reload (like the round-robin counter).

Active probes run as goroutines started by the reloadable `Server` automatically. SDK users mounting a bare
`Handler()` (no `Server`) call `Proxy.StartHealthChecks(ctx)` to enable active checks; passive checks need no
start-up.

```json
{
    "backends": [
        { "id": "api1", "url": "http://127.0.0.1:9001", "health": { "active": { "path": "/healthz" } } }
    ],
    "health": {
        "passive": { "statuses": [502, 503, 504], "count": 3, "window": 30, "cooldown": 20 }
    }
}
```

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

### `backend_error`, `not_found`, `method_not_allowed`, and `forbidden`

These top-level fields override Switchyard's built-in error responses. Each is a [Response](#response):

| Field | Triggered when | Status | Default body |
|-------|----------------|--------|--------------|
| `backend_error` | An upstream is unreachable, or a proxy location has an empty/no-available backend pool. | `502` | `switchyard: backend unavailable` |
| `not_found` | No location matches the request. | `404` | `switchyard: no matching location` |
| `method_not_allowed` | A location matched but no backend in its pool accepts the request method. | `405` | `switchyard: method not allowed` |
| `forbidden` | A location matched but its access control ([whitelist / blacklist](#whitelist--blacklist)) denied the client IP. | `403` | `switchyard: forbidden` |

For `method_not_allowed`, an `Allow` header is added automatically listing the sorted union of methods the matched location's backends accept (e.g. `Allow: DELETE, GET, HEAD, POST, PUT`). See [routing.md#method-routing](routing.md#method-routing).

All are optional; omit them to keep the built-in defaults. SDK users can replace the generators entirely (`p.BadGateway` / `p.NotFound` / `p.MethodNotAllowed` / `p.Forbidden`, and `loc.Responder` for a response location) — see [extending.md](extending.md#the-pluggable-surface).

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
    "method_not_allowed": {
        "status": 405,
        "headers": { "Content-Type": "application/json" },
        "body": "{\"error\":\"method not allowed\",\"method\":\"$request_method\",\"path\":\"$uri\"}"
    },
    "forbidden": {
        "status": 403,
        "headers": { "Content-Type": "application/json" },
        "body": "{\"error\":\"forbidden\",\"ip\":\"$remote_addr\"}"
    },

    "backends": [
        { "id": "api1",     "url": "http://127.0.0.1:9001", "max_connections": 100, "methods": ["GET", "HEAD"] },
        { "id": "api2",     "url": "http://127.0.0.1:9002", "max_connections": 100, "methods": ["POST", "PUT", "DELETE"], "timeouts": { "request": 10 } },
        { "id": "frontend", "url": "http://127.0.0.1:9003", "disable_keep_alive": true }
    ],

    "set_headers": {
        "X-Real-IP":         "$remote_addr",
        "X-Forwarded-Proto": "$scheme",
        "X-Forwarded-Host":  "$host"
    },

    "set_response_headers": {
        "X-Served-By":      "switchyard",
        "X-Request-Scheme": "$scheme"
    },

    "logging": {
        "outputs": ["console"],
        "format": "{receive_time} {method} {path} backend={backend_id} status={status} dur={request_duration}"
    },

    "locations": [
        {
            "path": "/api/",
            "backends": ["api1", "api2"],
            "blacklist": ["192.0.2.0/24", "10.0.0.0/8"],
            "max_connections": 150,
            "set_headers": {
                "X-Route": "api"
            },
            "set_response_headers": {
                "X-Served-By":   "api-tier",
                "Cache-Control": "no-store"
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
- Overrides the built-in `502` (`backend_error`), `404` (`not_found`), `405` (`method_not_allowed`), and `403` (`forbidden`) responses with JSON bodies
- Injects three headers into every forwarded request (global `set_headers`)
- Stamps `X-Served-By` and `X-Request-Scheme` onto every response (global `set_response_headers`)
- Logs every request to the console in a custom format (global `logging`)
- `/api/*` — denies clients in `192.0.2.0/24` or `10.0.0.0/8` with a JSON `403` (`forbidden`) before routing; otherwise routes by method within the pool: `GET`/`HEAD` go to `api1`, `POST`/`PUT`/`DELETE` go to `api2` (round-robin within each method's eligible backends); any other method yields `405` with `Allow: DELETE, GET, HEAD, POST, PUT`. Adds an extra request header, overrides `X-Served-By` to `api-tier` and adds `Cache-Control: no-store` on the response (location `set_response_headers`), and writes an additional log line to `api.log`
- `/media/*` — serves files from `/tmp/media`; a request to `/media/logo.png` serves `/tmp/media/logo.png`
- `/health` — returns a canned JSON response with the request-receipt time (`type: "response"`)
- `/*` — catch-all, forwards to `frontend`
