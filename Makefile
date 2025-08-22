.PHONY: all test test-unit test-integration test-coverage lint fmt clean build example

# Default target
all: test build

# Run all tests
test: test-unit

# Run unit tests
test-unit:
	go test -v ./pkg/agent/...

# Run integration tests (requires LiveKit server)
test-integration:
	go test -v -tags=integration ./pkg/agent/...

# Run tests with coverage
test-coverage:
	go test -v -race -coverprofile=coverage.out -covermode=atomic ./pkg/agent/...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

# Run linter
lint:
	@if command -v golangci-lint > /dev/null; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed. Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
		exit 1; \
	fi

# Format code
fmt:
	go fmt ./...
	goimports -w .

# Clean build artifacts
clean:
	rm -f coverage.out coverage.html
	rm -rf bin/
	go clean -testcache

# Build the library
build:
	go build ./pkg/agent/...

# Build and run example
example:
	go build -o bin/transcription-agent ./examples/transcription-agent
	@echo "Built example agent: bin/transcription-agent"

# Install dependencies
deps:
	go mod download
	go mod tidy

# Run specific test
test-specific:
	@read -p "Enter test name pattern: " test_name; \
	go test -v -run $$test_name ./pkg/agent/...

# Benchmark tests
bench:
	go test -bench=. -benchmem ./pkg/agent/...

# Check for vulnerabilities
vuln:
	@if command -v govulncheck > /dev/null; then \
		govulncheck ./...; \
	else \
		echo "govulncheck not installed. Install with: go install golang.org/x/vuln/cmd/govulncheck@latest"; \
		exit 1; \
	fi

# Generate mocks (if needed in future)
generate:
	go generate ./...

# Run tests in verbose mode with race detection
test-race:
	go test -v -race ./pkg/agent/...

# Quick test (no race detection, parallel)
test-quick:
	go test -parallel 4 ./pkg/agent/...

# CI target - runs all checks
ci: deps lint test-race test-coverage

# Help target
help:
	@echo "Available targets:"
	@echo "  all              - Run tests and build"
	@echo "  test             - Run all unit tests"
	@echo "  test-unit        - Run unit tests"
	@echo "  test-integration - Run integration tests (requires LiveKit server)"
	@echo "  test-coverage    - Run tests with coverage report"
	@echo "  test-race        - Run tests with race detection"
	@echo "  test-quick       - Run tests quickly (parallel, no race detection)"
	@echo "  test-specific    - Run specific test by pattern"
	@echo "  lint             - Run linter"
	@echo "  fmt              - Format code"
	@echo "  clean            - Remove build artifacts"
	@echo "  build            - Build the library"
	@echo "  example          - Build example agent"
	@echo "  deps             - Download and tidy dependencies"
	@echo "  bench            - Run benchmarks"
	@echo "  vuln             - Check for vulnerabilities"
	@echo "  ci               - Run all CI checks"
	@echo "  help             - Show this help message"