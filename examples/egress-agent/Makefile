# Egress Agent Makefile

.PHONY: all build test clean run install-deps verify help

# Variables
BINARY_NAME=egress-agent
GO=go
GOFLAGS=-v
LDFLAGS=-ldflags "-s -w"

# Default target
all: build

# Help target
help:
	@echo "Egress Agent Makefile"
	@echo ""
	@echo "Available targets:"
	@echo "  make build         - Build the egress agent binary"
	@echo "  make test          - Run all tests"
	@echo "  make clean         - Clean build artifacts"
	@echo "  make run           - Run the egress agent"
	@echo "  make install-deps  - Install dependencies (GStreamer, etc)"
	@echo "  make verify        - Verify all dependencies are installed"
	@echo "  make start-livekit - Start local LiveKit server"
	@echo "  make start-minio   - Start local MinIO server"
	@echo "  make generate-test - Generate test media files"
	@echo "  make help          - Show this help message"

# Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_NAME) .
	@echo "Build complete: ./$(BINARY_NAME)"

# Run tests
test:
	@echo "Running tests..."
	$(GO) test -v -race -cover ../../pkg/egress/...
	@echo "Tests complete"

# Clean build artifacts
clean:
	@echo "Cleaning..."
	@rm -f $(BINARY_NAME)
	@rm -rf test-data/sample-hls/*.ts
	@rm -rf /tmp/recordings/*
	@echo "Clean complete"

# Run the agent
run: build
	@echo "Running $(BINARY_NAME)..."
	./$(BINARY_NAME) -config config.yaml

# Install dependencies
install-deps:
	@echo "Installing dependencies..."
	@./install-gstreamer.sh
	@echo ""
	@echo "Checking for LiveKit server..."
	@which livekit-server > /dev/null || (echo "Installing LiveKit server..." && brew install livekit)
	@echo ""
	@echo "Checking for MinIO..."
	@which minio > /dev/null || (echo "Installing MinIO..." && brew install minio)
	@echo ""
	@echo "Dependencies installed"

# Verify dependencies
verify:
	@echo "Verifying dependencies..."
	@echo ""
	@echo "GStreamer version:"
	@gst-launch-1.0 --version | head -n 2
	@echo ""
	@echo "Verifying GStreamer plugins..."
	@./install-gstreamer.sh | grep -A 100 "Verifying required"
	@echo ""
	@echo "LiveKit server:"
	@which livekit-server && livekit-server --version || echo "Not installed"
	@echo ""
	@echo "MinIO:"
	@which minio && minio --version || echo "Not installed"
	@echo ""
	@echo "Go version:"
	@$(GO) version
	@echo ""
	@echo "Verification complete"

# Start LiveKit server
start-livekit:
	@echo "Starting LiveKit server..."
	./start-livekit.sh

# Start MinIO server
start-minio:
	@echo "Starting MinIO server..."
	./start-minio.sh

# Generate test media files
generate-test:
	@echo "Generating test media files..."
	./generate-test-media.sh

# Development targets
.PHONY: dev-setup dev-test

# Setup development environment
dev-setup: install-deps generate-test
	@echo "Development environment setup complete"

# Run tests with coverage report
dev-test:
	@echo "Running tests with coverage..."
	$(GO) test -v -race -coverprofile=coverage.out ../../pkg/egress/...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Docker targets (for future use)
.PHONY: docker-build docker-run

docker-build:
	@echo "Docker support not yet implemented"

docker-run:
	@echo "Docker support not yet implemented"