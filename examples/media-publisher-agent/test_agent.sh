#!/bin/bash

# Test script for media publisher agent

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Check if required environment variables are set
if [ -z "$LIVEKIT_API_KEY" ] || [ -z "$LIVEKIT_API_SECRET" ]; then
    echo -e "${RED}Error: LIVEKIT_API_KEY and LIVEKIT_API_SECRET must be set${NC}"
    exit 1
fi

LIVEKIT_URL=${LIVEKIT_URL:-"ws://localhost:7880"}
ROOM_NAME="media-publisher-test-$(date +%s)"

echo -e "${BLUE}Media Publisher Agent Test Script${NC}"
echo -e "${BLUE}================================${NC}"
echo -e "LiveKit URL: $LIVEKIT_URL"
echo -e "Room Name: $ROOM_NAME"
echo ""

# Function to create a room
create_room() {
    echo -e "${YELLOW}Creating room: $ROOM_NAME${NC}"
    
    # Using livekit-cli if available
    if command -v livekit-cli &> /dev/null; then
        livekit-cli create-room --name "$ROOM_NAME" --empty-timeout 300
    else
        echo -e "${YELLOW}livekit-cli not found, assuming room will be auto-created${NC}"
    fi
}

# Function to dispatch a job
dispatch_job() {
    local job_type=$1
    local metadata=$2
    local description=$3
    
    echo -e "\n${GREEN}Test: $description${NC}"
    echo -e "Dispatching $job_type job with metadata:"
    echo "$metadata" | jq '.' 2>/dev/null || echo "$metadata"
    
    # Create job request
    local job_request=$(cat <<EOF
{
    "type": "JT_PUBLISHER",
    "room": {
        "name": "$ROOM_NAME"
    },
    "metadata": $(echo "$metadata" | jq -c '.')
}
EOF
)
    
    # TODO: Use actual job dispatch API or livekit-cli when available
    echo -e "${YELLOW}Job dispatched (implementation needed)${NC}"
    
    # Wait a bit between tests
    sleep 5
}

# Create room
create_room

# Test 1: Audio tone generation
echo -e "\n${BLUE}Test 1: Audio Tone Generation${NC}"
dispatch_job "publisher" '{
    "mode": "audio_tone",
    "tone_frequency": 440.0,
    "volume": 0.8
}' "Generate 440Hz tone (A4 note)"

# Test 2: Video pattern generation
echo -e "\n${BLUE}Test 2: Video Pattern Generation${NC}"
dispatch_job "publisher" '{
    "mode": "video_pattern",
    "video_type": "moving_circle"
}' "Generate moving circle video pattern"

# Test 3: Both audio and video
echo -e "\n${BLUE}Test 3: Audio and Video Together${NC}"
dispatch_job "publisher" '{
    "mode": "both",
    "tone_frequency": 880.0,
    "video_type": "color_bars",
    "volume": 0.7
}' "Generate 880Hz tone with color bars"

# Test 4: Interactive mode
echo -e "\n${BLUE}Test 4: Interactive Mode${NC}"
dispatch_job "publisher" '{
    "mode": "interactive",
    "welcome_message": "Hello! I am an interactive media publisher. Send me commands!",
    "tone_frequency": 440.0,
    "volume": 1.0
}' "Interactive mode with welcome message"

echo -e "\n${GREEN}Interactive Mode Commands:${NC}"
echo "You can send these data messages to control the publisher:"
echo ""
echo "Set tone frequency:"
echo '{"type": "set_tone_frequency", "frequency": 880.0}'
echo ""
echo "Set volume:"
echo '{"type": "set_volume", "volume": 0.5}'
echo ""
echo "Change video pattern:"
echo '{"type": "change_pattern", "pattern": "checkerboard"}'
echo ""
echo "Simple commands:"
echo '{"type": "command", "command": "volume_up"}'
echo '{"type": "command", "command": "volume_down"}'
echo '{"type": "command", "command": "next_pattern"}'
echo '{"type": "command", "command": "hello"}'
echo '{"type": "command", "command": "status"}'

echo -e "\n${BLUE}Test Summary${NC}"
echo -e "${BLUE}============${NC}"
echo "1. Check agent logs for successful track publishing"
echo "2. Join the room '$ROOM_NAME' to see/hear the media"
echo "3. Try sending data messages in interactive mode"
echo "4. Monitor agent logs for frame statistics"

echo -e "\n${YELLOW}Note: This script shows example job configurations.${NC}"
echo -e "${YELLOW}Actual job dispatch requires integration with LiveKit's job system.${NC}"

# Generate a simple HTML test page
cat > test_publisher.html <<EOF
<!DOCTYPE html>
<html>
<head>
    <title>Media Publisher Test</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 20px; }
        .section { margin: 20px 0; padding: 10px; border: 1px solid #ccc; }
        button { margin: 5px; padding: 5px 10px; }
        #log { background: #f0f0f0; padding: 10px; height: 200px; overflow-y: auto; }
    </style>
</head>
<body>
    <h1>Media Publisher Agent Test Page</h1>
    
    <div class="section">
        <h2>Room: $ROOM_NAME</h2>
        <p>Use this page to test interactive commands with the media publisher agent.</p>
    </div>
    
    <div class="section">
        <h3>Quick Commands</h3>
        <button onclick="sendCommand('volume_up')">Volume Up</button>
        <button onclick="sendCommand('volume_down')">Volume Down</button>
        <button onclick="sendCommand('next_pattern')">Next Pattern</button>
        <button onclick="sendCommand('hello')">Say Hello</button>
        <button onclick="sendCommand('status')">Get Status</button>
    </div>
    
    <div class="section">
        <h3>Tone Control</h3>
        <label>Frequency: <input type="number" id="frequency" value="440" min="100" max="2000"></label>
        <button onclick="setFrequency()">Set Frequency</button>
    </div>
    
    <div class="section">
        <h3>Volume Control</h3>
        <label>Volume: <input type="range" id="volume" min="0" max="100" value="50"></label>
        <span id="volumeLabel">50%</span>
        <button onclick="setVolume()">Set Volume</button>
    </div>
    
    <div class="section">
        <h3>Pattern Control</h3>
        <select id="pattern">
            <option value="color_bars">Color Bars</option>
            <option value="moving_circle">Moving Circle</option>
            <option value="checkerboard">Checkerboard</option>
            <option value="gradient">Gradient</option>
        </select>
        <button onclick="setPattern()">Change Pattern</button>
    </div>
    
    <div class="section">
        <h3>Log</h3>
        <div id="log"></div>
    </div>
    
    <script>
        // Update volume label
        document.getElementById('volume').oninput = function() {
            document.getElementById('volumeLabel').innerText = this.value + '%';
        };
        
        function log(message) {
            const logDiv = document.getElementById('log');
            const time = new Date().toLocaleTimeString();
            logDiv.innerHTML += time + ' - ' + message + '<br>';
            logDiv.scrollTop = logDiv.scrollHeight;
        }
        
        function sendCommand(command) {
            const data = {
                type: 'command',
                command: command
            };
            log('Sending command: ' + command);
            // TODO: Implement actual LiveKit data channel sending
            console.log('Would send:', data);
        }
        
        function setFrequency() {
            const freq = document.getElementById('frequency').value;
            const data = {
                type: 'set_tone_frequency',
                frequency: parseFloat(freq)
            };
            log('Setting frequency to ' + freq + ' Hz');
            // TODO: Implement actual LiveKit data channel sending
            console.log('Would send:', data);
        }
        
        function setVolume() {
            const vol = document.getElementById('volume').value / 100;
            const data = {
                type: 'set_volume',
                volume: vol
            };
            log('Setting volume to ' + (vol * 100) + '%');
            // TODO: Implement actual LiveKit data channel sending
            console.log('Would send:', data);
        }
        
        function setPattern() {
            const pattern = document.getElementById('pattern').value;
            const data = {
                type: 'change_pattern',
                pattern: pattern
            };
            log('Changing pattern to ' + pattern);
            // TODO: Implement actual LiveKit data channel sending
            console.log('Would send:', data);
        }
        
        log('Test page loaded - implement LiveKit SDK integration to send commands');
    </script>
</body>
</html>
EOF

echo -e "\n${GREEN}Test page generated: test_publisher.html${NC}"
echo "Open this file in a browser to test interactive commands (requires LiveKit SDK integration)"