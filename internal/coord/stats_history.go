package coord

import (
	"sync"
	"time"
)

// MaxHistoryPoints is the maximum number of stats data points to store per peer.
// At 30-second heartbeat intervals, this provides ~1 week of history.
const MaxHistoryPoints = 20160

// StatsDataPoint represents a single stats measurement at a point in time.
type StatsDataPoint struct {
	Timestamp           time.Time `json:"ts"`
	BytesSentRate       float64   `json:"txB"`
	BytesReceivedRate   float64   `json:"rxB"`
	PacketsSentRate     float64   `json:"txP"`
	PacketsReceivedRate float64   `json:"rxP"`
}

// RingBuffer is a fixed-size circular buffer for stats data points.
type RingBuffer struct {
	data  []StatsDataPoint
	head  int // next write position
	count int // number of items in buffer
}

// NewRingBuffer creates a new ring buffer with the specified capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{
		data: make([]StatsDataPoint, capacity),
	}
}

// Push adds a data point to the buffer.
func (rb *RingBuffer) Push(dp StatsDataPoint) {
	rb.data[rb.head] = dp
	rb.head = (rb.head + 1) % len(rb.data)
	if rb.count < len(rb.data) {
		rb.count++
	}
}

// GetLast returns the last n data points, newest first.
func (rb *RingBuffer) GetLast(n int) []StatsDataPoint {
	if n <= 0 || rb.count == 0 {
		return nil
	}
	if n > rb.count {
		n = rb.count
	}

	result := make([]StatsDataPoint, n)
	// Start from most recent (head-1) and go backwards
	for i := 0; i < n; i++ {
		idx := (rb.head - 1 - i + len(rb.data)) % len(rb.data)
		result[i] = rb.data[idx]
	}
	return result
}

// GetSince returns all data points since the given timestamp, newest first.
func (rb *RingBuffer) GetSince(since time.Time) []StatsDataPoint {
	if rb.count == 0 {
		return nil
	}

	var result []StatsDataPoint
	// Start from most recent (head-1) and go backwards
	for i := 0; i < rb.count; i++ {
		idx := (rb.head - 1 - i + len(rb.data)) % len(rb.data)
		dp := rb.data[idx]
		if dp.Timestamp.Before(since) || dp.Timestamp.Equal(since) {
			break
		}
		result = append(result, dp)
	}
	return result
}

// Count returns the number of items in the buffer.
func (rb *RingBuffer) Count() int {
	return rb.count
}

// StatsHistory manages per-peer stats history with thread-safe access.
type StatsHistory struct {
	mu    sync.RWMutex
	peers map[string]*RingBuffer
}

// NewStatsHistory creates a new stats history store.
func NewStatsHistory() *StatsHistory {
	return &StatsHistory{
		peers: make(map[string]*RingBuffer),
	}
}

// RecordStats adds a new data point for a peer.
func (sh *StatsHistory) RecordStats(peerID string, dp StatsDataPoint) {
	sh.mu.Lock()
	defer sh.mu.Unlock()

	rb, exists := sh.peers[peerID]
	if !exists {
		rb = NewRingBuffer(MaxHistoryPoints)
		sh.peers[peerID] = rb
	}
	rb.Push(dp)
}

// GetHistory returns the last n data points for a peer, newest first.
func (sh *StatsHistory) GetHistory(peerID string, limit int) []StatsDataPoint {
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	rb, exists := sh.peers[peerID]
	if !exists {
		return nil
	}
	return rb.GetLast(limit)
}

// GetHistorySince returns all data points for a peer since the given timestamp.
func (sh *StatsHistory) GetHistorySince(peerID string, since time.Time) []StatsDataPoint {
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	rb, exists := sh.peers[peerID]
	if !exists {
		return nil
	}
	return rb.GetSince(since)
}

// CleanupPeer removes all history for a peer.
func (sh *StatsHistory) CleanupPeer(peerID string) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	delete(sh.peers, peerID)
}

// PeerCount returns the number of peers with history.
func (sh *StatsHistory) PeerCount() int {
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	return len(sh.peers)
}
