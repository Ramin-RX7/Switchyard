# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

A [Makefile](Makefile) wraps the common commands (`make` or `make help` lists them). It's a task hub only — Go's toolchain does the real building.

```bash
make build      # go build -o Switchyard ./cmd/switchyard/   (capital -o avoids the switchyard/ dir clash)
make run        # go run ./cmd/switchyard/ -config switchyard.json
make reload     # ./Switchyard reload          (graceful hot reload of the running server)
make force-reload # ./Switchyard reload --force (force reload: cancel in-flight, best-effort 503)
make test       # go test ./...
make race       # go test -race ./...
make cover      # go test -cover ./...
make lint       # go vet ./... + gofmt check
make examples   # go build ./examples/...
make ci         # fmt-check + vet + build + examples + test + race
go test -run TestName ./...   # (still handy for a single test)
```

**Keep the Makefile in sync:** if you add or change a build/test/lint command, update [Makefile](Makefile) (and this block) so they don't drift.

The server listens on the address in the config's `listen` field, falling back to `:8091`. The turnkey binary (`./Switchyard -config switchyard.json`) also writes a pid file (default `switchyard.pid`; `-pidfile PATH` to relocate, `-pidfile ""` to disable) and drains in-flight requests (15s) on SIGINT/SIGTERM. Reload the config live with `./Switchyard reload` (graceful) or `./Switchyard reload --force` (force) — these read the pid file and signal the running process (graceful = SIGHUP, force = SIGUSR2, so `kill -HUP/-USR2 <pid>` also work); `make reload` / `make force-reload` wrap them.

## Documentation

Full feature documentation is in [`docs/`](docs/):

| File | What it covers |
|------|---------------|
| [docs/concepts.md](docs/concepts.md) | Glossary — Backend, Location, Decision, Action, Request snapshot, Variable, Template, Round-robin, Stacking, Fail-fast |
| [docs/architecture.md](docs/architecture.md) | Three-stage pipeline (Capture → Decide → Act), file map, extension pattern |
| [docs/config-reference.md](docs/config-reference.md) | Every JSON field with types, defaults, validation rules, and an annotated example |
| [docs/backends.md](docs/backends.md) | Backend config, round-robin selection, error handling, `loggingTransport` wrapping |
| [docs/routing.md](docs/routing.md) | Location matching (prefix/regex), proxy vs static types, `strip_prefix`, stacking |
| [docs/variables.md](docs/variables.md) | All `$variable` placeholders and where they can be used |
| [docs/set-headers.md](docs/set-headers.md) | Header injection, variable syntax, `Host` special case, stacking |
| [docs/logging.md](docs/logging.md) | Format fields, outputs, body capture, `loggingTransport`, `statusWriter` |
| [docs/extending.md](docs/extending.md) | SDK usage — overriding any of the 11 pluggable stages with your own code |
| [docs/testing.md](docs/testing.md) | Test suite layout, per-stage testing patterns, and the tests-required contributor rule |
| [docs/features.md](docs/features.md) | Running inventory of every implemented feature (⚙️ Config / 🧩 SDK), plus the pluggable-stage matrix |

**Keep [docs/features.md](docs/features.md) in sync:** every new feature MUST be added there, in the same format as the existing entries — a numbered `##` section with a one-paragraph description plus **⚙️ Config** (JSON keys) and **🧩 SDK** (interface, default, override field) lines; add a row to that file's pluggable-stages table when the feature introduces a new stage.

**Keep [README.md](README.md) in sync:** every new feature MUST also get a one-line bullet in the README's **Features** list (and, if it adds a new pluggable stage, keep the stage count accurate). The README is the front-page summary; [docs/features.md](docs/features.md) is the full inventory — both must reflect a newly introduced feature.

## Architecture

Switchyard is an importable library (`package switchyard`, import path `github.com/Ramin-RX7/Switchyard/switchyard`) living in the [switchyard/](switchyard/) directory. The `cmd/switchyard` binary is a thin consumer — it's the config-only turnkey mode. Both usage modes (config-only binary; SDK import) run off the same code. Entry points: `LoadConfig` ([switchyard/config.go](switchyard/config.go)), `New` + `Handler`/`ListenAndServe` ([switchyard/proxy.go](switchyard/proxy.go)). For zero-downtime reload there is an exported `Server` type (`Server{Addr, PidFile, Build}` + `Run`/`Start`/`Reload`, plus `SignalReload`/`ReadPidFile`) — `Build` is invoked at start and on every reload, so SDK stage overrides must be re-applied inside it. See [docs/extending.md](docs/extending.md#hot-reload-the-server-type).

The request lifecycle is split into three strictly separate stages — preserve this separation when adding features.

**Request flow** (`Proxy.handle` in [switchyard/proxy.go](switchyard/proxy.go)):

1. **Capture** — `captureRequest` ([switchyard/request.go](switchyard/request.go)) converts the live `*http.Request` into an immutable `Request` snapshot. Headers are cloned. All subsequent logic reads this value.
2. **Decide** — `p.Decider.Decide` (default `DefaultDecider` in [switchyard/decide.go](switchyard/decide.go)) is a pure function (no I/O, no side effects). Returns a `Decision` ([switchyard/decision.go](switchyard/decision.go)) with the chosen action and selected backend. Routing logic lives here and nowhere else.
3. **Act** — `Proxy.handleRequest` ([switchyard/proxy.go](switchyard/proxy.go)) delegates to `p.Actor.Act` (default `DefaultActor` in [switchyard/actor.go](switchyard/actor.go)) — the only stage that touches the outside world; it forwards, serves files, or rejects.

See [docs/architecture.md](docs/architecture.md) for the full pipeline diagram, package layout, and extension pattern.

**Pluggable stages (SDK).** All 11 core stages are interfaces with config-driven defaults, overridable by SDK users via exported fields on `Proxy`/`Location`: `Decider` (`p.Decider`), `Actor` (`p.Actor`), `Router` (`p.Router`), `BackendSelector` (`p.Selector`/`loc.Selector`, default `RoundRobinSelector`), `BackendPool` (`p.Pool`/`loc.Pool`, default `StaticPool`), `HeaderApplier` (`p.Headers`/`loc.Headers`, default `TemplateHeaderSetter`), `ResponseHeaderApplier` (`p.ResponseHeaders`/`loc.ResponseHeaders`, default `TemplateResponseHeaderSetter`), `StaticServer` (`loc.Static`, default `FileServer`), `AccessController` (`loc.Access`, default `IPAccessControl`; nil = unrestricted), `ResponseGenerator` (`loc.Responder` for response locations, `p.NotFound`/`p.BadGateway`/`p.MethodNotAllowed`/`p.Forbidden` for global error responses, default `TemplateResponder`), `Logger` (`p.Logger`/`loc.Logger`, default `FormatLogger`). See [docs/extending.md](docs/extending.md).

**Key design rules:**
- All routing logic belongs in the `Decider`. Never perform I/O there.
- All side effects belong in the `Actor`. Never make routing decisions there.
- Adding a new action type: constant in `decision.go` → case in `DefaultDecider.Decide` → case in `DefaultActor.Act`.
- Behavior parity: `New(cfg)` must reproduce the turnkey binary's behavior exactly; overrides are additive.
- Tests required: behavior changes ship with tests (flow + default + custom override); run `go test ./...`. See [docs/testing.md](docs/testing.md).
- Feature inventory: a new feature ships with an entry in [docs/features.md](docs/features.md), same format as the others (⚙️ Config / 🧩 SDK), plus a pluggable-stages table row if it adds a stage, AND a one-line bullet in the [README.md](README.md) Features list.

## Feature Quick-Reference

**Backends** ([docs/backends.md](docs/backends.md)): each backend wraps `httputil.NewSingleHostReverseProxy` with a custom `ErrorHandler` (→ 502 on failure). Selection is lock-free round-robin (`atomic.Uint64`). `X-Forwarded-For` is maintained automatically.

**Location routing** ([docs/routing.md](docs/routing.md)): optional `locations` array; top-to-bottom, first-match wins. Each location carries its own backend pool and independent round-robin counter. `type: "static"` serves files via `http.FileServer`; `type: "response"` returns a canned Switchyard-generated response. No match → 404.

**Method routing** ([docs/routing.md](docs/routing.md#method-routing)): after path→location, backends are filtered by HTTP method. Each backend may declare `methods` (case-insensitive; empty/omitted = accept any); round-robin runs over the method-filtered subset (shared counter). A matched location with no method-accepting backend → **405** via the configurable `method_not_allowed` responder, with an auto-generated `Allow` header. `Backend.Accepts(method)` is exported for method-aware custom selectors; reroute stays within method-eligible backends.

**IP access control** ([docs/routing.md](docs/routing.md#ip-access-control)): `whitelist`/`blacklist` (single IP or CIDR, v4/v6) at two tiers — top-level (global) and per-location. The global tier is checked in the `Decider` **before** location matching (so it also gates no-match/no-location paths); the per-location tier right after the location matches (before method routing/backend selection), applying to proxy/static/response locations. The tiers stack (**AND** — a request must pass both). Within a tier: blacklist wins over whitelist, non-empty whitelist is allow-list only, empty/omitted = unrestricted. Denial at either → **403** via the configurable `forbidden` responder (SDK `p.Forbidden`). Uses `RemoteAddr` (custom `AccessController` via `p.Access` global / `loc.Access` per-location for `X-Forwarded-For`/other logic). Bad entries fail fast at startup.

**Response generator** ([docs/config-reference.md](docs/config-reference.md#response)): abstract `ResponseGenerator` (default `TemplateResponder`) producing status + headers + body with `$variable` substitution. Powers the `type: "response"` location plus the configurable built-in error responses — top-level `backend_error` (502 unreachable/empty pool), `not_found` (404 no match), `method_not_allowed` (405 no method-accepting backend; SDK `p.MethodNotAllowed`), `forbidden` (403 access-denied; SDK `p.Forbidden`), and the `overflow` reject response (now with `headers` + variable-capable body).

**Variables** ([docs/variables.md](docs/variables.md)): nginx-style `$name`/`${name}` placeholders resolved from the request snapshot. Used in `set_headers` values, `{var.NAME}` log fields, and `response`/`backend_error`/`not_found`/`method_not_allowed`/`overflow` bodies and headers. Includes `$time_iso8601` and `$time_unix` (request-receipt time). Validated at startup.

**set_headers** ([docs/set-headers.md](docs/set-headers.md)): map of header name → template value; applied before forwarding. Global and location headers stack (`applyStackedHeaders`): location wins on conflicts, all other globals retained.

**set_response_headers** ([docs/set-headers.md](docs/set-headers.md#response-headers-set_response_headers)): response-side mirror of `set_headers` — map of header name → template value (same `$variables`) set on the response returned to the client. Set/override on key conflict; global then location stacking (location wins, other globals retained). Applies uniformly to proxied, static, and Switchyard-generated (`response`/error) responses via a lazy ResponseWriter wrapper that injects just before the status line, so streaming and WebSocket upgrades keep working (forwards Flush/Hijack). No `Host` special-case (request-only). Not applied to the pre-routing global-overflow 503 (that carries `overflow.headers`).

**Hot reload** ([docs/architecture.md](docs/architecture.md#configuration-reload-hot-reload)): the binary swaps `switchyard.json` in-process (atomic `Proxy` generation swap; one process, one listening socket) with no restart and no dropped connections. `switchyard reload` = graceful (SIGHUP; in-flight requests finish on the old config, new requests use the new one); `switchyard reload --force` = force (SIGUSR2; in-flight requests cancelled → best-effort 503). Fail-safe: an invalid new config is rejected and logged, and the running config keeps serving. Everything the config rebuilds reloads live; the `listen` address and `server` timeouts (owned by the running `http.Server`) need a full restart. SDK: the exported `Server` type (see Architecture above).

**Logging** ([docs/logging.md](docs/logging.md)): optional `logging` block with `format`, `outputs` (console/file). Format uses `{field}` and `{group.param}` placeholders; compiled and validated at startup. Both global and location loggers fire for matched requests. Bodies buffered on demand only when referenced in the format.

## Working philosophy

[AGENTS.md](AGENTS.md) is the governing document for how to work here. In short: clarity over complexity, correctness over cleverness, small verifiable steps, and no abstraction or mechanism that isn't justified by an immediate need. A change is only acceptable if it improves the system without making it harder to understand. When in doubt, prefer the minimal solution.
