# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
go build -o Switchyard ./cmd/switchyard/        # build the binary
go run ./cmd/switchyard/ -config switchyard.json # run directly (defaults to ./switchyard.json)
go test ./...                                    # run all tests (no tests exist yet)
go test -run TestName ./...                      # run a single test
go vet ./...                                     # static checks
gofmt -l ./cmd/switchyard/                       # list files needing formatting
```

The server listens on the address in the config's `listen` field, falling back to `:8091`.

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

## Architecture

Switchyard is a single-package (`package main`) HTTP reverse proxy. The request lifecycle is split into three strictly separate stages — preserve this separation when adding features.

**Request flow** (`Proxy.handle` in [proxy.go](cmd/switchyard/proxy.go)):

1. **Capture** — `captureRequest` ([request.go](cmd/switchyard/request.go)) converts the live `*http.Request` into an immutable `Request` snapshot. Headers are cloned. All subsequent logic reads this value.
2. **Decide** — `Proxy.decide` ([proxy.go](cmd/switchyard/proxy.go)) is a pure function (no I/O, no side effects). Returns a `Decision` ([decision.go](cmd/switchyard/decision.go)) with the chosen action and selected backend. Routing logic lives here and nowhere else.
3. **Act** — `Proxy.handleRequest` + `Proxy.act` ([proxy.go](cmd/switchyard/proxy.go)) is the only stage that touches the outside world — it forwards, serves files, or rejects.

See [docs/architecture.md](docs/architecture.md) for the full pipeline diagram, file map, and extension pattern.

**Key design rules:**
- All routing logic belongs in `decide`. Never perform I/O there.
- All side effects belong in `act`. Never make routing decisions there.
- Adding a new action type: constant in `decision.go` → case in `decide` → case in `act`.

## Feature Quick-Reference

**Backends** ([docs/backends.md](docs/backends.md)): each backend wraps `httputil.NewSingleHostReverseProxy` with a custom `ErrorHandler` (→ 502 on failure). Selection is lock-free round-robin (`atomic.Uint64`). `X-Forwarded-For` is maintained automatically.

**Location routing** ([docs/routing.md](docs/routing.md)): optional `locations` array; top-to-bottom, first-match wins. Each location carries its own backend pool and independent round-robin counter. `type: "static"` serves files via `http.FileServer`. No match → 404.

**Variables** ([docs/variables.md](docs/variables.md)): nginx-style `$name`/`${name}` placeholders resolved from the request snapshot. Used in `set_headers` values and `{var.NAME}` log fields. Validated at startup.

**set_headers** ([docs/set-headers.md](docs/set-headers.md)): map of header name → template value; applied before forwarding. Global and location headers stack (`applyStackedHeaders`): location wins on conflicts, all other globals retained.

**Logging** ([docs/logging.md](docs/logging.md)): optional `logging` block with `format`, `outputs` (console/file). Format uses `{field}` and `{group.param}` placeholders; compiled and validated at startup. Both global and location loggers fire for matched requests. Bodies buffered on demand only when referenced in the format.

## Working philosophy

[AGENTS.md](AGENTS.md) is the governing document for how to work here. In short: clarity over complexity, correctness over cleverness, small verifiable steps, and no abstraction or mechanism that isn't justified by an immediate need. A change is only acceptable if it improves the system without making it harder to understand. When in doubt, prefer the minimal solution.
