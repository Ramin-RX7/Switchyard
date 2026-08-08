# Switchyard - AGENTS.md

## Purpose

Switchyard is a systems project focused on building a reverse proxy in Go through incremental, practical development.

---

## Core Intent

All work should prioritize:

* clarity over complexity
* correctness over cleverness
* incremental progress over large changes
* simplicity over abstraction

---

## Engineering Discipline

Agents should treat the codebase as a real system that must remain understandable and maintainable at all times.

Avoid unnecessary complexity in design, structure, or reasoning.

---

## Development Approach

Work should proceed in small, verifiable steps.

Each change should:

* be understandable in isolation
* not require global refactoring
* preserve existing behavior unless explicitly intended

---

## Design Boundaries

Keep the system focused on its core purpose.

Do not introduce concepts or mechanisms that are not clearly justified by immediate needs.

Prefer minimal solutions that solve the problem directly.

---

## Reliability Mindset

Treat errors, failures, and edge cases as normal conditions.

The system should behave predictably under failure, and never rely on hidden assumptions.

---

## Code Quality Expectations

Code should remain:

* readable without external context
* organized by clear responsibility
* free from unnecessary generalization

If a design decision feels optional, it should be avoided.

---

## Evaluation of Changes

A change is only acceptable if it improves the system without making it harder to understand.

If a change increases complexity, it must be strongly justified by necessity.

---

## Working Philosophy

Think like a builder of a small, real-world system that must remain stable over time, not like a designer of a theoretical framework.

---

## Documentation

Feature documentation lives in [`docs/`](docs/). Every feature has its own document. When adding a new capability, add or update the corresponding doc. The governing documents are:

- [`docs/concepts.md`](docs/concepts.md) — definitions of all terms used in the codebase and config
- [`docs/architecture.md`](docs/architecture.md) — the three-stage pipeline and the rules for extending it
- [`docs/config-reference.md`](docs/config-reference.md) — every configuration field

---

## Extending Switchyard

Switchyard is both a turnkey binary and an importable library. Two kinds of extension:

**As an SDK user (no core edits).** Every core stage is an interface with a config-driven default. Override a stage by assigning your own implementation to an exported field on `Proxy`/`Location`, or embed the default and override one method. `New(cfg)` reproduces the turnkey behavior exactly; overrides are additive. See [`docs/extending.md`](docs/extending.md).

**As a contributor.** When adding a new pluggable stage, follow the established mold: interface + a default struct built from config in `New`, wired to an exported field, with defaults reproducing prior behavior. When adding a new action type, in this order:

1. Add a new `Action` constant in `decision.go`
2. Add a compiled location kind in `location.go` (`compileLocations`) if the action requires a new location type
3. Add a case in `DefaultDecider.Decide` (`decide.go`) that returns a `Decision` with the new action — no I/O, just logic
4. Add a case in `DefaultActor.Act` (`actor.go`) that performs the side effect

Each step compiles independently and can be reviewed before moving on. Keep decide passive; keep the actor the only place with side effects.

---

## Testing (required)

Changes to behavior must come with tests. Every stage/feature is tested along three axes: the real request flow works, the default behavior is correct, and a custom user implementation is honored. When you change a stage's logic, update its `*_test.go` accordingly; a new pluggable stage ships with its own `<stage>_test.go` (default + custom-override + an end-to-end assertion). Run `make ci` (or `go test ./...` + `-race`) before finishing. See [`docs/testing.md`](docs/testing.md) for the layout, helpers, and full rule.

---

## Tooling

Common commands live in the [`Makefile`](Makefile) (`make` lists them). It's a task hub, not the build system — Go's toolchain does the real building. **If you add or change a build/test/lint command, update the `Makefile` (and the command list in `CLAUDE.md` / `docs/testing.md`) so they don't drift.**
