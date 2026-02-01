.PHONY: all build test test-verbose test-coverage clean install lint fmt

# Build variables
BINARY_NAME=tunnelmesh
BUILD_DIR=bin
GO=go

# Version info
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS=-ldflags "-X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildTime=$(BUILD_TIME)"

all: build

build: $(BUILD_DIR)/$(BINARY_NAME)

$(BUILD_DIR)/$(BINARY_NAME):
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/tunnelmesh

test:
	$(GO) test -race ./...

test-verbose:
	$(GO) test -race -v ./...

test-coverage:
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

clean:
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

install: build
	cp $(BUILD_DIR)/$(BINARY_NAME) /usr/local/bin/

lint:
	golangci-lint run ./...

fmt:
	$(GO) fmt ./...
	goimports -w .

# Development helpers
dev-server: build
	./$(BUILD_DIR)/$(BINARY_NAME) serve --config server.yaml

dev-peer: build
	./$(BUILD_DIR)/$(BINARY_NAME) join --config peer.yaml

# Generate SSH keys for testing
gen-keys:
	@mkdir -p ~/.tunnelmesh
	@ssh-keygen -t ed25519 -f ~/.tunnelmesh/id_ed25519 -N "" -C "tunnelmesh"
	@echo "Keys generated in ~/.tunnelmesh/"
