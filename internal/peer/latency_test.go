package peer

import (
	"testing"
)

func TestLatencyProber_GetLatencies(t *testing.T) {
	// Create prober without UDP transport (safe for testing)
	lp := NewLatencyProber(nil)

	// Initially empty
	latencies := lp.GetLatencies()
	if len(latencies) != 0 {
		t.Errorf("expected empty latencies, got %v", latencies)
	}

	// Manually set some latencies (simulating pong callback)
	lp.mu.Lock()
	lp.latencies["peer1"] = 15
	lp.latencies["peer2"] = 25
	lp.mu.Unlock()

	// Verify we can retrieve them
	latencies = lp.GetLatencies()
	if len(latencies) != 2 {
		t.Errorf("expected 2 latencies, got %d", len(latencies))
	}
	if latencies["peer1"] != 15 {
		t.Errorf("expected peer1 latency 15, got %d", latencies["peer1"])
	}
	if latencies["peer2"] != 25 {
		t.Errorf("expected peer2 latency 25, got %d", latencies["peer2"])
	}

	// Verify it returns a copy (modifications don't affect original)
	latencies["peer1"] = 100
	original := lp.GetLatencies()
	if original["peer1"] != 15 {
		t.Errorf("GetLatencies should return a copy, original was modified")
	}
}

func TestLatencyProber_ClearPeer(t *testing.T) {
	lp := NewLatencyProber(nil)

	// Add some latencies
	lp.mu.Lock()
	lp.latencies["peer1"] = 15
	lp.latencies["peer2"] = 25
	lp.mu.Unlock()

	// Clear one peer
	lp.ClearPeer("peer1")

	latencies := lp.GetLatencies()
	if len(latencies) != 1 {
		t.Errorf("expected 1 latency after clear, got %d", len(latencies))
	}
	if _, exists := latencies["peer1"]; exists {
		t.Error("peer1 should have been cleared")
	}
	if latencies["peer2"] != 25 {
		t.Errorf("peer2 should not have been affected")
	}
}

func TestLatencyProber_ClearAll(t *testing.T) {
	lp := NewLatencyProber(nil)

	// Add some latencies
	lp.mu.Lock()
	lp.latencies["peer1"] = 15
	lp.latencies["peer2"] = 25
	lp.mu.Unlock()

	// Clear all
	lp.ClearAll()

	latencies := lp.GetLatencies()
	if len(latencies) != 0 {
		t.Errorf("expected empty latencies after clear all, got %v", latencies)
	}
}

func TestLatencyProber_NilUDP(t *testing.T) {
	lp := NewLatencyProber(nil)

	// Should not panic with nil UDP transport
	lp.probeAllPeers()

	// GetLatencies should still work
	latencies := lp.GetLatencies()
	if len(latencies) != 0 {
		t.Errorf("expected empty latencies, got %v", latencies)
	}
}
