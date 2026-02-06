// Package coord provides the TunnelMesh coordinator server.
package coord

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// CoordRegistry is the Prometheus registry for coordinator metrics.
var CoordRegistry = prometheus.NewRegistry()

// coordMetricsOnce ensures metrics are only initialized once.
var coordMetricsOnce sync.Once

// coordMetricsInstance is the singleton instance of coordinator metrics.
var coordMetricsInstance *CoordMetrics

// CoordMetrics holds all Prometheus metrics for the coordinator.
type CoordMetrics struct {
	// Peer RTT metrics
	PeerRTTMs *prometheus.GaugeVec // tunnelmesh_coord_peer_rtt_ms{peer}

	// Peer-to-peer latency
	PeerLatencyMs *prometheus.GaugeVec // tunnelmesh_coord_peer_latency_ms{source,target}

	// Connection gauges
	OnlinePeers prometheus.Gauge

	// Heartbeat stats
	TotalHeartbeats prometheus.Counter
}

func init() {
	// Register standard Go metrics
	CoordRegistry.MustRegister(collectors.NewGoCollector())
	CoordRegistry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

// InitCoordMetrics initializes all coordinator metrics.
// Metrics are only registered once; subsequent calls return the same instance.
func InitCoordMetrics() *CoordMetrics {
	coordMetricsOnce.Do(func() {
		coordMetricsInstance = &CoordMetrics{
			PeerRTTMs: promauto.With(CoordRegistry).NewGaugeVec(prometheus.GaugeOpts{
				Name: "tunnelmesh_coord_peer_rtt_ms",
				Help: "RTT reported by peer to coordinator in milliseconds",
			}, []string{"peer"}),

			PeerLatencyMs: promauto.With(CoordRegistry).NewGaugeVec(prometheus.GaugeOpts{
				Name: "tunnelmesh_coord_peer_latency_ms",
				Help: "Latency between peers reported to coordinator in milliseconds",
			}, []string{"source", "target"}),

			OnlinePeers: promauto.With(CoordRegistry).NewGauge(prometheus.GaugeOpts{
				Name: "tunnelmesh_coord_online_peers",
				Help: "Number of currently online peers",
			}),

			TotalHeartbeats: promauto.With(CoordRegistry).NewCounter(prometheus.CounterOpts{
				Name: "tunnelmesh_coord_heartbeats_total",
				Help: "Total heartbeats received by coordinator",
			}),
		}
	})

	return coordMetricsInstance
}
