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

func TestStatsHistory_FileSizeLimit(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "stats.json")

	sh := NewStatsHistory()
	now := time.Now()

	// Add maximum data points for multiple peers (simulating 3 days of data)
	// At 10-second intervals, 3 days = 25920 points per peer
	numPeers := 10
	pointsPerPeer := MaxHistoryPoints // 25920

	for p := 0; p < numPeers; p++ {
		peerName := "peer" + string(rune('A'+p))
		for i := 0; i < pointsPerPeer; i++ {
			sh.RecordStats(peerName, StatsDataPoint{
				Timestamp:           now.Add(-time.Duration(pointsPerPeer-i) * 10 * time.Second),
				BytesSentRate:       float64(i * 100),
				BytesReceivedRate:   float64(i * 50),
				PacketsSentRate:     float64(i),
				PacketsReceivedRate: float64(i),
			})
		}
	}

	// Save and check file size
	if err := sh.Save(path); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}

	// File should be reasonable size
	// Each data point is roughly 80-100 bytes in JSON
	// 10 peers * 25920 points * ~100 bytes = ~26 MB max
	maxExpectedSize := int64(30 * 1024 * 1024) // 30 MB max
	if info.Size() > maxExpectedSize {
		t.Errorf("file size %d bytes exceeds expected max %d bytes", info.Size(), maxExpectedSize)
	}

	t.Logf("File size for %d peers with %d points each: %d bytes (%.2f MB)",
		numPeers, pointsPerPeer, info.Size(), float64(info.Size())/(1024*1024))
}

func TestStatsHistory_RingBufferCapacity(t *testing.T) {
	sh := NewStatsHistory()
	now := time.Now()

	// Add more than MaxHistoryPoints to verify ring buffer wraps correctly
	extraPoints := 100
	totalPoints := MaxHistoryPoints + extraPoints

	for i := 0; i < totalPoints; i++ {
		sh.RecordStats("peer1", StatsDataPoint{
			Timestamp:     now.Add(time.Duration(i) * 30 * time.Second),
			BytesSentRate: float64(i),
		})
	}

	// Should only have MaxHistoryPoints stored
	history := sh.GetHistory("peer1", totalPoints)
	if len(history) != MaxHistoryPoints {
		t.Errorf("expected %d points (MaxHistoryPoints), got %d", MaxHistoryPoints, len(history))
	}

	// The oldest points should have been dropped
	// Newest should be totalPoints-1, oldest should be extraPoints
	if history[0].BytesSentRate != float64(totalPoints-1) {
		t.Errorf("expected newest value %d, got %f", totalPoints-1, history[0].BytesSentRate)
	}
	if history[len(history)-1].BytesSentRate != float64(extraPoints) {
		t.Errorf("expected oldest value %d, got %f", extraPoints, history[len(history)-1].BytesSentRate)
	}
}

func TestStatsHistory_SaveLoadRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "stats.json")

	sh1 := NewStatsHistory()
	now := time.Now()

	// Add data with all fields populated
	testData := []StatsDataPoint{
		{
			Timestamp:           now.Add(-2 * time.Hour),
			BytesSentRate:       1234.56,
			BytesReceivedRate:   789.01,
			PacketsSentRate:     100.5,
			PacketsReceivedRate: 200.25,
		},
		{
			Timestamp:           now.Add(-1 * time.Hour),
			BytesSentRate:       2345.67,
			BytesReceivedRate:   890.12,
			PacketsSentRate:     150.5,
			PacketsReceivedRate: 250.25,
		},
	}

	for _, dp := range testData {
		sh1.RecordStats("test-peer", dp)
	}

	// Save
	if err := sh1.Save(path); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// Load
	sh2 := NewStatsHistory()
	if err := sh2.Load(path); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	// Verify all fields preserved
	history := sh2.GetHistory("test-peer", 10)
	if len(history) != 2 {
		t.Fatalf("expected 2 items, got %d", len(history))
	}

	// History is newest first, so index 0 is the second test data point
	if history[0].BytesSentRate != testData[1].BytesSentRate {
		t.Errorf("BytesSentRate mismatch: expected %f, got %f", testData[1].BytesSentRate, history[0].BytesSentRate)
	}
	if history[0].BytesReceivedRate != testData[1].BytesReceivedRate {
		t.Errorf("BytesReceivedRate mismatch: expected %f, got %f", testData[1].BytesReceivedRate, history[0].BytesReceivedRate)
	}
	if history[0].PacketsSentRate != testData[1].PacketsSentRate {
		t.Errorf("PacketsSentRate mismatch: expected %f, got %f", testData[1].PacketsSentRate, history[0].PacketsSentRate)
	}
	if history[0].PacketsReceivedRate != testData[1].PacketsReceivedRate {
		t.Errorf("PacketsReceivedRate mismatch: expected %f, got %f", testData[1].PacketsReceivedRate, history[0].PacketsReceivedRate)
	}
}

func TestStatsHistory_MultipleSaveLoadCycles(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "stats.json")

	now := time.Now()

	// Simulate multiple server restarts with data accumulation
	for cycle := 0; cycle < 5; cycle++ {
		sh := NewStatsHistory()

		// Load existing data
		if err := sh.Load(path); err != nil {
			t.Fatalf("cycle %d: load failed: %v", cycle, err)
		}

		// Add new data
		for i := 0; i < 100; i++ {
			sh.RecordStats("peer1", StatsDataPoint{
				Timestamp:     now.Add(time.Duration(cycle*100+i) * 30 * time.Second),
				BytesSentRate: float64(cycle*1000 + i),
			})
		}

		// Save
		if err := sh.Save(path); err != nil {
			t.Fatalf("cycle %d: save failed: %v", cycle, err)
		}
	}

	// Final load and verify
	shFinal := NewStatsHistory()
	if err := shFinal.Load(path); err != nil {
		t.Fatalf("final load failed: %v", err)
	}

	history := shFinal.GetHistory("peer1", 1000)
	if len(history) != 500 { // 5 cycles * 100 points
		t.Errorf("expected 500 points after 5 cycles, got %d", len(history))
	}
}

func TestStatsHistory_LoadCorruptJSON(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "corrupt.json")

	// Write corrupt JSON
	if err := os.WriteFile(path, []byte("{ invalid json }"), 0600); err != nil {
		t.Fatalf("failed to write corrupt file: %v", err)
	}

	sh := NewStatsHistory()
	err := sh.Load(path)
	if err == nil {
		t.Error("expected error loading corrupt JSON, got nil")
	}
}

func TestStatsHistory_LoadEmptyFile(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "empty.json")

	// Write empty file
	if err := os.WriteFile(path, []byte(""), 0600); err != nil {
		t.Fatalf("failed to write empty file: %v", err)
	}

	sh := NewStatsHistory()
	err := sh.Load(path)
	if err == nil {
		t.Error("expected error loading empty file, got nil")
	}
}

func TestStatsHistory_LoadEmptyJSON(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "empty.json")

	// Write valid but empty JSON
	if err := os.WriteFile(path, []byte(`{"peers":{}}`), 0600); err != nil {
		t.Fatalf("failed to write empty JSON file: %v", err)
	}

	sh := NewStatsHistory()
	if err := sh.Load(path); err != nil {
		t.Errorf("loading empty JSON should not error: %v", err)
	}

	if sh.PeerCount() != 0 {
		t.Errorf("expected 0 peers, got %d", sh.PeerCount())
	}
}

func TestStatsHistory_SaveOverwritesExisting(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "stats.json")

	now := time.Now()

	// First save
	sh1 := NewStatsHistory()
	sh1.RecordStats("peer1", StatsDataPoint{
		Timestamp:     now,
		BytesSentRate: 100,
	})
	if err := sh1.Save(path); err != nil {
		t.Fatalf("first save failed: %v", err)
	}

	// Second save with different data (should overwrite)
	sh2 := NewStatsHistory()
	sh2.RecordStats("peer2", StatsDataPoint{
		Timestamp:     now,
		BytesSentRate: 200,
	})
	if err := sh2.Save(path); err != nil {
		t.Fatalf("second save failed: %v", err)
	}

	// Load and verify only second data exists
	sh3 := NewStatsHistory()
	if err := sh3.Load(path); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if sh3.PeerCount() != 1 {
		t.Errorf("expected 1 peer, got %d", sh3.PeerCount())
	}

	history1 := sh3.GetHistory("peer1", 10)
	if history1 != nil {
		t.Error("peer1 should not exist after overwrite")
	}

	history2 := sh3.GetHistory("peer2", 10)
	if history2 == nil || len(history2) != 1 {
		t.Error("peer2 should exist with 1 data point")
	}
}

func TestStatsHistory_ConcurrentRecordAndSave(t *testing.T) {
	// Tests that concurrent RecordStats calls work correctly with Save/Load
	// Note: Concurrent file writes from multiple processes are NOT supported
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "stats.json")

	sh := NewStatsHistory()
	now := time.Now()

	// Concurrent recording of stats
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			peerName := "peer" + string(rune('A'+n))
			for j := 0; j < 100; j++ {
				sh.RecordStats(peerName, StatsDataPoint{
					Timestamp:     now.Add(time.Duration(j) * 30 * time.Second),
					BytesSentRate: float64(n*1000 + j),
				})
			}
		}(i)
	}
	wg.Wait()

	// Save after all concurrent writes complete
	if err := sh.Save(path); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// Load and verify
	sh2 := NewStatsHistory()
	if err := sh2.Load(path); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if sh2.PeerCount() != 10 {
		t.Errorf("expected 10 peers, got %d", sh2.PeerCount())
	}

	// Each peer should have 100 data points
	for i := 0; i < 10; i++ {
		peerName := "peer" + string(rune('A'+i))
		history := sh2.GetHistory(peerName, 200)
		if len(history) != 100 {
			t.Errorf("peer %s: expected 100 points, got %d", peerName, len(history))
		}
	}
}

func TestStatsHistory_PeerWithAllExpiredData(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "stats.json")

	sh1 := NewStatsHistory()
	now := time.Now()

	// Add data for peer1 that's all expired
	sh1.RecordStats("peer1", StatsDataPoint{
		Timestamp:     now.Add(-5 * 24 * time.Hour),
		BytesSentRate: 100,
	})

	// Add recent data for peer2
	sh1.RecordStats("peer2", StatsDataPoint{
		Timestamp:     now.Add(-1 * time.Hour),
		BytesSentRate: 200,
	})

	if err := sh1.Save(path); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// Load
	sh2 := NewStatsHistory()
	if err := sh2.Load(path); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	// peer1 should be completely removed (all data expired)
	// peer2 should exist
	if sh2.PeerCount() != 1 {
		t.Errorf("expected 1 peer (peer2 only), got %d", sh2.PeerCount())
	}

	if sh2.GetHistory("peer1", 10) != nil {
		t.Error("peer1 should not exist (all data expired)")
	}

	if sh2.GetHistory("peer2", 10) == nil {
		t.Error("peer2 should exist")
	}
}
