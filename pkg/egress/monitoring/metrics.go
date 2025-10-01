package monitoring

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus metrics for the egress agent
type Metrics struct {
	// Pipeline metrics
	PipelinesActive      prometheus.Gauge
	PipelinesTotal       prometheus.Counter
	PipelineErrors       prometheus.Counter
	PipelineDuration     prometheus.Histogram

	// Packet metrics
	PacketsReceived      *prometheus.CounterVec
	PacketsDropped       *prometheus.CounterVec
	PacketLatency        *prometheus.HistogramVec

	// Segment metrics
	SegmentsWritten      prometheus.Counter
	SegmentDuration      prometheus.Histogram
	SegmentSize          prometheus.Histogram

	// Upload metrics
	UploadsTotal         *prometheus.CounterVec
	UploadsInProgress    prometheus.Gauge
	UploadDuration       *prometheus.HistogramVec
	UploadErrors         *prometheus.CounterVec

	// Resource metrics
	CPUUsagePercent      prometheus.Gauge
	MemoryUsageMB        prometheus.Gauge
	GoroutineCount       prometheus.Gauge

	// Codec metrics
	CodecChangesRejected prometheus.Counter
	CodecsInUse          *prometheus.GaugeVec

	// Screenshot metrics
	ScreenshotsCaptured  prometheus.Counter
	ScreenshotErrors     prometheus.Counter
}

// NewMetrics creates and registers all Prometheus metrics
func NewMetrics(namespace string) *Metrics {
	if namespace == "" {
		namespace = "livekit_egress"
	}

	return &Metrics{
		// Pipeline metrics
		PipelinesActive: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "pipelines_active",
			Help:      "Number of active egress pipelines",
		}),
		PipelinesTotal: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "pipelines_total",
			Help:      "Total number of pipelines created",
		}),
		PipelineErrors: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "pipeline_errors_total",
			Help:      "Total number of pipeline errors",
		}),
		PipelineDuration: promauto.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "pipeline_duration_seconds",
			Help:      "Duration of pipeline sessions in seconds",
			Buckets:   prometheus.ExponentialBuckets(10, 2, 10), // 10s to ~5000s
		}),

		// Packet metrics
		PacketsReceived: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "packets_received_total",
				Help:      "Total number of RTP packets received",
			},
			[]string{"type"}, // "audio" or "video"
		),
		PacketsDropped: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "packets_dropped_total",
				Help:      "Total number of RTP packets dropped",
			},
			[]string{"type", "reason"},
		),
		PacketLatency: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "packet_latency_ms",
				Help:      "Packet processing latency in milliseconds",
				Buckets:   prometheus.LinearBuckets(0, 5, 20), // 0-100ms in 5ms steps
			},
			[]string{"type"},
		),

		// Segment metrics
		SegmentsWritten: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "segments_written_total",
			Help:      "Total number of HLS segments written",
		}),
		SegmentDuration: promauto.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "segment_duration_seconds",
			Help:      "Duration of HLS segments in seconds",
			Buckets:   prometheus.LinearBuckets(1, 1, 10), // 1-10 seconds
		}),
		SegmentSize: promauto.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "segment_size_bytes",
			Help:      "Size of HLS segments in bytes",
			Buckets:   prometheus.ExponentialBuckets(100000, 2, 10), // 100KB to ~100MB
		}),

		// Upload metrics
		UploadsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "uploads_total",
				Help:      "Total number of uploads to storage",
			},
			[]string{"type", "status"}, // type: segment/playlist/screenshot, status: success/failure
		),
		UploadsInProgress: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "uploads_in_progress",
			Help:      "Number of uploads currently in progress",
		}),
		UploadDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "upload_duration_seconds",
				Help:      "Duration of uploads in seconds",
				Buckets:   prometheus.ExponentialBuckets(0.1, 2, 10), // 100ms to ~100s
			},
			[]string{"type"},
		),
		UploadErrors: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "upload_errors_total",
				Help:      "Total number of upload errors",
			},
			[]string{"type", "error"},
		),

		// Resource metrics
		CPUUsagePercent: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "cpu_usage_percent",
			Help:      "CPU usage percentage",
		}),
		MemoryUsageMB: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "memory_usage_mb",
			Help:      "Memory usage in megabytes",
		}),
		GoroutineCount: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "goroutines",
			Help:      "Number of goroutines",
		}),

		// Codec metrics
		CodecChangesRejected: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "codec_changes_rejected_total",
			Help:      "Total number of codec changes rejected",
		}),
		CodecsInUse: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "codecs_in_use",
				Help:      "Codecs currently in use",
			},
			[]string{"type", "codec"},
		),

		// Screenshot metrics
		ScreenshotsCaptured: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "screenshots_captured_total",
			Help:      "Total number of screenshots captured",
		}),
		ScreenshotErrors: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "screenshot_errors_total",
			Help:      "Total number of screenshot errors",
		}),
	}
}

// RecordPacketReceived increments the packet received counter
func (m *Metrics) RecordPacketReceived(packetType string) {
	m.PacketsReceived.WithLabelValues(packetType).Inc()
}

// RecordPacketDropped increments the packet dropped counter
func (m *Metrics) RecordPacketDropped(packetType, reason string) {
	m.PacketsDropped.WithLabelValues(packetType, reason).Inc()
}

// RecordUpload records an upload attempt
func (m *Metrics) RecordUpload(uploadType, status string, durationSeconds float64) {
	m.UploadsTotal.WithLabelValues(uploadType, status).Inc()
	if status == "success" {
		m.UploadDuration.WithLabelValues(uploadType).Observe(durationSeconds)
	}
}

// RecordUploadError records an upload error
func (m *Metrics) RecordUploadError(uploadType, errorType string) {
	m.UploadErrors.WithLabelValues(uploadType, errorType).Inc()
}

// SetCodecInUse sets the codec gauge
func (m *Metrics) SetCodecInUse(codecType, codecName string, inUse bool) {
	value := 0.0
	if inUse {
		value = 1.0
	}
	m.CodecsInUse.WithLabelValues(codecType, codecName).Set(value)
}