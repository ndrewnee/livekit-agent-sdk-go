#!/bin/bash

# Test script to verify all examples can build

set -e

echo "Testing LiveKit Agent SDK Go Examples"
echo "===================================="
echo ""

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m' # No Color

# Test function
test_example() {
    local example=$1
    echo -n "Testing $example... "
    
    cd "$example"
    
    # Download dependencies
    if go mod download > /dev/null 2>&1; then
        # Try to build
        if go build . > /dev/null 2>&1; then
            echo -e "${GREEN}✓ SUCCESS${NC}"
            cd ..
            return 0
        else
            echo -e "${RED}✗ BUILD FAILED${NC}"
            cd ..
            return 1
        fi
    else
        echo -e "${RED}✗ DEPENDENCY DOWNLOAD FAILED${NC}"
        cd ..
        return 1
    fi
}

# Track results
total=0
passed=0

# Test each example
for example in simple-room-agent participant-monitoring-agent media-publisher-agent; do
    total=$((total + 1))
    if test_example "$example"; then
        passed=$((passed + 1))
    fi
done

echo ""
echo "Results: $passed/$total examples built successfully"

if [ "$passed" -eq "$total" ]; then
    echo -e "${GREEN}All examples built successfully!${NC}"
    exit 0
else
    echo -e "${RED}Some examples failed to build${NC}"
    exit 1
fi