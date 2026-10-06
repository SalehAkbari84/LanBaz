# LanBaz developer tasks.
#
# The Go toolchain and Node are expected in PATH. Override GO / NPM when they
# live somewhere unusual, for example: make test GO=/opt/go/bin/go

# Go is taken from PATH, else from the toolchain bundled next to the repo.
BUNDLED_GO := $(wildcard $(CURDIR)/../.tools/go/bin/go.exe $(CURDIR)/../.tools/go/bin/go)
GO ?= $(if $(shell command -v go 2>/dev/null),go,$(if $(BUNDLED_GO),$(firstword $(BUNDLED_GO)),go))
NPM ?= npm
EXE := $(if $(filter Windows_NT,$(OS)),.exe,)
GOBIN_DIR := $(CURDIR)/bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.buildTime=$(BUILD_TIME)

.PHONY: all
all: build test

## build: compile lanbazd and lanbazctl into ./bin
.PHONY: build
build:
	@mkdir -p $(GOBIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(GOBIN_DIR)/lanbazd$(EXE) ./core/cmd/lanbazd
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(GOBIN_DIR)/lanbazctl$(EXE) ./core/cmd/lanbazctl
	@echo "built $(GOBIN_DIR)/lanbazd$(EXE) and $(GOBIN_DIR)/lanbazctl$(EXE) ($(VERSION))"

## test: run the Go test suite
.PHONY: test
test:
	$(GO) test ./core/...

## test-race: run the Go test suite with the race detector (needs a C toolchain)
.PHONY: test-race
test-race:
	CGO_ENABLED=1 $(GO) test -race ./core/...

## cover: run the tests and report coverage per package
.PHONY: cover
cover:
	$(GO) test -coverprofile=coverage.out ./core/...
	$(GO) tool cover -func=coverage.out | tail -20

## vet: run go vet
.PHONY: vet
vet:
	$(GO) vet ./core/...

## fmt: format the Go sources
.PHONY: fmt
fmt:
	$(GO) fmt ./core/...

## check: fmt check + vet + tests, the gate before opening a phase
.PHONY: check
check: vet test

## sidecar: build lanbazd into app/src-tauri/binaries for the Tauri shell
.PHONY: sidecar
sidecar:
	GO_BIN=$(GO) ./scripts/build-sidecar.sh

## app-install: install the frontend dependencies
.PHONY: app-install
app-install:
	cd app && $(NPM) install

## app-typecheck: typecheck the frontend without bundling
.PHONY: app-typecheck
app-typecheck:
	cd app && $(NPM) run typecheck

## app-build: typecheck and bundle the frontend
.PHONY: app-build
app-build:
	cd app && $(NPM) run build

## dev: build the sidecar and run the Tauri shell in dev mode
.PHONY: dev
dev: sidecar
	cd app && $(NPM) run tauri dev

## run: run the daemon in the foreground
.PHONY: run
run:
	$(GO) run ./core/cmd/lanbazd --log-level debug

## acceptance: end-to-end check of the Phase 0 acceptance criteria
.PHONY: acceptance
acceptance: build
	GO_BIN=$(GO) ./scripts/acceptance.sh

## clean: remove build artefacts
.PHONY: clean
clean:
	rm -rf $(GOBIN_DIR) app/dist app/src-tauri/target coverage.out

## help: list the targets
.PHONY: help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'