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
	PeerRTTMs *prometheus.GaugeVec // tunnelmesh_coordinator_peer_rtt_ms{peer}

	// Peer-to-peer latency
	PeerLatencyMs *prometheus.GaugeVec // tunnelmesh_coordinator_peer_latency_ms{source,target}

	// Connection gauges
	OnlinePeers prometheus.Gauge

	// Heartbeat stats
	TotalHeartbeats prometheus.Counter
}

// InitCoordMetrics initializes all coordinator metrics.
// Metrics are only registered once; subsequent calls return the same instance.
// Uses the default Prometheus registry so metrics are exposed on the peer's
// /metrics endpoint when running in join_mesh mode.
func InitCoordMetrics() *CoordMetrics {
	coordMetricsOnce.Do(func() {
		coordMetricsInstance = &CoordMetrics{
			PeerRTTMs: promauto.NewGaugeVec(prometheus.GaugeOpts{
				Name: "tunnelmesh_coordinator_peer_rtt_ms",
				Help: "RTT reported by peer to coordinator in milliseconds",
			}, []string{"peer"}),

			PeerLatencyMs: promauto.NewGaugeVec(prometheus.GaugeOpts{
				Name: "tunnelmesh_coordinator_peer_latency_ms",
				Help: "Latency between peers reported to coordinator in milliseconds",
			}, []string{"source", "target"}),

			OnlinePeers: promauto.NewGauge(prometheus.GaugeOpts{
				Name: "tunnelmesh_coordinator_online_peers",
				Help: "Number of currently online peers",
			}),

			TotalHeartbeats: promauto.NewCounter(prometheus.CounterOpts{
				Name: "tunnelmesh_coordinator_heartbeats_total",
				Help: "Total heartbeats received by coordinator",
			}),
		}
	})

	return coordMetricsInstance
}
