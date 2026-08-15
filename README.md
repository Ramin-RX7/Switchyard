# Switchyard

An HTTP reverse proxy and API gateway written in Go.

Switchyard sits in front of your backend services and routes incoming traffic to them — forwarding requests, load-balancing across pools, routing by path and HTTP method, gating clients by IP, serving static files, generating its own responses, injecting request and response headers, applying connection limits and timeouts, and writing structured access logs. Configuration is a single JSON file, reloadable in-process without dropping connections. For a complete, always-current inventory see [docs/features.md](docs/features.md).

---

## Features

- **Reverse proxy** — forwards HTTP requests to upstream backends via `httputil.ReverseProxy`, with automatic `X-Forwarded-For`
- **Load balancing** — round-robin across backend pools (global or per-location), lock-free with atomic counters
- **Location routing** — nginx-style ordered location blocks with prefix or Go regexp path matching, first-match-wins
- **Method-based routing** — route by HTTP method; backends declare accepted `methods`, unmatched → configurable **405** with an `Allow` header
- **IP access control** — `whitelist` / `blacklist` (single IP or CIDR, IPv4/IPv6) at two tiers — project-wide (global) and per-location — stacking as AND; denial → configurable **403**
- **Static file serving** — serve a local directory from any location path, with automatic prefix stripping
- **Response generation** — Switchyard-produced responses (status + headers + body with variables) for a `type: "response"` location and every built-in error response (**502** / **404** / **405** / **403** / overflow), each individually overridable
- **Request header injection** — inject or override request headers before forwarding, with variable substitution
- **Response header injection** — set headers on the client response (proxied, static, generated) with the same variables; streaming- and WebSocket-safe
- **Request variables** — nginx-style `$variable` placeholders (`$remote_addr`, `$scheme`, `$host`, `$http_*`, `$time_iso8601`, and more)
- **Connection limits & overflow** — concurrent-in-flight caps nested at project / location / backend scopes, with configurable overflow behavior (`reject` / `queue` / `reroute`)
- **Retry / reroute on failure** — retry a failed forward on another backend on a connection error, a configurable response-status list, or an unhealthy backend, with idempotency-aware safety, `none`/`constant`/`exponential` backoff (+ jitter), and field-merged global/per-location policy
- **Timeouts & transport tuning** — upstream and client-facing timeouts plus keep-alive pool tuning, at project and per-backend scope
- **Custom logging** — structured log lines with a user-defined format, parameterized fields, optional body capture, and multiple outputs (console / file / both); stacked global and per-location loggers
- **Fail-fast validation** — all configuration errors (unknown variables, bad regexes, missing backends, bad IP ranges) are caught at startup
- **Zero-downtime reload** — `switchyard reload [--force]` swaps the config in-process without restarting or dropping connections; an invalid new config is rejected and the running one keeps serving
- **Usable two ways** — run the turnkey binary with just a JSON config (nginx-style), or import it as a Go **SDK** and override any of the **11 pluggable stages** (routing, backend selection, access control, response generation, logging, …) with your own code — no forking. See [docs/extending.md](docs/extending.md)

---

## Quick Start

**1. Build**

```bash
make build          # or: go build -o Switchyard ./cmd/switchyard/
```

**2. Create a config file**

```json
{
    "listen": ":8080",
    "backends": [
        { "id": "app", "url": "http://127.0.0.1:3000" }
    ]
}
```

**3. Run**

```bash
./Switchyard -config switchyard.json
```

Switchyard will start listening on `:8080` and forward all requests to `http://127.0.0.1:3000`. It also writes a pid file (default `switchyard.pid`; `-pidfile PATH` to relocate, `-pidfile ""` to disable).

**4. Reload config without downtime**

```bash
./Switchyard reload            # graceful: in-flight requests drain on the old config
./Switchyard reload --force    # force: cancel in-flight requests (best-effort 503), then swap
```

Reload swaps the config in-process without restarting or dropping connections. Graceful lets in-flight requests finish on the old config while new requests use the new one; force cancels in-flight requests first. An invalid new config is rejected and the running config keeps serving. (`listen` and the `server` timeouts need a full restart to change.)

---

## Configuration

The full config supports backends, location routing, header injection, and logging:

```json
{
    "listen": ":8091",

    "backends": [
        { "id": "api1",     "url": "http://127.0.0.1:9001" },
        { "id": "api2",     "url": "http://127.0.0.1:9002" },
        { "id": "frontend", "url": "http://127.0.0.1:9003" }
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
            "set_headers": { "X-Route": "api" },
            "logging": {
                "outputs": ["file"],
                "file": "api.log",
                "format": "API {method} {path} backend={backend_id} status={status} app_dur={app_duration}"
            }
        },
        {
            "path": "/media/",
            "type": "static",
            "root": "/var/www/media"
        },
        {
            "path": "/",
            "backends": ["frontend"]
        }
    ]
}
```

**What this does:**

- `/api/*` — load-balanced between `api1` and `api2`; logs to both console (global) and `api.log` (location)
- `/media/*` — serves files from `/var/www/media`
- `/*` — catch-all, forwards to `frontend`
- All requests get `X-Real-IP`, `X-Forwarded-Proto`, and `X-Forwarded-Host` headers injected

---

## Documentation

Full reference documentation is in the [`docs/`](docs/) directory:

| Document | Description |
|----------|-------------|
| [docs/features.md](docs/features.md) | Complete inventory of every implemented feature (⚙️ Config / 🧩 SDK) |
| [docs/concepts.md](docs/concepts.md) | Glossary — what every term means |
| [docs/architecture.md](docs/architecture.md) | The three-stage request pipeline |
| [docs/config-reference.md](docs/config-reference.md) | Every configuration field documented |
| [docs/backends.md](docs/backends.md) | Backend setup, round-robin, error handling |
| [docs/routing.md](docs/routing.md) | Location routing, prefix/regex matching, method routing, IP access control, static serving |
| [docs/variables.md](docs/variables.md) | All available `$variable` placeholders |
| [docs/set-headers.md](docs/set-headers.md) | Request and response header injection with variable substitution |
| [docs/logging.md](docs/logging.md) | Log format, fields, outputs, body capture |
| [docs/extending.md](docs/extending.md) | Using Switchyard as an SDK — override any of the 11 pluggable stages in your own Go code |
| [docs/testing.md](docs/testing.md) | Test-suite layout, per-stage testing patterns, and the tests-required rule |
