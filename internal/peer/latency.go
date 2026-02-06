package peer

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	udptransport "github.com/tunnelmesh/tunnelmesh/internal/transport/udp"
)

// LatencyProber periodically measures RTT to connected peers.
type LatencyProber struct {
	mu         sync.RWMutex
	latencies  map[string]int64 // peer name -> latency in microseconds
	udp        *udptransport.Transport
	probeEvery time.Duration
}

// NewLatencyProber creates a new latency prober.
func NewLatencyProber(udp *udptransport.Transport) *LatencyProber {
	return &LatencyProber{
		latencies:  make(map[string]int64),
		udp:        udp,
		probeEvery: 30 * time.Second, // Probe every 30 seconds
	}
}

// Start begins the latency probing loop.
func (lp *LatencyProber) Start(ctx context.Context) {
	if lp.udp == nil {
		log.Debug().Msg("latency prober: no UDP transport, skipping")
		return
	}

	// Set up pong callback to record latencies
	lp.udp.SetPongCallback(func(peerName string, rtt time.Duration) {
		lp.mu.Lock()
		lp.latencies[peerName] = rtt.Microseconds()
		lp.mu.Unlock()

		log.Debug().
			Str("peer", peerName).
			Float64("rtt_ms", float64(rtt.Microseconds())/1000.0).
			Msg("recorded peer latency")
	})

	ticker := time.NewTicker(lp.probeEvery)
	defer ticker.Stop()

	log.Debug().Dur("interval", lp.probeEvery).Msg("starting latency prober")

	// Perform initial probe immediately
	lp.probeAllPeers()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lp.probeAllPeers()
		}
	}
}

// probeAllPeers sends ping to all connected UDP peers.
func (lp *LatencyProber) probeAllPeers() {
	if lp.udp == nil {
		return
	}

	peers := lp.udp.ListConnectedPeers()
	if len(peers) == 0 {
		log.Debug().Msg("latency prober: no UDP peers connected")
		return
	}

	log.Debug().Int("count", len(peers)).Strs("peers", peers).Msg("probing latency to connected peers")

	for _, peerName := range peers {
		if err := lp.udp.SendPingToPeer(peerName); err != nil {
			log.Debug().Err(err).Str("peer", peerName).Msg("failed to send ping")
		}
	}
}

// GetLatencies returns a copy of the current latency measurements.
// Returns map of peer name -> latency in microseconds.
func (lp *LatencyProber) GetLatencies() map[string]int64 {
	lp.mu.RLock()
	defer lp.mu.RUnlock()

	// Return a copy to avoid race conditions
	result := make(map[string]int64, len(lp.latencies))
	for k, v := range lp.latencies {
		result[k] = v
	}
	return result
}

// ClearPeer removes the latency measurement for a peer.
// Called when a peer disconnects.
func (lp *LatencyProber) ClearPeer(peerName string) {
	lp.mu.Lock()
	defer lp.mu.Unlock()
	delete(lp.latencies, peerName)
}

// ClearAll removes all latency measurements.
func (lp *LatencyProber) ClearAll() {
	lp.mu.Lock()
	defer lp.mu.Unlock()
	lp.latencies = make(map[string]int64)
}
