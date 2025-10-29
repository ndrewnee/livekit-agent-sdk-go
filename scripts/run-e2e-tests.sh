#!/bin/bash

# E2E Test Runner for LiveKit Egress
# This script sets up the environment and runs end-to-end tests

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
LIVEKIT_URL="${LIVEKIT_URL:-ws://localhost:7880}"
LIVEKIT_API_KEY="${LIVEKIT_API_KEY:-devkey}"
LIVEKIT_API_SECRET="${LIVEKIT_API_SECRET:-secret}"

MINIO_ENDPOINT="${MINIO_ENDPOINT:-localhost:9000}"
MINIO_ACCESS_KEY="${MINIO_ACCESS_KEY:-minioadmin}"
MINIO_SECRET_KEY="${MINIO_SECRET_KEY:-minioadmin}"
MINIO_BUCKET="${MINIO_BUCKET:-egress-test}"

# Functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

check_command() {
    if ! command -v $1 &> /dev/null; then
        log_error "$1 is not installed"
        return 1
    fi
    log_success "$1 is installed"
    return 0
}

check_service() {
    local url=$1
    local name=$2

    if curl -s -f -o /dev/null "$url"; then
        log_success "$name is running at $url"
        return 0
    else
        log_error "$name is not accessible at $url"
        return 1
    fi
}

# Header
echo "=========================================="
echo "  LiveKit Egress E2E Test Runner"
echo "=========================================="
echo ""

# Check prerequisites
log_info "Checking prerequisites..."

MISSING_DEPS=0

check_command go || MISSING_DEPS=1
check_command curl || MISSING_DEPS=1
check_command ffmpeg || log_warning "ffmpeg not found (optional for debugging)"
check_command gst-inspect-1.0 || MISSING_DEPS=1

if [ $MISSING_DEPS -eq 1 ]; then
    log_error "Missing required dependencies. Please install them first."
    echo ""
    echo "Installation instructions:"
    echo "  - Go: https://go.dev/doc/install"
    echo "  - GStreamer: https://gstreamer.freedesktop.org/documentation/installing/"
    echo ""
    exit 1
fi

echo ""
log_info "Checking services..."

# Check LiveKit
if ! check_service "http://localhost:7880/healthz" "LiveKit"; then
    log_warning "LiveKit server not running. Start it with:"
    echo "    livekit-server --dev --bind 0.0.0.0"
    echo ""
    read -p "Continue anyway? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

# Check MinIO
if ! check_service "http://localhost:9000/minio/health/live" "MinIO"; then
    log_warning "MinIO server not running. Start it with:"
    echo "    docker run -p 9000:9000 -p 9001:9001 \\"
    echo "      -e MINIO_ROOT_USER=minioadmin \\"
    echo "      -e MINIO_ROOT_PASSWORD=minioadmin \\"
    echo "      quay.io/minio/minio server /data --console-address \":9001\""
    echo ""
    read -p "Continue anyway? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

# Show configuration
echo ""
log_info "Configuration:"
echo "  LiveKit URL: $LIVEKIT_URL"
echo "  LiveKit API Key: $LIVEKIT_API_KEY"
echo "  MinIO Endpoint: $MINIO_ENDPOINT"
echo "  MinIO Bucket: $MINIO_BUCKET"
echo ""

# Parse command line arguments
TEST_PATTERN="TestE2E"
VERBOSE=""
TIMEOUT="10m"
SHORT=""

while [[ $# -gt 0 ]]; do
    case $1 in
        -v|--verbose)
            VERBOSE="-v"
            shift
            ;;
        -t|--test)
            TEST_PATTERN="$2"
            shift 2
            ;;
        --timeout)
            TIMEOUT="$2"
            shift 2
            ;;
        --short)
            SHORT="-short"
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [options]"
            echo ""
            echo "Options:"
            echo "  -v, --verbose      Verbose output"
            echo "  -t, --test NAME    Run specific test pattern (default: TestE2E)"
            echo "  --timeout DURATION Test timeout (default: 10m)"
            echo "  --short            Skip long-running tests"
            echo "  -h, --help         Show this help"
            echo ""
            echo "Examples:"
            echo "  $0                                    # Run all E2E tests"
            echo "  $0 -v                                 # Run with verbose output"
            echo "  $0 -t TestE2ECompleteEgressWorkflow  # Run specific test"
            echo "  $0 --short                            # Skip long tests"
            echo ""
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            echo "Use -h or --help for usage information"
            exit 1
            ;;
    esac
done

# Export environment variables
export LIVEKIT_URL
export LIVEKIT_API_KEY
export LIVEKIT_API_SECRET
export MINIO_ENDPOINT
export MINIO_ACCESS_KEY
export MINIO_SECRET_KEY
export MINIO_BUCKET

# Run tests
log_info "Running E2E tests with pattern: $TEST_PATTERN"
echo ""

cd "$(dirname "$0")/.." || exit 1

if go test -tags=e2e $VERBOSE $SHORT -timeout $TIMEOUT ./pkg/egress -run "$TEST_PATTERN"; then
    echo ""
    log_success "All tests passed!"
    echo ""
    log_info "View results:"
    echo "  - MinIO Console: http://localhost:9001 (minioadmin/minioadmin)"
    echo "  - HLS Player: open tools/hls-player/player.html"
    echo "  - MinIO Browser: http://localhost:9000/egress-test/"
    echo ""
    exit 0
else
    echo ""
    log_error "Some tests failed!"
    echo ""
    log_info "Troubleshooting:"
    echo "  1. Check LiveKit logs: docker logs <container-id>"
    echo "  2. Check MinIO console: http://localhost:9001"
    echo "  3. Enable GST debug: GST_DEBUG=3 $0"
    echo "  4. Check test output above for specific errors"
    echo ""
    exit 1
fi