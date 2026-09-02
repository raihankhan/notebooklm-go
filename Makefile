# Makefile for github.com/raihankhan/notebooklm-go
#
# Conventions
#   - VERSION is overridable: `make build VERSION=0.2.0`
#   - All targets are read-only against the source tree except `clean` and `build`
#     (build writes to ./bin).
#   - `make check` is the umbrella target CI invokes.

VERSION         ?= 0.1.0-dev
PKG             := github.com/raihankhan/notebooklm-go
BIN_DIR         := bin
CMD             := ./cmd/notebooklm
COMMIT          := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE            := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS         := -X $(PKG)/internal/buildinfo.Version=$(VERSION) \
                   -X $(PKG)/internal/buildinfo.Commit=$(COMMIT) \
                   -X $(PKG)/internal/buildinfo.Date=$(DATE)

GO              ?= go
GOLANGCI_LINT   ?= golangci-lint

.PHONY: build check fmt vet lint test test-cassette test-e2e cover boundarycheck clean release help

build: ## Compile cmd/notebooklm into ./bin/ with ldflags-injected version
	@mkdir -p $(BIN_DIR)
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/notebooklm $(CMD)

check: fmt vet lint test boundarycheck build ## Run all checks (the umbrella CI target)
	@echo "check: OK"

fmt: ## Verify gofmt formatting (no diff on empty module)
	@bash -c 'diff -u <(echo -n) <(gofmt -l . | grep -v "^.worktrees/")'

vet: ## Run go vet
	$(GO) vet ./...

lint: ## Run golangci-lint (installed by CI)
	$(GOLANGCI_LINT) run

test: ## Run unit tests with the race detector
	$(GO) test -race ./...

test-cassette: ## Run the cassette match-tuple and credential-guard tests
	$(GO) test -run 'TestNoCredentialInCassettes|TestMatchTuplePinned' ./internal/web/policy/... ./internal/tools/cassette/...

test-e2e: ## Run end-to-end tests (stubs until later phase)
	@echo "e2e tests not yet implemented"

cover: ## Produce cover.out coverage profile
	$(GO) test -coverprofile=cover.out ./...

boundarycheck: ## Verify every package respects docs/AGENTS.md rule 5
	@echo "  CHECK  boundary rules"
	@$(GO) run ./internal/tools/boundarycheck

clean: ## Remove build artifacts
	rm -f cover.out
	rm -rf $(BIN_DIR)

release: ## Cross-compile releases (stubbed until Phase 13)
	@echo "release not yet implemented (lands in Phase 13)"

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "Targets:\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  %-16s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
