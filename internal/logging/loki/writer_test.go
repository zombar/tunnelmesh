package loki

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewWriter_Defaults(t *testing.T) {
	w := NewWriter(Config{
		URL: "http://localhost:3100",
	})

	if w.batchSize != 100 {
		t.Errorf("expected default batchSize 100, got %d", w.batchSize)
	}
	if w.flushInterval != 5*time.Second {
		t.Errorf("expected default flushInterval 5s, got %v", w.flushInterval)
	}
	if w.labels["job"] != "tunnelmesh" {
		t.Errorf("expected default job label 'tunnelmesh', got %q", w.labels["job"])
	}
}

func TestNewWriter_CustomConfig(t *testing.T) {
	w := NewWriter(Config{
		URL:           "http://localhost:3100",
		BatchSize:     50,
		FlushInterval: 10 * time.Second,
		Timeout:       30 * time.Second,
		Labels: map[string]string{
			"peer":    "test-peer",
			"mesh_ip": "10.99.0.1",
		},
	})

	if w.batchSize != 50 {
		t.Errorf("expected batchSize 50, got %d", w.batchSize)
	}
	if w.flushInterval != 10*time.Second {
		t.Errorf("expected flushInterval 10s, got %v", w.flushInterval)
	}
	if w.labels["peer"] != "test-peer" {
		t.Errorf("expected peer label 'test-peer', got %q", w.labels["peer"])
	}
	if w.labels["mesh_ip"] != "10.99.0.1" {
		t.Errorf("expected mesh_ip label '10.99.0.1', got %q", w.labels["mesh_ip"])
	}
	// job label should still be set
	if w.labels["job"] != "tunnelmesh" {
		t.Errorf("expected job label 'tunnelmesh', got %q", w.labels["job"])
	}
}

func TestWriter_Write_BuffersEntries(t *testing.T) {
	w := NewWriter(Config{
		URL:       "http://localhost:3100",
		BatchSize: 10,
	})

	// Write some entries
	testMsg := []byte(`{"level":"info","msg":"test message"}`)
	for i := 0; i < 5; i++ {
		n, err := w.Write(testMsg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != len(testMsg) {
			t.Errorf("expected n=%d, got %d", len(testMsg), n)
		}
	}

	w.mu.Lock()
	bufLen := len(w.buffer)
	w.mu.Unlock()

	if bufLen != 5 {
		t.Errorf("expected 5 buffered entries, got %d", bufLen)
	}
}

func TestWriter_Write_SkipsEmptyLines(t *testing.T) {
	w := NewWriter(Config{
		URL:       "http://localhost:3100",
		BatchSize: 10,
	})

	// Write empty and whitespace-only entries
	_, _ = w.Write([]byte(""))
	_, _ = w.Write([]byte("   "))
	_, _ = w.Write([]byte("\n"))
	_, _ = w.Write([]byte(`{"level":"info","msg":"real message"}`))

	w.mu.Lock()
	bufLen := len(w.buffer)
	w.mu.Unlock()

	if bufLen != 1 {
		t.Errorf("expected 1 buffered entry (empty lines skipped), got %d", bufLen)
	}
}

func TestWriter_Write_FlushesWhenBatchFull(t *testing.T) {
	var requestCount atomic.Int32
	var receivedPayload lokiPushRequest
	var payloadMu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		body, _ := io.ReadAll(r.Body)
		payloadMu.Lock()
		_ = json.Unmarshal(body, &receivedPayload)
		payloadMu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	w := NewWriter(Config{
		URL:       server.URL,
		BatchSize: 3,
	})

	// Start background flush goroutine
	w.Start()
	defer w.Stop()

	// Write exactly batch size entries
	_, _ = w.Write([]byte(`{"level":"info","msg":"message 1"}`))
	_, _ = w.Write([]byte(`{"level":"info","msg":"message 2"}`))
	_, _ = w.Write([]byte(`{"level":"info","msg":"message 3"}`))

	// Give time for async flush
	time.Sleep(100 * time.Millisecond)

	if requestCount.Load() != 1 {
		t.Errorf("expected 1 flush request, got %d", requestCount.Load())
	}

	payloadMu.Lock()
	streamCount := len(receivedPayload.Streams)
	var valueCount int
	if streamCount > 0 {
		valueCount = len(receivedPayload.Streams[0].Values)
	}
	payloadMu.Unlock()

	if streamCount != 1 {
		t.Fatalf("expected 1 stream, got %d", streamCount)
	}

	if valueCount != 3 {
		t.Errorf("expected 3 values in stream, got %d", valueCount)
	}
}

func TestWriter_Flush_SendsCorrectPayload(t *testing.T) {
	var receivedPayload lokiPushRequest
	var receivedContentType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedPayload)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	w := NewWriter(Config{
		URL:       server.URL,
		BatchSize: 100,
		Labels: map[string]string{
			"peer":    "test-peer",
			"mesh_ip": "10.99.0.5",
		},
	})

	_, _ = w.Write([]byte(`{"level":"info","msg":"test log line"}`))
	w.flush()

	if receivedContentType != "application/json" {
		t.Errorf("expected Content-Type 'application/json', got %q", receivedContentType)
	}

	if len(receivedPayload.Streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(receivedPayload.Streams))
	}

	stream := receivedPayload.Streams[0]

	// Check labels
	if stream.Stream["peer"] != "test-peer" {
		t.Errorf("expected peer label 'test-peer', got %q", stream.Stream["peer"])
	}
	if stream.Stream["mesh_ip"] != "10.99.0.5" {
		t.Errorf("expected mesh_ip label '10.99.0.5', got %q", stream.Stream["mesh_ip"])
	}
	if stream.Stream["job"] != "tunnelmesh" {
		t.Errorf("expected job label 'tunnelmesh', got %q", stream.Stream["job"])
	}

	// Check values
	if len(stream.Values) != 1 {
		t.Fatalf("expected 1 value, got %d", len(stream.Values))
	}

	// Each value is [timestamp, line]
	if len(stream.Values[0]) != 2 {
		t.Fatalf("expected value tuple of length 2, got %d", len(stream.Values[0]))
	}

	// Timestamp should be a nanosecond timestamp string
	ts := stream.Values[0][0]
	if len(ts) < 19 { // nanosecond timestamps are at least 19 digits
		t.Errorf("timestamp %q seems too short for nanoseconds", ts)
	}

	// Line should be the log content
	if stream.Values[0][1] != `{"level":"info","msg":"test log line"}` {
		t.Errorf("unexpected log line: %q", stream.Values[0][1])
	}
}

func TestWriter_Flush_EmptyBuffer(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	w := NewWriter(Config{
		URL:       server.URL,
		BatchSize: 100,
	})

	// Flush with empty buffer should not make request
	w.flush()

	if requestCount.Load() != 0 {
		t.Errorf("expected no requests for empty buffer, got %d", requestCount.Load())
	}
}

func TestWriter_StartStop_PeriodicFlush(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	w := NewWriter(Config{
		URL:           server.URL,
		BatchSize:     1000, // High batch size so we don't trigger batch flush
		FlushInterval: 50 * time.Millisecond,
	})

	w.Start()

	// Write some entries
	_, _ = w.Write([]byte(`{"level":"info","msg":"message 1"}`))
	_, _ = w.Write([]byte(`{"level":"info","msg":"message 2"}`))

	// Wait for periodic flush
	time.Sleep(100 * time.Millisecond)

	if requestCount.Load() < 1 {
		t.Errorf("expected at least 1 periodic flush, got %d", requestCount.Load())
	}

	w.Stop()
}

func TestWriter_Stop_FinalFlush(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	w := NewWriter(Config{
		URL:           server.URL,
		BatchSize:     1000,          // High batch size
		FlushInterval: 1 * time.Hour, // Long interval
	})

	w.Start()
	_, _ = w.Write([]byte(`{"level":"info","msg":"final message"}`))

	// Stop should trigger final flush
	w.Stop()

	if requestCount.Load() != 1 {
		t.Errorf("expected 1 final flush on Stop, got %d", requestCount.Load())
	}
}

func TestWriter_SetLabels(t *testing.T) {
	w := NewWriter(Config{
		URL: "http://localhost:3100",
		Labels: map[string]string{
			"peer": "original",
		},
	})

	w.SetLabels(map[string]string{
		"mesh_ip": "10.99.0.10",
		"version": "1.0.0",
	})

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.labels["peer"] != "original" {
		t.Errorf("expected peer label to remain 'original', got %q", w.labels["peer"])
	}
	if w.labels["mesh_ip"] != "10.99.0.10" {
		t.Errorf("expected mesh_ip label '10.99.0.10', got %q", w.labels["mesh_ip"])
	}
	if w.labels["version"] != "1.0.0" {
		t.Errorf("expected version label '1.0.0', got %q", w.labels["version"])
	}
}

func TestWriter_HandlesLokiUnavailable(t *testing.T) {
	// Server that always fails
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	w := NewWriter(Config{
		URL:       server.URL,
		BatchSize: 100,
	})

	// Write should not return error even if Loki is unavailable
	n, err := w.Write([]byte(`{"level":"info","msg":"test"}`))
	if err != nil {
		t.Errorf("Write should not return error: %v", err)
	}
	if n == 0 {
		t.Error("Write should return bytes written")
	}

	// Flush should not panic or return error
	w.flush()
}

func TestWriter_HandlesLokiConnectionRefused(t *testing.T) {
	w := NewWriter(Config{
		URL:       "http://localhost:1", // Invalid port
		BatchSize: 100,
		Timeout:   100 * time.Millisecond,
	})

	_, _ = w.Write([]byte(`{"level":"info","msg":"test"}`))

	// Should not panic
	w.flush()
}

func TestWriter_ConcurrentWrites(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	w := NewWriter(Config{
		URL:           server.URL,
		BatchSize:     10,
		FlushInterval: 50 * time.Millisecond,
	})

	w.Start()
	defer w.Stop()

	// Concurrent writes
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = w.Write([]byte(`{"level":"info","msg":"concurrent message"}`))
		}(i)
	}
	wg.Wait()

	// Wait for flushes
	time.Sleep(200 * time.Millisecond)

	// Should have made multiple flush requests
	if requestCount.Load() < 1 {
		t.Errorf("expected at least 1 flush request, got %d", requestCount.Load())
	}
}
