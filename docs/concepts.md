# Concepts

This glossary defines the terms used throughout Switchyard's configuration, documentation, and source code.

---

## Backend

A configured upstream server that Switchyard can forward requests to. Each backend has an `id` (a short name used to reference it elsewhere in the config) and a `url` (the full address including scheme and host). Backends are the targets of all proxying. See [backends.md](backends.md).

## Location

A path-based routing block, similar to nginx's `location` directive. Locations are defined in an ordered list; the first one whose `path` matches the incoming request URI wins. A location can forward to a private pool of backends (`type: "proxy"`) or serve files from disk (`type: "static"`). When no locations are configured, all requests use global round-robin over all backends. See [routing.md](routing.md).

## Decision

The output of Switchyard's passive routing stage. A `Decision` records what should happen to a request — which action to take (`forward`, `static`, or `reject`), which backend was selected, and which location matched — without actually doing anything. No network I/O happens during decision-making. See [architecture.md](architecture.md).

## Action

One of three values that a `Decision` can carry:

- **`forward`** — proxy the request to a backend
- **`static`** — serve a file from disk
- **`reject`** — return an HTTP error to the client (404 when no location matched, 502 when a proxy location has no backends)

## Request Snapshot

The immutable internal `Request` value captured at the very start of every request, before any routing or forwarding happens. All inspection logic (routing, logging, variable resolution, header injection) reads from this snapshot, not from the live `*http.Request`. Headers are cloned at capture time so that later mutations made by `set_headers` never affect the logged copy or variable values. See [architecture.md](architecture.md).

## Variable

An nginx-style `$name` placeholder that resolves to a value derived from the request snapshot at runtime. Variables can be referenced in `set_headers` value templates and in log format strings (via `{var.NAME}`). All available variables are listed in [variables.md](variables.md).

## Template

A string that contains one or more variable references. Templates support two syntaxes: `$name` and `${name}`. They are compiled once at startup; an unknown variable name causes Switchyard to exit immediately rather than producing silently wrong output at runtime. Templates are used in `set_headers` values.

## Round-Robin

The backend selection algorithm. Each pool of backends (the global pool or a location's own pool) maintains an atomic counter. The counter is incremented on every request and taken modulo the pool size to pick a backend. The counter is lock-free (`atomic.Uint64`), so no synchronization overhead occurs under concurrent load.

## Stacking

The behavior where location-level settings *augment* global settings rather than replacing them. Specifically:

- A matched request fires **both** the global logger and the location's own logger (if each is configured). Neither overrides the other.
- A matched request has global `set_headers` applied first, then the location's `set_headers` on top. When the same header name appears in both, the location value wins; all other global headers are retained.

Stacking means global configuration is a baseline that every request goes through. Location configuration adds to it for matched requests.

## Fail-Fast

Switchyard validates the entire configuration at startup before accepting any traffic. If a backend URL is invalid, a regex fails to compile, a static root directory does not exist, a variable name is unknown, or a log format field is misspelled, Switchyard exits immediately with an error. No partial startup, no lazy validation.
