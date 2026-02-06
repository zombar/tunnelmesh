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
		}
	})

	return coordMetricsInstance
}
