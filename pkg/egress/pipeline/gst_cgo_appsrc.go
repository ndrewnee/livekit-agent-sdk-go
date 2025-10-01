package pipeline

/*
#cgo pkg-config: gstreamer-1.0 gstreamer-app-1.0
#include <gst/gst.h>
#include <gst/app/gstappsrc.h>
#include <string.h>

// Push buffer to appsrc
GstFlowReturn push_buffer_to_appsrc(GstElement *appsrc, void *data, int size, uint64_t pts) {
    if (!GST_IS_APP_SRC(appsrc)) {
        return GST_FLOW_ERROR;
    }

    GstBuffer *buffer = gst_buffer_new_allocate(NULL, size, NULL);
    if (!buffer) {
        return GST_FLOW_ERROR;
    }

    // Fill buffer with data
    GstMapInfo map;
    if (gst_buffer_map(buffer, &map, GST_MAP_WRITE)) {
        memcpy(map.data, data, size);
        gst_buffer_unmap(buffer, &map);
    }

    // Set presentation timestamp
    GST_BUFFER_PTS(buffer) = pts;

    // Push buffer to appsrc
    GstFlowReturn ret = gst_app_src_push_buffer(GST_APP_SRC(appsrc), buffer);
    return ret;
}

// Set appsrc to live mode
void set_appsrc_live(GstElement *appsrc) {
    if (GST_IS_APP_SRC(appsrc)) {
        g_object_set(G_OBJECT(appsrc),
            "is-live", TRUE,
            "format", GST_FORMAT_TIME,
            "do-timestamp", FALSE,
            NULL);
    }
}

// Configure appsrc caps
void set_appsrc_caps(GstElement *appsrc, const char *caps_string) {
    if (!GST_IS_APP_SRC(appsrc)) {
        return;
    }

    GstCaps *caps = gst_caps_from_string(caps_string);
    if (caps) {
        gst_app_src_set_caps(GST_APP_SRC(appsrc), caps);
        gst_caps_unref(caps);
    }
}
*/
import "C"
import (
	"fmt"
	"unsafe"

	"github.com/go-gst/go-gst/gst"
)

// AppsrcHelper provides real CGo bindings for appsrc functionality
type AppsrcHelper struct {
	videoSrc *gst.Element
	audioSrc *gst.Element
}

// NewAppsrcHelper creates a new appsrc helper with CGo bindings
func NewAppsrcHelper(videoSrc, audioSrc *gst.Element) *AppsrcHelper {
	if videoSrc != nil {
		// Configure video appsrc for live RTP
		C.set_appsrc_live((*C.GstElement)(unsafe.Pointer(videoSrc.Instance())))
		// Include payload for proper RTP caps negotiation
		capsStr := C.CString("application/x-rtp,media=video,clock-rate=90000,encoding-name=H264,payload=96")
		defer C.free(unsafe.Pointer(capsStr))
		C.set_appsrc_caps((*C.GstElement)(unsafe.Pointer(videoSrc.Instance())), capsStr)
	}

	if audioSrc != nil {
		// Configure audio appsrc for live RTP
		C.set_appsrc_live((*C.GstElement)(unsafe.Pointer(audioSrc.Instance())))
		// Include payload for proper RTP caps negotiation
		capsStr := C.CString("application/x-rtp,media=audio,clock-rate=48000,encoding-name=OPUS,payload=111")
		defer C.free(unsafe.Pointer(capsStr))
		C.set_appsrc_caps((*C.GstElement)(unsafe.Pointer(audioSrc.Instance())), capsStr)
	}

	return &AppsrcHelper{
		videoSrc: videoSrc,
		audioSrc: audioSrc,
	}
}

// PushVideoBuffer pushes a video RTP packet to the video appsrc
func (h *AppsrcHelper) PushVideoBuffer(data []byte, pts uint64) error {
	if h.videoSrc == nil {
		return fmt.Errorf("video appsrc is nil")
	}

	if len(data) == 0 {
		return fmt.Errorf("empty data buffer")
	}

	// Push buffer using CGo
	ret := C.push_buffer_to_appsrc(
		(*C.GstElement)(unsafe.Pointer(h.videoSrc.Instance())),
		unsafe.Pointer(&data[0]),
		C.int(len(data)),
		C.uint64_t(pts),
	)

	if ret != C.GST_FLOW_OK {
		return fmt.Errorf("failed to push video buffer: flow return %d", ret)
	}

	return nil
}

// PushAudioBuffer pushes an audio RTP packet to the audio appsrc
func (h *AppsrcHelper) PushAudioBuffer(data []byte, pts uint64) error {
	if h.audioSrc == nil {
		return fmt.Errorf("audio appsrc is nil")
	}

	if len(data) == 0 {
		return fmt.Errorf("empty data buffer")
	}

	// Push buffer using CGo
	ret := C.push_buffer_to_appsrc(
		(*C.GstElement)(unsafe.Pointer(h.audioSrc.Instance())),
		unsafe.Pointer(&data[0]),
		C.int(len(data)),
		C.uint64_t(pts),
	)

	if ret != C.GST_FLOW_OK {
		return fmt.Errorf("failed to push audio buffer: flow return %d", ret)
	}

	return nil
}

// SendEOS sends end-of-stream signal to appsrc
func (h *AppsrcHelper) SendEOS() {
	if h.videoSrc != nil {
		h.videoSrc.Emit("end-of-stream")
	}
	if h.audioSrc != nil {
		h.audioSrc.Emit("end-of-stream")
	}
}