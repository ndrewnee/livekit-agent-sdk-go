.PHONY: build test clean run

build:
	@echo "Building save-to-hls-gstreamer (this may take a while due to CGO)..."
	go build -o save-to-hls-gstreamer .

test:
	@echo "Running tests..."
	go test -tags=e2e -v -run TestHLSRecorderGStreamer -timeout=5m

clean:
	@echo "Cleaning..."
	rm -rf save-to-hls-gstreamer hls-gst-output /tmp/test_video.h264 /tmp/test_audio.ogg

run: build
	@echo "Starting HLS recorder..."
	./save-to-hls-gstreamer

check-deps:
	@echo "Checking GStreamer installation..."
	@pkg-config --modversion gstreamer-1.0 || (echo "ERROR: GStreamer not installed!" && exit 1)
	@echo "✓ GStreamer installed"
	@echo "Checking required plugins..."
	@gst-inspect-1.0 rtpjitterbuffer > /dev/null 2>&1 || (echo "ERROR: rtpjitterbuffer not found!" && exit 1)
	@gst-inspect-1.0 rtph264depay > /dev/null 2>&1 || (echo "ERROR: rtph264depay not found!" && exit 1)
	@gst-inspect-1.0 rtpopusdepay > /dev/null 2>&1 || (echo "ERROR: rtpopusdepay not found!" && exit 1)
	@gst-inspect-1.0 avenc_aac > /dev/null 2>&1 || (echo "ERROR: avenc_aac not found (install gst-plugins-ugly)!" && exit 1)
	@gst-inspect-1.0 hlssink2 > /dev/null 2>&1 || (echo "ERROR: hlssink2 not found!" && exit 1)
	@echo "✓ All required GStreamer plugins found"
