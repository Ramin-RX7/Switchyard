# Switchyard task runner.
#
# NOTE: `make` is NOT the build system here — the Go toolchain already caches and
# does incremental builds. This Makefile is just a task hub so the exact command
# invocations (e.g. the capital -o Switchyard, -race) live in one place and
# `make ci` "just works". Run `make` (or `make help`) to list targets.
#
# KEEP IN SYNC: if you add or change a build/test/lint command anywhere in the
# project (or in CLAUDE.md / docs/testing.md), update this Makefile too.

BINARY := Switchyard
CONFIG := switchyard.json

.DEFAULT_GOAL := help
.PHONY: help build run reload force-reload test race cover vet fmt fmt-check tidy examples lint ci clean

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-13s\033[0m %s\n", $$1, $$2}'

build: ## Compile the binary to ./Switchyard
	go build -o $(BINARY) ./cmd/switchyard/

run: ## Run the turnkey binary against switchyard.json
	go run ./cmd/switchyard/ -config $(CONFIG)

reload: ## Tell the running server to reload config gracefully (in-flight requests drain)
	./$(BINARY) reload

force-reload: ## Tell the running server to reload config, cancelling in-flight requests (503)
	./$(BINARY) reload --force

test: ## Run all tests
	go test ./...

race: ## Run all tests with the race detector
	go test -race ./...

cover: ## Run tests with a coverage summary
	go test -cover ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format all Go files in place
	gofmt -w .

fmt-check: ## Fail if any Go file is not gofmt-clean (CI)
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "unformatted files:"; echo "$$out"; exit 1; fi

tidy: ## Tidy go.mod / go.sum
	go mod tidy

examples: ## Build the SDK examples
	go build ./examples/...

lint: vet fmt-check ## Static checks (go vet + gofmt)

ci: fmt-check vet build examples test race ## Everything CI should run

clean: ## Remove build artifacts
	rm -f $(BINARY)
