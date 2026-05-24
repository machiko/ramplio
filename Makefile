BINARY     := ramplio
BUILD_DIR  := ./bin
CMD        := ./cmd/ramplio
GO         := go
GOFLAGS    :=

.PHONY: all build test race lint clean run help

all: build

## build: compile the binary to ./bin/ramplio
build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY) $(CMD)

## test: run all tests
test:
	$(GO) test ./...

## race: run all tests with race detector (required before every commit touching concurrency)
race:
	$(GO) test -race ./...

## cover: run tests and print coverage summary
cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out

## lint: run golangci-lint
lint:
	golangci-lint run

## run: build and run with a scenario file  (usage: make run SCENARIO=testdata/example.yaml)
run: build
	$(BUILD_DIR)/$(BINARY) run --scenario $(SCENARIO)

## install: build and copy binary to ~/.local/bin (already in PATH, no sudo needed)
install: build
	@mkdir -p $(HOME)/.local/bin
	cp $(BUILD_DIR)/$(BINARY) $(HOME)/.local/bin/$(BINARY)
	@echo "Installed → $(HOME)/.local/bin/$(BINARY)"

## clean: remove build artifacts
clean:
	rm -rf $(BUILD_DIR) coverage.out

## help: print this help
help:
	@grep -E '^##' Makefile | sed 's/^## /  /'
