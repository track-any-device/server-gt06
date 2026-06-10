// Package metrics exposes Prometheus instrumentation for the GT06 server.
//
// Scrape endpoint: GET /metrics  (HTTP on GT06_HTTP_ADDR, default :9091)
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics groups all Prometheus instruments.
type Metrics struct {
	ConnectionsTotal  prometheus.Counter
	ConnectionsActive prometheus.Gauge

	FramesReceived *prometheus.CounterVec

	LoginSuccess prometheus.Counter
	LoginFailure prometheus.Counter

	Heartbeats      prometheus.Counter
	LocationReports prometheus.Counter
	SOSAlarms       prometheus.Counter
	OverspeedAlarms prometheus.Counter
	DecodeErrors    prometheus.Counter
	UnknownMessages prometheus.Counter

	StreamPublishDuration prometheus.Histogram
}

func New() *Metrics {
	return &Metrics{
		ConnectionsTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "gt06_connections_total",
			Help: "Total TCP connections accepted since startup.",
		}),
		ConnectionsActive: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "gt06_connections_active",
			Help: "Currently connected GT06 devices.",
		}),
		FramesReceived: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "gt06_frames_received_total",
			Help: "Total GT06 frames received, by protocol code name.",
		}, []string{"protocol"}),
		LoginSuccess: promauto.NewCounter(prometheus.CounterOpts{
			Name: "gt06_login_success_total",
			Help: "Approved 0x01 login frames.",
		}),
		LoginFailure: promauto.NewCounter(prometheus.CounterOpts{
			Name: "gt06_login_failure_total",
			Help: "Rejected 0x01 login frames (not approved or not found).",
		}),
		Heartbeats: promauto.NewCounter(prometheus.CounterOpts{
			Name: "gt06_heartbeats_total",
			Help: "Total 0x13 heartbeat frames received.",
		}),
		LocationReports: promauto.NewCounter(prometheus.CounterOpts{
			Name: "gt06_location_reports_total",
			Help: "Total location frames published to Redis Stream.",
		}),
		SOSAlarms: promauto.NewCounter(prometheus.CounterOpts{
			Name: "gt06_sos_alarms_total",
			Help: "SOS alarm events received.",
		}),
		OverspeedAlarms: promauto.NewCounter(prometheus.CounterOpts{
			Name: "gt06_overspeed_alarms_total",
			Help: "Overspeed alarm events received.",
		}),
		DecodeErrors: promauto.NewCounter(prometheus.CounterOpts{
			Name: "gt06_decode_errors_total",
			Help: "Frames dropped due to CRC failure or parse error.",
		}),
		UnknownMessages: promauto.NewCounter(prometheus.CounterOpts{
			Name: "gt06_unknown_messages_total",
			Help: "Frames with unrecognised protocol codes.",
		}),
		StreamPublishDuration: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "gt06_stream_publish_seconds",
			Help:    "Redis Stream XADD latency in seconds.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0},
		}),
	}
}
