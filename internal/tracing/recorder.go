// Package tracing provides runtime trace recording capabilities using Go 1.25's FlightRecorder.
package tracing

import (
	"errors"
	"io"
	"runtime/trace"
	"sync"
	"time"
)

// DefaultBufferSize is the default size of the trace ring buffer (10MB).
const DefaultBufferSize = 10 * 1024 * 1024

var (
	recorder *trace.FlightRecorder
	mu       sync.Mutex
	enabled  bool
)

// ErrNotEnabled is returned when tracing operations are attempted while tracing is disabled.
var ErrNotEnabled = errors.New("tracing not enabled")

// Init initializes the FlightRecorder with the given configuration.
// If enable is false, tracing is disabled and Snapshot will return ErrNotEnabled.
// bufferSize specifies the size of the ring buffer in bytes.
func Init(enable bool, bufferSize int) error {
	mu.Lock()
	defer mu.Unlock()

	if !enable {
		enabled = false
		return nil
	}

	if bufferSize <= 0 {
		bufferSize = DefaultBufferSize
	}

	cfg := trace.FlightRecorderConfig{
		MinAge:   30 * time.Second, // Keep at least 30 seconds of trace data
		MaxBytes: uint64(bufferSize),
	}

	recorder = trace.NewFlightRecorder(cfg)

	if err := recorder.Start(); err != nil {
		return err
	}

	enabled = true
	return nil
}

// Enabled returns true if tracing is currently enabled.
func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return enabled
}

// Snapshot writes the current trace buffer to the given writer.
// The output is compatible with `go tool trace`.
// Returns ErrNotEnabled if tracing is not initialized.
func Snapshot(w io.Writer) error {
	mu.Lock()
	defer mu.Unlock()

	if !enabled || recorder == nil {
		return ErrNotEnabled
	}

	_, err := recorder.WriteTo(w)
	return err
}

// Stop stops the FlightRecorder and releases resources.
// It is safe to call Stop multiple times.
func Stop() {
	mu.Lock()
	defer mu.Unlock()

	if recorder != nil {
		recorder.Stop()
		recorder = nil
	}
	enabled = false
}
