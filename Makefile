GO      ?= go
BIN     := bin/incus-tui
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build run test test-integration lint fmt tidy vuln snapshot docker clean

build: ## build the binary
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/incus-tui

run: build ## build and run
	./$(BIN)

test: ## unit tests (race)
	$(GO) test -race ./...

test-integration: ## live tests (boots a VM; needs a local Incus + KVM)
	$(GO) test -tags integration -timeout 600s ./internal/incus/...

lint: ## golangci-lint
	golangci-lint run ./...

fmt: ## format
	gofmt -w internal cmd

tidy: ## tidy modules
	$(GO) mod tidy

vuln: ## vulnerability scan
	govulncheck ./...

snapshot: ## local goreleaser snapshot (no publish)
	goreleaser release --snapshot --clean --skip=publish

docker: ## build the container image
	docker build -t incus-tui:dev .

clean: ## remove build artifacts
	rm -rf bin dist
