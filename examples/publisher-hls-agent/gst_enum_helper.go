package main

/*
#cgo pkg-config: gstreamer-1.0
#include <gst/gst.h>
#include <stdlib.h>

// setElementEnumProperty sets an enum property on a GStreamer element using its integer value.
// This is necessary because go-gst's SetProperty() doesn't handle enum types properly.
void setElementEnumProperty(GstElement *element, const char *propertyName, int value) {
    g_object_set(G_OBJECT(element), propertyName, value, NULL);
}
*/
import "C"
import (
	"unsafe"

	"github.com/go-gst/go-gst/gst"
)

// setEnumProperty sets an enum property on a GStreamer element.
// This works around the limitation in go-gst's SetProperty which rejects enum values.
//
// Example usage:
//
//	dashSink, _ := gst.NewElement("dashsink")
//	setEnumProperty(dashSink, "muxer", 2) // Set to dashmp4mux (enum value 2)
func setEnumProperty(element *gst.Element, propertyName string, value int) {
	cPropertyName := C.CString(propertyName)
	defer C.free(unsafe.Pointer(cPropertyName))

	// Use unsafe.Pointer to bridge between different CGo type namespaces
	C.setElementEnumProperty(
		(*C.GstElement)(unsafe.Pointer(element.Instance())),
		cPropertyName,
		C.int(value),
	)
}
