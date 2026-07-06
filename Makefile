# ouracli build tooling.
#
# `make build` produces ./oura with version metadata baked in via -ldflags;
# `make check` is what CI (and you, before sending a PR) should run.

BINARY    := oura
CMD       := ./cmd/oura
DIST      := dist

VERSION   := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE      := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS   := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: all
all: build

.PHONY: build
build: ## Build ./oura with version info, trimming local build paths from the binary.
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

.PHONY: test
test: ## Run the test suite.
	go test ./...

.PHONY: test-race
test-race: ## Run the test suite with the race detector.
	go test -race ./...

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: fmt-check
fmt-check: ## Fail if any file is not gofmt-clean.
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

.PHONY: check
check: vet fmt-check test-race ## Everything CI runs: vet, fmt-check, test-race.

.PHONY: clean
clean: ## Remove build artifacts.
	rm -f $(BINARY)
	rm -rf $(DIST)

.PHONY: help
help: ## List targets.
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | sed 's/:.*## /\t/'
