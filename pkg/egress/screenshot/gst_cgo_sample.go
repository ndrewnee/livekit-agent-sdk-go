// +build cgo

package screenshot

// #cgo pkg-config: gstreamer-1.0 gstreamer-app-1.0
// #include <gst/gst.h>
// #include <gst/app/gstappsink.h>
// #include <stdlib.h>
// #include <string.h>
//
// typedef struct {
//     guint8 *data;
//     gsize size;
//     gboolean success;
// } BufferData;
//
// typedef struct {
//     guint8 *data;
//     gsize size;
//     gint64 pts;
//     gboolean success;
// } BufferDataWithPTS;
//
// BufferDataWithPTS extract_sample_buffer_with_pts(GstElement *appsink) {
//     BufferDataWithPTS result = {NULL, 0, 0, FALSE};
//
//     GstSample *sample = gst_app_sink_pull_sample(GST_APP_SINK(appsink));
//     if (!sample) {
//         return result;
//     }
//
//     GstBuffer *buffer = gst_sample_get_buffer(sample);
//     if (!buffer) {
//         gst_sample_unref(sample);
//         return result;
//     }
//
//     // Extract REAL PTS from buffer
//     result.pts = GST_BUFFER_PTS(buffer);
//
//     GstMapInfo map;
//     if (gst_buffer_map(buffer, &map, GST_MAP_READ)) {
//         // Allocate memory and copy data
//         result.data = (guint8*)malloc(map.size);
//         if (result.data) {
//             memcpy(result.data, map.data, map.size);
//             result.size = map.size;
//             result.success = TRUE;
//         }
//         gst_buffer_unmap(buffer, &map);
//     }
//
//     gst_sample_unref(sample);
//     return result;
// }
//
// // Keep old function for backward compatibility
// BufferData extract_sample_buffer(GstElement *appsink) {
//     BufferDataWithPTS full = extract_sample_buffer_with_pts(appsink);
//     BufferData result = {full.data, full.size, full.success};
//     return result;
// }
//
// void free_buffer_data(guint8 *data) {
//     if (data) {
//         free(data);
//     }
// }
//
// // Helper to set appsink callbacks
// void setup_appsink_callbacks(GstElement *appsink);
//
import "C"
import (
	"fmt"
	"unsafe"

	"github.com/go-gst/go-gst/gst"
)

// SampleExtractor provides CGo bindings for extracting GStreamer samples
type SampleExtractor struct {
	appsink *gst.Element
}

// NewSampleExtractor creates a new sample extractor
func NewSampleExtractor(appsink *gst.Element) *SampleExtractor {
	return &SampleExtractor{
		appsink: appsink,
	}
}

// ExtractBuffer extracts buffer data from an appsink
func (se *SampleExtractor) ExtractBuffer() ([]byte, error) {
	if se.appsink == nil {
		return nil, fmt.Errorf("appsink is nil")
	}

	// Get the C pointer to the appsink
	cAppsink := (*C.GstElement)(unsafe.Pointer(se.appsink.Instance()))

	// Extract the buffer data
	bufferData := C.extract_sample_buffer(cAppsink)

	if bufferData.success == 0 {
		return nil, fmt.Errorf("failed to extract buffer from sample")
	}

	// Convert to Go byte slice
	if bufferData.data == nil || bufferData.size == 0 {
		return nil, fmt.Errorf("empty buffer extracted")
	}

	// Copy the data to a Go slice
	data := C.GoBytes(unsafe.Pointer(bufferData.data), C.int(bufferData.size))

	// Free the C memory
	C.free_buffer_data(bufferData.data)

	return data, nil
}

// ExtractBufferWithMetadata extracts buffer and REAL timestamp from appsink
func (se *SampleExtractor) ExtractBufferWithMetadata() ([]byte, int64, error) {
	if se.appsink == nil {
		return nil, 0, fmt.Errorf("appsink is nil")
	}

	// Get the C pointer to the appsink
	cAppsink := (*C.GstElement)(unsafe.Pointer(se.appsink.Instance()))

	// Extract buffer data with REAL PTS
	bufferData := C.extract_sample_buffer_with_pts(cAppsink)

	if bufferData.success == 0 {
		return nil, 0, fmt.Errorf("failed to extract buffer from sample")
	}

	// Convert to Go byte slice
	if bufferData.data == nil || bufferData.size == 0 {
		return nil, 0, fmt.Errorf("empty buffer extracted")
	}

	// Copy the data to a Go slice
	data := C.GoBytes(unsafe.Pointer(bufferData.data), C.int(bufferData.size))

	// Get REAL timestamp from GStreamer buffer
	timestamp := int64(bufferData.pts)

	// Free the C memory
	C.free_buffer_data(bufferData.data)

	return data, timestamp, nil
}