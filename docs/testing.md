# Testing

Switchyard's tests live beside the code in [`switchyard/`](../switchyard/) as `*_test.go` files. They exist to guarantee three things for **every** stage/feature:

1. **Flow / IO** — the real request→response path works and produces the accepted output.
2. **Default behavior** — the built-in implementation is correct (e.g. round-robin actually rotates).
3. **Custom behavior** — a user-supplied implementation assigned to the stage's field is honored and behaves as intended.

## Running

The [Makefile](../Makefile) wraps these (`make test`, `make race`, `make cover`, `make ci`). Raw commands:

```bash
go test ./...                 # run everything   (make test)
go test ./switchyard/         # just the library
go test -run TestDecide ./... # a subset by name
go test -race ./...           # detect data races (round-robin is lock-free)  (make race)
go test -cover ./...          # coverage summary  (make cover)
```

`go vet ./...` and `gofmt -l .` should always be clean (`make lint`). If you add or change a command here, update the [Makefile](../Makefile) too.

## How it's organized

Two Go packages coexist in the `switchyard/` directory:

- **`package switchyard` (white-box)** — for unexported pure functions only: request capture, variable/template resolution, log-format compilation and rendering. Files: `request_test.go`, `vars_test.go`, `logging_internal_test.go`.
- **`package switchyard_test` (black-box)** — for everything behavioral. It drives only the exported API (`New`, `Handler`, the interface fields), so it exercises exactly what an SDK user sees. This is most of the suite: one file per stage (`decide_test.go`, `selector_test.go`, `router_test.go`, `pool_test.go`, `headers_test.go`, `static_test.go`, `actor_test.go`, `logging_test.go`), plus `proxy_test.go` (validation + end-to-end) and `example_test.go` (compile-checked doc snippets).

Prefer black-box tests: they prove the public contract and can't accidentally depend on internals. Reach for white-box only for pure helpers with no public entry point.

## The patterns (shared helpers in `helpers_test.go`)

- **Fake backends** — `newEchoBackend(t, id)` starts a real `httptest.Server` that records the headers it received and echoes its `id`. Use it to assert routing, load distribution, header injection, and `X-Forwarded-For`.
- **Driving a request** — `serve(p, method, target)` runs one request through `p.Handler()` against an `httptest.NewRecorder()` (the proxy runs in-process; only backends are real servers). Returns the recorder for status/body assertions.
- **Custom-behavior fakes** — small types implementing a stage interface (`recordingLogger`, `fixedSelector`, `teapotDecider`, `headerRouter`, `filterPool`, `sentinelStatic`, `sentinelActor`, …). Assign one to the corresponding `Proxy`/`Location` field, then assert it's honored.

### Testing each kind of stage

- **Pure stage (Decide, Router, Selector, Pool):** call the method directly with a constructed `Request` and assert the returned `Decision`/`*Location`/`*Backend`. No I/O needed.
- **Side-effecting stage (Actor, StaticServer):** drive through `Handler`/`serve` (or call `Act`/`Serve` with a recorder) and assert status + body.
- **Config-driven default:** build a `Config`, call `New`, and assert the default's observable behavior (e.g. round-robin alternation, headers reaching the backend).
- **Custom override (axis 3):** after `New`, assign your fake to `p.<Stage>` or `loc.<Stage>` and assert the new behavior — this is the same thing an SDK user does.
- **Logger output:** there's no writer injection, so either unit-test `logFormat.render` (white-box) or configure a `file` output to a `t.TempDir()` path and read it back (black-box).

## Contributor rule (required)

**Any change to a stage's logic MUST come with tests.** Specifically:

- Changing an existing stage's behavior → update/extend its `*_test.go` so the three axes (flow, default, custom) still hold, and add a case for the new behavior.
- Adding a **new pluggable stage** → ship a `<stage>_test.go` with: a default-behavior test, a custom-override test (assign a fake to the field and assert it's honored), and at least one assertion of the stage in the end-to-end flow (`proxy_test.go`).
- Fixing a bug → add a test that fails before the fix and passes after.

A quick way to confirm a test actually asserts behavior (not just executes it): temporarily break the code it covers and check the test fails.

Keep tests table-driven where it reduces repetition, name them `Test<Stage><Behavior>`, and use `t.TempDir()` for any filesystem needs so nothing leaks between runs.
