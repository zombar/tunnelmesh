// Package loki provides a zerolog writer that pushes logs to Grafana Loki.
package loki

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Config holds configuration for the Loki writer.
type Config struct {
	URL           string            // Loki push URL (e.g., "http://10.99.0.1:3100")
	Labels        map[string]string // Static labels to add to all log entries
	BatchSize     int               // Max entries before flush (default: 100)
	FlushInterval time.Duration     // Flush interval (default: 5s)
	Timeout       time.Duration     // HTTP timeout (default: 10s)
}

// Writer implements io.Writer and pushes logs to Loki.
// It buffers log entries and flushes them periodically or when the batch is full.
type Writer struct {
	url    string
	labels map[string]string
	client *http.Client

	mu        sync.Mutex
	buffer    []entry
	batchSize int

	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	flushInterval time.Duration

	// Concurrency control
	flushing     int32         // atomic flag to prevent concurrent flushes
	flushTrigger chan struct{} // buffered channel to limit goroutine spawning

	// Error tracking
	flushErrors uint64 // atomic counter for flush failures
}

type entry struct {
	timestamp time.Time
	line      string
}

// lokiPushRequest is the payload format for Loki's push API.
type lokiPushRequest struct {
	Streams []lokiStream `json:"streams"`
}

type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][]string        `json:"values"`
}

// NewWriter creates a new Loki writer with the given configuration.
func NewWriter(cfg Config) *Writer {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 5 * time.Second
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.Labels == nil {
		cfg.Labels = make(map[string]string)
	}
	// Always add job label
	if _, ok := cfg.Labels["job"]; !ok {
		cfg.Labels["job"] = "tunnelmesh"
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Writer{
		url:           cfg.URL,
		labels:        cfg.Labels,
		client:        &http.Client{Timeout: cfg.Timeout},
		buffer:        make([]entry, 0, cfg.BatchSize),
		batchSize:     cfg.BatchSize,
		ctx:           ctx,
		cancel:        cancel,
		flushInterval: cfg.FlushInterval,
		flushTrigger:  make(chan struct{}, 1), // buffered to allow 1 pending flush
	}
}

// Write implements io.Writer. It buffers the log entry and triggers a flush
// if the buffer is full. This method never returns an error to avoid
// disrupting logging when Loki is unavailable.
func (w *Writer) Write(p []byte) (n int, err error) {
	// Make a copy since zerolog reuses the buffer
	line := string(bytes.TrimSpace(p))
	if line == "" {
		return len(p), nil
	}

	w.mu.Lock()
	w.buffer = append(w.buffer, entry{
		timestamp: time.Now(),
		line:      line,
	})
	shouldFlush := len(w.buffer) >= w.batchSize
	w.mu.Unlock()

	if shouldFlush {
		// Signal flush needed - non-blocking to avoid slowing down logging
		select {
		case w.flushTrigger <- struct{}{}:
			// Flush will be picked up by the background goroutine
		default:
			// Flush already pending, skip
		}
	}

	return len(p), nil
}

// Start begins the background flush goroutine.
func (w *Writer) Start() {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		ticker := time.NewTicker(w.flushInterval)
		defer ticker.Stop()

		for {
			select {
			case <-w.ctx.Done():
				return
			case <-ticker.C:
				w.flush()
			case <-w.flushTrigger:
				w.flush()
			}
		}
	}()
}

// Stop gracefully shuts down the writer, flushing any remaining entries.
func (w *Writer) Stop() {
	w.cancel()
	w.wg.Wait()
	// Final flush
	w.flush()
}

// flush sends buffered entries to Loki.
// Uses atomic flag to prevent concurrent flushes.
func (w *Writer) flush() {
	// Prevent concurrent flushes
	if !atomic.CompareAndSwapInt32(&w.flushing, 0, 1) {
		return // Another flush is in progress
	}
	defer atomic.StoreInt32(&w.flushing, 0)

	w.mu.Lock()
	if len(w.buffer) == 0 {
		w.mu.Unlock()
		return
	}
	// Swap buffer
	entries := w.buffer
	w.buffer = make([]entry, 0, w.batchSize)
	w.mu.Unlock()

	// Build Loki push payload
	values := make([][]string, len(entries))
	for i, e := range entries {
		// Loki expects nanosecond timestamps as strings
		values[i] = []string{
			strconv.FormatInt(e.timestamp.UnixNano(), 10),
			e.line,
		}
	}

	payload := lokiPushRequest{
		Streams: []lokiStream{
			{
				Stream: w.labels,
				Values: values,
			},
		},
	}

	data, err := json.Marshal(payload)
	if err != nil {
		atomic.AddUint64(&w.flushErrors, 1)
		// Log to stderr to avoid logging loops - only on first few errors
		if w.flushErrors <= 3 {
			fmt.Fprintf(os.Stderr, "loki: failed to marshal payload: %v\n", err)
		}
		return
	}

	// Send to Loki
	ctx, cancel := context.WithTimeout(context.Background(), w.client.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", w.url+"/loki/api/v1/push", bytes.NewReader(data))
	if err != nil {
		atomic.AddUint64(&w.flushErrors, 1)
		if w.flushErrors <= 3 {
			fmt.Fprintf(os.Stderr, "loki: failed to create request: %v\n", err)
		}
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		atomic.AddUint64(&w.flushErrors, 1)
		// Log Loki connection errors (limited to avoid spam)
		if w.flushErrors <= 3 {
			fmt.Fprintf(os.Stderr, "loki: failed to send logs: %v\n", err)
		}
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Check response status and log errors
	if resp.StatusCode >= 400 {
		atomic.AddUint64(&w.flushErrors, 1)
		if w.flushErrors <= 3 {
			fmt.Fprintf(os.Stderr, "loki: server returned status %d\n", resp.StatusCode)
		}
	}
}

// FlushErrors returns the count of flush errors (for monitoring/debugging).
func (w *Writer) FlushErrors() uint64 {
	return atomic.LoadUint64(&w.flushErrors)
}

// SetLabels updates the labels for future log entries.
// This is useful for adding labels that aren't known at initialization time.
func (w *Writer) SetLabels(labels map[string]string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for k, v := range labels {
		w.labels[k] = v
	}
}
