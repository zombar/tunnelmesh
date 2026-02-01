.PHONY: all build build-force test test-verbose test-coverage clean install lint fmt \
        dev-server dev-peer gen-keys release release-all push-release \
        docker-build docker-up docker-down docker-logs docker-clean docker-test

# Build variables
BINARY_NAME=tunnelmesh
BUILD_DIR=bin
GO=go

# Version info (use short commit ID if no tag)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS=-ldflags "-s -w -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildTime=$(BUILD_TIME)"

# Platforms for cross-compilation
PLATFORMS=linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

all: build

# Build for current platform
build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/tunnelmesh

# Force rebuild even if binary exists
build-force: clean build

# Run tests
test:
	$(GO) test -race ./...

test-verbose:
	$(GO) test -race -v ./...

test-coverage:
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

# Clean build artifacts
clean:
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

# Install to /usr/local/bin
install: build
	cp $(BUILD_DIR)/$(BINARY_NAME) /usr/local/bin/

# Linting and formatting
lint:
	golangci-lint run ./...

fmt:
	$(GO) fmt ./...
	goimports -w .

# Development helpers
dev-server: build
	./$(BUILD_DIR)/$(BINARY_NAME) serve --config server.yaml

dev-peer: build
	sudo ./$(BUILD_DIR)/$(BINARY_NAME) join --config peer.yaml

# Generate SSH keys for testing
gen-keys:
	@mkdir -p ~/.tunnelmesh
	@ssh-keygen -t ed25519 -f ~/.tunnelmesh/id_ed25519 -N "" -C "tunnelmesh"
	@echo "Keys generated in ~/.tunnelmesh/"

# Build release binaries for all platforms
release-all:
	@mkdir -p $(BUILD_DIR)/release
	@for platform in $(PLATFORMS); do \
		GOOS=$${platform%/*} GOARCH=$${platform#*/} \
		$(GO) build $(LDFLAGS) -o $(BUILD_DIR)/release/$(BINARY_NAME)-$${platform%/*}-$${platform#*/}$$([ "$${platform%/*}" = "windows" ] && echo ".exe") \
		./cmd/tunnelmesh; \
		echo "Built: $(BINARY_NAME)-$${platform%/*}-$${platform#*/}"; \
	done
	@echo "Release binaries in $(BUILD_DIR)/release/"

# Build release for current platform
release: build
	@mkdir -p $(BUILD_DIR)/release
	@cp $(BUILD_DIR)/$(BINARY_NAME) $(BUILD_DIR)/release/$(BINARY_NAME)-$(VERSION)
	@echo "Release binary: $(BUILD_DIR)/release/$(BINARY_NAME)-$(VERSION)"

# Tag defaults to commit ID, or override with TAG=v1.0.0
TAG ?= $(COMMIT)

# Create and push a release tag
push-release:
	@echo "Creating tag $(TAG)..."
	git tag -a $(TAG) -m "Release $(TAG)"
	git push origin $(TAG)
	@echo "Tag $(TAG) pushed"

# Create GitHub release (requires gh CLI)
github-release: release-all
	@if ! command -v gh &> /dev/null; then \
		echo "GitHub CLI (gh) not installed. Install with: brew install gh"; \
		exit 1; \
	fi
	gh release create $(TAG) $(BUILD_DIR)/release/* --title "$(TAG)" --generate-notes

# Show version info
version:
	@echo "Version: $(VERSION)"
	@echo "Commit:  $(COMMIT)"
	@echo "Build:   $(BUILD_TIME)"

# Docker Compose targets
DOCKER_COMPOSE=docker compose -f docker/docker-compose.yml

docker-build:
	$(DOCKER_COMPOSE) build

docker-up: docker-build
	$(DOCKER_COMPOSE) up -d
	@echo "TunnelMesh Docker environment started"
	@echo "Use 'make docker-logs' to follow logs"

docker-down:
	$(DOCKER_COMPOSE) down -v

docker-logs:
	$(DOCKER_COMPOSE) logs -f

docker-clean: docker-down
	$(DOCKER_COMPOSE) down -v --rmi local
	@echo "Docker environment cleaned"

docker-test: docker-build
	@echo "Starting Docker test environment..."
	$(DOCKER_COMPOSE) up -d
	@echo "Waiting for mesh to stabilize (20s)..."
	@sleep 20
	@echo "\n=== Mesh Status ==="
	$(DOCKER_COMPOSE) logs --tail=50 client 2>&1 | grep -E "(SUCCESS|FAILED|Pinging)" || true
	@echo "\n=== Running containers ==="
	$(DOCKER_COMPOSE) ps

# Help
help:
	@echo "Available targets:"
	@echo "  build          - Build for current platform"
	@echo "  build-force    - Clean and rebuild"
	@echo "  test           - Run tests with race detector"
	@echo "  test-verbose   - Run tests with verbose output"
	@echo "  test-coverage  - Run tests and generate coverage report"
	@echo "  clean          - Remove build artifacts"
	@echo "  install        - Install to /usr/local/bin"
	@echo "  lint           - Run golangci-lint"
	@echo "  fmt            - Format code"
	@echo "  release        - Build release binary for current platform"
	@echo "  release-all    - Build release binaries for all platforms"
	@echo "  push-release   - Create and push git tag (default: commit ID, or TAG=v1.0.0)"
	@echo "  github-release - Create GitHub release (default: commit ID, or TAG=v1.0.0)"
	@echo "  dev-server     - Build and run coordination server"
	@echo "  dev-peer       - Build and run peer (with sudo)"
	@echo "  gen-keys       - Generate SSH keys for testing"
	@echo "  version        - Show version info"
	@echo ""
	@echo "Docker targets:"
	@echo "  docker-build   - Build Docker images"
	@echo "  docker-up      - Start server + 5 client mesh environment"
	@echo "  docker-down    - Stop and remove containers"
	@echo "  docker-logs    - Follow container logs"
	@echo "  docker-clean   - Remove containers and images"
	@echo "  docker-test    - Build, start, and show mesh status"
