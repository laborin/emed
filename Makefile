.PHONY: all build build-all run install clean test test-race test-coverage bench fmt fmt-check vet lint lint-all vuln tidy-check ci deps deps-check dev help

BINARY_NAME := emed
MAIN_PATH   := ./cmd/emed
BUILD_DIR   := build
INSTALL_PATH ?= /usr/local/bin

# Pinned tool versions, keep in sync with .github/workflows/ci.yml
GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION   := v1.9.0

GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
GOVULNCHECK   := go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

# Version comes from the nearest git tag (v0.5.0, v0.5.0-3-gabc123, ...), "dev" without tags
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-s -w -X github.com/laborin/emed/internal/config.version=$(VERSION)"

# Lint only new code by default, the existing debt is tracked in docs/ROADMAP.md
LINT_BASE ?= origin/main

all: build

## build: build the binary for the current platform
build:
	go build $(LDFLAGS) -o $(BINARY_NAME) $(MAIN_PATH)

## build-all: cross compile for every supported platform (linux, darwin)
build-all:
	GOOS=linux  GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64  $(MAIN_PATH)
	GOOS=linux  GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64  $(MAIN_PATH)
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(MAIN_PATH)
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(MAIN_PATH)

## run: build and run emed in the current directory
run: build
	./$(BINARY_NAME)

## install: install the binary in INSTALL_PATH (default /usr/local/bin)
install: build
	sudo install -d $(INSTALL_PATH)
	sudo install -m 755 $(BINARY_NAME) $(INSTALL_PATH)/$(BINARY_NAME)

## clean: remove build artifacts
clean:
	go clean
	rm -f $(BINARY_NAME) coverage.out coverage.html
	rm -rf $(BUILD_DIR)

## test: run the unit tests
test:
	go test ./...

## test-race: run the tests with the race detector, like CI does
test-race:
	go test -race ./...

## test-coverage: generate coverage.html
test-coverage:
	go test -cover -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

## bench: run benchmarks
bench:
	go test -bench=. -benchmem ./...

## fmt: format all the code
fmt:
	gofmt -s -w .

## fmt-check: fail if some file is not formatted
fmt-check:
	@out="$$(gofmt -s -l .)"; if [ -n "$$out" ]; then echo "Not formatted:"; echo "$$out"; exit 1; fi

## vet: run go vet
vet:
	go vet ./...

## lint: run golangci-lint on the changes since LINT_BASE (origin/main)
lint:
	$(GOLANGCI_LINT) run --new-from-merge-base=$(LINT_BASE) ./...

## lint-all: run golangci-lint on all the code, including old issues
lint-all:
	$(GOLANGCI_LINT) run ./...

## vuln: check dependencies and stdlib for known vulnerabilities
vuln:
	$(GOVULNCHECK) ./...

## tidy-check: fail if go.mod or go.sum are not tidy
tidy-check:
	go mod tidy -diff

## ci: run the same checks than CI, run it before open a pull request
ci: fmt-check tidy-check vet lint test-race build

## deps: download and tidy dependencies
deps:
	go mod download
	go mod tidy

## deps-check: list available dependency updates
deps-check:
	go list -u -m all

## dev: build with debug symbols, for dlv
dev:
	go build -gcflags="all=-N -l" -o $(BINARY_NAME) $(MAIN_PATH)

## help: show this help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
