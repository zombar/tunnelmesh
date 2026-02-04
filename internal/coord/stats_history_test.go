package coord

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRingBuffer_Push(t *testing.T) {
	rb := NewRingBuffer(3)

	// Push 3 items
	rb.Push(StatsDataPoint{BytesSentRate: 1})
	rb.Push(StatsDataPoint{BytesSentRate: 2})
	rb.Push(StatsDataPoint{BytesSentRate: 3})

	if rb.Count() != 3 {
		t.Errorf("expected count 3, got %d", rb.Count())
	}

	// Push a 4th item, should wrap
	rb.Push(StatsDataPoint{BytesSentRate: 4})

	if rb.Count() != 3 {
		t.Errorf("expected count 3 after wrap, got %d", rb.Count())
	}

	// Get last 3, should be 4, 3, 2 (newest first)
	last := rb.GetLast(3)
	if len(last) != 3 {
		t.Fatalf("expected 3 items, got %d", len(last))
	}
	if last[0].BytesSentRate != 4 {
		t.Errorf("expected newest=4, got %f", last[0].BytesSentRate)
	}
	if last[1].BytesSentRate != 3 {
		t.Errorf("expected second=3, got %f", last[1].BytesSentRate)
	}
	if last[2].BytesSentRate != 2 {
		t.Errorf("expected oldest=2, got %f", last[2].BytesSentRate)
	}
}

func TestRingBuffer_GetLast_LessThanCapacity(t *testing.T) {
	rb := NewRingBuffer(10)

	rb.Push(StatsDataPoint{BytesSentRate: 1})
	rb.Push(StatsDataPoint{BytesSentRate: 2})

	// Request more than available
	last := rb.GetLast(5)
	if len(last) != 2 {
		t.Errorf("expected 2 items, got %d", len(last))
	}
	if last[0].BytesSentRate != 2 {
		t.Errorf("expected newest=2, got %f", last[0].BytesSentRate)
	}
}

func TestRingBuffer_GetLast_Empty(t *testing.T) {
	rb := NewRingBuffer(10)

	last := rb.GetLast(5)
	if last != nil {
		t.Errorf("expected nil for empty buffer, got %v", last)
	}
}

func TestRingBuffer_GetSince(t *testing.T) {
	rb := NewRingBuffer(10)

	now := time.Now()
	rb.Push(StatsDataPoint{Timestamp: now.Add(-3 * time.Minute), BytesSentRate: 1})
	rb.Push(StatsDataPoint{Timestamp: now.Add(-2 * time.Minute), BytesSentRate: 2})
	rb.Push(StatsDataPoint{Timestamp: now.Add(-1 * time.Minute), BytesSentRate: 3})
	rb.Push(StatsDataPoint{Timestamp: now, BytesSentRate: 4})

	// Get items since 2.5 minutes ago (should include items at -2m, -1m, and now)
	since := rb.GetSince(now.Add(-150 * time.Second))
	if len(since) != 3 {
		t.Fatalf("expected 3 items since 2.5min ago, got %d", len(since))
	}
	if since[0].BytesSentRate != 4 {
		t.Errorf("expected newest=4, got %f", since[0].BytesSentRate)
	}
	if since[1].BytesSentRate != 3 {
		t.Errorf("expected second=3, got %f", since[1].BytesSentRate)
	}
	if since[2].BytesSentRate != 2 {
		t.Errorf("expected oldest=2, got %f", since[2].BytesSentRate)
	}
}

func TestStatsHistory_RecordAndGet(t *testing.T) {
	sh := NewStatsHistory()

	sh.RecordStats("peer1", StatsDataPoint{BytesSentRate: 100})
	sh.RecordStats("peer1", StatsDataPoint{BytesSentRate: 200})
	sh.RecordStats("peer2", StatsDataPoint{BytesSentRate: 50})

	history1 := sh.GetHistory("peer1", 10)
	if len(history1) != 2 {
		t.Errorf("expected 2 items for peer1, got %d", len(history1))
	}
	if history1[0].BytesSentRate != 200 {
		t.Errorf("expected newest=200, got %f", history1[0].BytesSentRate)
	}

	history2 := sh.GetHistory("peer2", 10)
	if len(history2) != 1 {
		t.Errorf("expected 1 item for peer2, got %d", len(history2))
	}

	// Non-existent peer
	history3 := sh.GetHistory("peer3", 10)
	if history3 != nil {
		t.Errorf("expected nil for non-existent peer, got %v", history3)
	}
}

func TestStatsHistory_CleanupPeer(t *testing.T) {
	sh := NewStatsHistory()

	sh.RecordStats("peer1", StatsDataPoint{BytesSentRate: 100})
	sh.CleanupPeer("peer1")

	history := sh.GetHistory("peer1", 10)
	if history != nil {
		t.Errorf("expected nil after cleanup, got %v", history)
	}

	if sh.PeerCount() != 0 {
		t.Errorf("expected 0 peers after cleanup, got %d", sh.PeerCount())
	}
}

func TestStatsHistory_ConcurrentAccess(t *testing.T) {
	sh := NewStatsHistory()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(peerNum int) {
			defer wg.Done()
			peerID := "peer"
			for j := 0; j < 100; j++ {
				sh.RecordStats(peerID, StatsDataPoint{BytesSentRate: float64(j)})
				sh.GetHistory(peerID, 10)
			}
		}(i)
	}
	wg.Wait()

	// Should not panic or deadlock
	if sh.PeerCount() != 1 {
		t.Errorf("expected 1 peer, got %d", sh.PeerCount())
	}
}

func TestStatsHistory_SaveLoad(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "stats.json")

	// Create history and add data
	sh1 := NewStatsHistory()
	now := time.Now()

	sh1.RecordStats("peer1", StatsDataPoint{
		Timestamp:     now.Add(-1 * time.Hour),
		BytesSentRate: 100,
	})
	sh1.RecordStats("peer1", StatsDataPoint{
		Timestamp:     now.Add(-30 * time.Minute),
		BytesSentRate: 200,
	})
	sh1.RecordStats("peer2", StatsDataPoint{
		Timestamp:     now.Add(-15 * time.Minute),
		BytesSentRate: 50,
	})

	// Save
	if err := sh1.Save(path); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("expected file to exist after save")
	}

	// Load into new instance
	sh2 := NewStatsHistory()
	if err := sh2.Load(path); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	// Verify data
	if sh2.PeerCount() != 2 {
		t.Errorf("expected 2 peers after load, got %d", sh2.PeerCount())
	}

	history1 := sh2.GetHistory("peer1", 10)
	if len(history1) != 2 {
		t.Errorf("expected 2 items for peer1 after load, got %d", len(history1))
	}

	history2 := sh2.GetHistory("peer2", 10)
	if len(history2) != 1 {
		t.Errorf("expected 1 item for peer2 after load, got %d", len(history2))
	}
}

func TestStatsHistory_LoadNonExistent(t *testing.T) {
	sh := NewStatsHistory()
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "nonexistent.json")

	// Loading non-existent file should not error (just start fresh)
	if err := sh.Load(path); err != nil {
		t.Errorf("load of non-existent file should not error, got: %v", err)
	}

	if sh.PeerCount() != 0 {
		t.Errorf("expected 0 peers after loading non-existent file, got %d", sh.PeerCount())
	}
}

func TestStatsHistory_LoadExpiredData(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "stats.json")

	// Create history with old data (more than 3 days ago)
	sh1 := NewStatsHistory()
	now := time.Now()

	// Add data from 4 days ago (should be filtered out on load)
	sh1.RecordStats("peer1", StatsDataPoint{
		Timestamp:     now.Add(-4 * 24 * time.Hour),
		BytesSentRate: 100,
	})
	// Add recent data (should be kept)
	sh1.RecordStats("peer1", StatsDataPoint{
		Timestamp:     now.Add(-1 * time.Hour),
		BytesSentRate: 200,
	})

	if err := sh1.Save(path); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// Load into new instance
	sh2 := NewStatsHistory()
	if err := sh2.Load(path); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	// Should only have the recent data point
	history := sh2.GetHistory("peer1", 10)
	if len(history) != 1 {
		t.Errorf("expected 1 item after filtering old data, got %d", len(history))
	}
	if history[0].BytesSentRate != 200 {
		t.Errorf("expected recent data point (200), got %f", history[0].BytesSentRate)
	}
}
