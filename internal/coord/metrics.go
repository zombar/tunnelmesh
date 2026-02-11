// Package coord provides the TunnelMesh coordinator server.
package coord

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// coordMetricsOnce ensures metrics are only initialized once.
var coordMetricsOnce sync.Once

// coordMetricsInstance is the singleton instance of coordinator metrics.
var coordMetricsInstance *CoordMetrics

// CoordMetrics holds all Prometheus metrics for the coordinator.
// These are registered with the default Prometheus registry so they're
// exposed alongside peer metrics when running in join_mesh mode.
type CoordMetrics struct {
	// Peer RTT metrics
	PeerRTTSeconds *prometheus.GaugeVec // tunnelmesh_coordinator_peer_rtt_seconds{peer}

	// Peer-to-peer latency
	PeerLatencySeconds *prometheus.GaugeVec // tunnelmesh_coordinator_peer_latency_seconds{source,target}

	// Connection gauges
	OnlinePeers prometheus.Gauge

	// Heartbeat stats
	TotalHeartbeats prometheus.Counter

	// Network stats persistence metrics
	networkStatsSaveTotal    prometheus.Counter
	networkStatsSaveErrors   *prometheus.CounterVec
	networkStatsSaveDuration prometheus.Histogram
	networkStatsLastSave     *prometheus.GaugeVec
	statsHistorySaveTotal    prometheus.Counter
	statsHistorySaveErrors   *prometheus.CounterVec
	statsHistorySaveDuration prometheus.Histogram
	statsCorruptionDetected  *prometheus.CounterVec
}

// InitCoordMetrics initializes all coordinator metrics.
// Metrics are only registered once; subsequent calls return the same instance.
// Pass a registry to register metrics with that registry (for exposure on /metrics endpoint).
// If nil, uses the default Prometheus registry.
func InitCoordMetrics(registry prometheus.Registerer) *CoordMetrics {
	coordMetricsOnce.Do(func() {
		if registry == nil {
			registry = prometheus.DefaultRegisterer
		}
		coordMetricsInstance = &CoordMetrics{
			PeerRTTSeconds: promauto.With(registry).NewGaugeVec(prometheus.GaugeOpts{
				Name: "tunnelmesh_coordinator_peer_rtt_seconds",
				Help: "RTT reported by peer to coordinator in seconds",
			}, []string{"peer"}),

			PeerLatencySeconds: promauto.With(registry).NewGaugeVec(prometheus.GaugeOpts{
				Name: "tunnelmesh_coordinator_udp_peer_latency_seconds",
				Help: "UDP tunnel RTT between peers in seconds (only available for direct UDP connections)",
			}, []string{"source", "target"}),

			OnlinePeers: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
				Name: "tunnelmesh_coordinator_online_peers",
				Help: "Number of currently online peers",
			}),

			TotalHeartbeats: promauto.With(registry).NewCounter(prometheus.CounterOpts{
				Name: "tunnelmesh_coordinator_heartbeats_total",
				Help: "Total heartbeats received by coordinator",
			}),

			// Network stats persistence metrics (per-peer granularity)
			networkStatsSaveTotal: promauto.With(registry).NewCounter(prometheus.CounterOpts{
				Name: "tunnelmesh_network_stats_save_total",
				Help: "Total successful network stats saves",
			}),
			networkStatsSaveErrors: promauto.With(registry).NewCounterVec(prometheus.CounterOpts{
				Name: "tunnelmesh_network_stats_save_errors_total",
				Help: "Total network stats save errors",
			}, []string{"peer", "error_type"}),
			networkStatsSaveDuration: promauto.With(registry).NewHistogram(prometheus.HistogramOpts{
				Name:    "tunnelmesh_network_stats_save_duration_seconds",
				Help:    "Network stats save latency in seconds",
				Buckets: prometheus.DefBuckets,
			}),
			networkStatsLastSave: promauto.With(registry).NewGaugeVec(prometheus.GaugeOpts{
				Name: "tunnelmesh_network_stats_last_save_timestamp",
				Help: "Unix timestamp of last network stats save per peer",
			}, []string{"peer"}),

			// Coordinator aggregate stats operations (across all peers)
			statsHistorySaveTotal: promauto.With(registry).NewCounter(prometheus.CounterOpts{
				Name: "tunnelmesh_coordinator_stats_history_save_total",
				Help: "Total successful stats history saves",
			}),
			statsHistorySaveErrors: promauto.With(registry).NewCounterVec(prometheus.CounterOpts{
				Name: "tunnelmesh_coordinator_stats_history_save_errors_total",
				Help: "Total stats history save errors",
			}, []string{"error_type"}),
			statsHistorySaveDuration: promauto.With(registry).NewHistogram(prometheus.HistogramOpts{
				Name:    "tunnelmesh_coordinator_stats_history_save_duration_seconds",
				Help:    "Stats history save latency in seconds",
				Buckets: prometheus.DefBuckets,
			}),
			statsCorruptionDetected: promauto.With(registry).NewCounterVec(prometheus.CounterOpts{
				Name: "tunnelmesh_coordinator_stats_corruption_detected_total",
				Help: "Total stats corruption events detected",
			}, []string{"stats_type"}),
		}
	})

	return coordMetricsInstance
}
