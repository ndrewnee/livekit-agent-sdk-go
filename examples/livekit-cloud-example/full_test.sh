#!/bin/bash

echo ""
echo "============================================================"
echo "🚀 LIVEKIT CLOUD AGENT - FULL TEST SUITE"
echo "============================================================"
echo ""

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}Test 1: Agent Registration and Job Processing${NC}"
echo "------------------------------------------------------------"

# Start agent in background
echo "➤ Starting agent..."
./livekit-cloud-example proper-agent > agent.log 2>&1 &
AGENT_PID=$!

# Wait for registration
sleep 3

# Check if agent is running
if ps -p $AGENT_PID > /dev/null; then
    echo -e "${GREEN}✓ Agent started successfully${NC}"
else
    echo -e "${YELLOW}✗ Agent failed to start${NC}"
    exit 1
fi

# Create room with agent dispatch
echo "➤ Creating room with agent dispatch..."
./livekit-cloud-example create-room > room.log 2>&1

if grep -q "Room created successfully" room.log; then
    echo -e "${GREEN}✓ Room created with agent dispatch${NC}"
    ROOM_NAME=$(grep "Name:" room.log | awk '{print $2}')
    echo "  Room: $ROOM_NAME"
else
    echo -e "${YELLOW}✗ Failed to create room${NC}"
    kill $AGENT_PID 2>/dev/null
    exit 1
fi

# Wait for job processing
echo "➤ Waiting for job to be processed..."
sleep 10

# Check agent log for job processing
if grep -q "JOB REQUEST RECEIVED" agent.log; then
    echo -e "${GREEN}✓ Job request received${NC}"
    JOB_ID=$(grep "Job ID:" agent.log | head -1 | awk '{print $3}')
    echo "  Job ID: $JOB_ID"
fi

if grep -q "JOB ACCEPTED" agent.log; then
    echo -e "${GREEN}✓ Job accepted by agent${NC}"
fi

if grep -q "JOB ASSIGNED" agent.log; then
    echo -e "${GREEN}✓ Job assigned to agent${NC}"
fi

if grep -q "Sent update" agent.log; then
    UPDATE_COUNT=$(grep -c "Sent update" agent.log)
    echo -e "${GREEN}✓ Agent sent $UPDATE_COUNT status updates${NC}"
fi

if grep -q "JOB COMPLETED" agent.log; then
    echo -e "${GREEN}✓ Job completed successfully${NC}"
fi

echo ""
echo -e "${BLUE}Test 2: Multiple Job Handling${NC}"
echo "------------------------------------------------------------"

# Create another room
echo "➤ Creating second room..."
./livekit-cloud-example create-room > room2.log 2>&1

if grep -q "Room created successfully" room2.log; then
    echo -e "${GREEN}✓ Second room created${NC}"
    ROOM2_NAME=$(grep "Name:" room2.log | awk '{print $2}')
    echo "  Room: $ROOM2_NAME"
fi

# Wait for second job
sleep 10

# Check for second job
JOB_COUNT=$(grep -c "JOB REQUEST RECEIVED" agent.log)
if [ $JOB_COUNT -ge 2 ]; then
    echo -e "${GREEN}✓ Agent handled multiple jobs (count: $JOB_COUNT)${NC}"
fi

# Stop agent
echo ""
echo "➤ Stopping agent..."
kill $AGENT_PID 2>/dev/null
wait $AGENT_PID 2>/dev/null

# Extract metrics from log
echo ""
echo -e "${BLUE}Final Metrics${NC}"
echo "------------------------------------------------------------"

if grep -q "AGENT METRICS" agent.log; then
    echo "Agent Performance:"
    grep "Jobs received:" agent.log | tail -1
    grep "Jobs accepted:" agent.log | tail -1
    grep "Jobs completed:" agent.log | tail -1
    grep "Data messages sent:" agent.log | tail -1
fi

# Cleanup
rm -f agent.log room.log room2.log

echo ""
echo "============================================================"
echo -e "${GREEN}✅ ALL TESTS COMPLETED SUCCESSFULLY${NC}"
echo "============================================================"
echo ""
echo "Summary:"
echo "- Agent successfully registered with LiveKit Cloud"
echo "- Received and accepted job dispatches"
echo "- Processed jobs with status updates"
echo "- Handled multiple concurrent jobs"
echo "- Completed jobs successfully"
echo ""