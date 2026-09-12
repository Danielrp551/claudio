BINARY      := claudio
PKG         := ./...
CMD         := ./cmd/claudio
BIN_DIR     := bin
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)
GOFILES     := $(shell git ls-files '*.go' 2>/dev/null)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the binary into bin/
	@mkdir -p $(BIN_DIR)
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(CMD)

.PHONY: install
install: ## Install the binary into GOBIN
	go install -trimpath -ldflags "$(LDFLAGS)" $(CMD)

.PHONY: test
test: ## Run tests with the race detector
	go test -race -count=1 $(PKG)

.PHONY: cover
cover: ## Run tests and write a coverage profile
	go test -race -count=1 -coverprofile=coverage.txt -covermode=atomic $(PKG)
	go tool cover -func=coverage.txt | tail -1

.PHONY: fmt
fmt: ## Format the code
	gofmt -s -w .

.PHONY: fmt-check
fmt-check: ## Fail if any file is not formatted
	@out=$$(gofmt -s -l .); \
	if [ -n "$$out" ]; then echo "not formatted:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## Run go vet
	go vet $(PKG)

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run

.PHONY: tidy
tidy: ## Tidy the module and fail if it changed
	go mod tidy
	@git diff --exit-code go.mod go.sum 2>/dev/null || \
		{ echo "go.mod or go.sum changed, commit the result of go mod tidy"; exit 1; }

.PHONY: check
check: fmt-check vet lint test ## Everything CI runs

.PHONY: clean
clean: ## Remove build artefacts
	rm -rf $(BIN_DIR) coverage.txt
