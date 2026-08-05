# Switchyard

An HTTP reverse proxy and API gateway written in Go.

Switchyard sits in front of your backend services and routes incoming traffic to them — forwarding requests, load-balancing across pools, serving static files, injecting headers, and writing structured access logs. Configuration is a single JSON file with no dynamic reloading required.

---

## Features

- **Reverse proxy** — forwards HTTP requests to upstream backends via `httputil.ReverseProxy`
- **Load balancing** — round-robin across backend pools (global or per-location), lock-free with atomic counters
- **Location routing** — nginx-style ordered location blocks with prefix or Go regexp path matching
- **Static file serving** — serve a local directory from any location path, with automatic prefix stripping
- **Header injection** — inject or override request headers before forwarding, with variable substitution
- **Request variables** — nginx-style `$variable` placeholders (`$remote_addr`, `$scheme`, `$host`, `$http_*`, and more)
- **Custom logging** — structured log lines with a user-defined format, parameterized fields, and optional body capture
- **Multiple log outputs** — write to console, file, or both; stacked global and per-location loggers
- **Fail-fast validation** — all configuration errors (unknown variables, bad regexes, missing backends) are caught at startup

---

## Quick Start

**1. Build**

```bash
go build -o switchyard ./cmd/switchyard/
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
./switchyard -config switchyard.json
```

Switchyard will start listening on `:8080` and forward all requests to `http://127.0.0.1:3000`.

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
| [docs/concepts.md](docs/concepts.md) | Glossary — what every term means |
| [docs/architecture.md](docs/architecture.md) | The three-stage request pipeline |
| [docs/config-reference.md](docs/config-reference.md) | Every configuration field documented |
| [docs/backends.md](docs/backends.md) | Backend setup, round-robin, error handling |
| [docs/routing.md](docs/routing.md) | Location routing, prefix/regex matching, static serving |
| [docs/variables.md](docs/variables.md) | All available `$variable` placeholders |
| [docs/set-headers.md](docs/set-headers.md) | Header injection with variable substitution |
| [docs/logging.md](docs/logging.md) | Log format, fields, outputs, body capture |
