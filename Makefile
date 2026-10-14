# Developer loop for stillhere. Releases are built by GoReleaser (#12).

GO ?= go

# golangci-lint is pinned and run from the module cache, so every machine and
# CI lint with the same version. A golangci-lint binary built with an older Go
# refuses this Go 1.27 module. CGO_ENABLED=0 links it with Go's internal
# linker, which also sidesteps a host C linker that cannot read the macOS SDK.
GOLANGCI_LINT ?= CGO_ENABLED=0 $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build test lint fmt tidy

## build: compile bin/stillhere, static (no cgo), with version, commit and date
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/stillhere ./cmd/stillhere

## test: run every test against real git, with the race detector
test:
	$(GO) test -race -count=1 ./...

## lint: golangci-lint, including the depguard rules that enforce the dependency rule
lint:
	$(GOLANGCI_LINT) run ./...

## fmt: apply gofmt and goimports as configured in .golangci.yml
fmt:
	$(GOLANGCI_LINT) fmt

## tidy: prune and verify go.mod and go.sum
tidy:
	$(GO) mod tidy
	$(GO) mod verify
