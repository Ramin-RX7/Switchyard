# Contributing to Switchyard

Thanks for your interest in improving Switchyard. This is a small, real-world
reverse proxy that must stay understandable and stable over time, so
contributions are held to that bar. This document is the short human-facing
guide; the full working rules live in the governing documents (see below) and
apply to every change.

## Getting started

```bash
make build      # build the binary
make test       # go test ./...
make ci         # fmt-check + vet + build + examples + test + race — run this before opening a PR
```

`make` (or `make help`) lists every available command.

## Guiding principles

Switchyard favors **clarity over complexity, correctness over cleverness, small
verifiable steps, and minimal solutions**. Don't introduce abstraction or
mechanism that isn't justified by an immediate need. A change is only acceptable
if it improves the system without making it harder to understand.

## The governing documents — read and follow them

The rules in these files are **not optional** and are **not limited to AI
agents**. They govern all code, logic, and architecture in this repository, and
every contribution (human or agent) must satisfy them:

- **[AGENTS.md](AGENTS.md)** — the working philosophy, engineering discipline,
  the three-stage pipeline (Capture → Decide → Act) and the exact steps for
  extending it, the pluggable-stage mold, the tests-required rule, and the
  keep-docs-in-sync rule.
- **[CLAUDE.md](CLAUDE.md)** — the architecture overview, key design rules, and
  the same sync/testing obligations, with a per-feature quick reference.

Whatever those documents require of an agent, they require of you: the pipeline
separation (routing logic only in the `Decider`, side effects only in the
`Actor`), the additive-override contract (`New(cfg)` must reproduce turnkey
behavior exactly), and the documentation obligations below.

## Requirements for every change

1. **Tests are required.** Behavior changes ship with tests along the three axes
   used throughout the suite: the real request flow works, the default behavior
   is correct, and a custom user override is honored. A new pluggable stage
   ships with its own `<stage>_test.go` (default + custom-override + end-to-end).
   Run `make ci` (or `go test ./...` and `go test -race ./...`) before finishing.
   See [docs/testing.md](docs/testing.md).

2. **Keep the docs in sync.** Every new feature MUST be added to
   [docs/features.md](docs/features.md) in the existing format (a numbered `##`
   section with a description plus **⚙️ Config** and **🧩 SDK** lines, and a
   pluggable-stages table row if it adds a stage), AND get a one-line bullet in
   the [README.md](README.md) Features list. Update the relevant `docs/` page for
   the area you touched.

3. **Keep the Makefile in sync.** If you add or change a build/test/lint command,
   update [Makefile](Makefile) and the command lists in
   [CLAUDE.md](CLAUDE.md) / [docs/testing.md](docs/testing.md) so they don't
   drift.

4. **Follow the extension mold.** New pluggable stage = interface + a default
   struct built from config in `New`, wired to an exported `Proxy`/`Location`
   field, with defaults reproducing prior behavior. New action type = constant in
   `decision.go` → (location kind in `location.go` if needed) → case in
   `DefaultDecider.Decide` → case in `DefaultActor.Act`, in that order. See
   [docs/extending.md](docs/extending.md).

## Submitting

- Keep changes small and reviewable in isolation; preserve existing behavior
  unless a change is explicitly intended.
- Make sure `make ci` passes.
- Open a pull request describing what changed and why, and note which docs you
  updated.
