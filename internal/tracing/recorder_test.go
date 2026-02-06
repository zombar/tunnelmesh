package tracing

import (
	"bytes"
	"testing"
)

func TestInit_Disabled(t *testing.T) {
	// Clean state
	Stop()

	err := Init(false, 0)
	if err != nil {
		t.Fatalf("Init(false) failed: %v", err)
	}

	if Enabled() {
		t.Error("Enabled() should return false when disabled")
	}

	// Snapshot should return error when disabled
	var buf bytes.Buffer
	if err := Snapshot(&buf); err != ErrNotEnabled {
		t.Errorf("Snapshot() = %v, want ErrNotEnabled", err)
	}
}

func TestInit_Enabled(t *testing.T) {
	// Clean state
	Stop()

	err := Init(true, DefaultBufferSize)
	if err != nil {
		t.Fatalf("Init(true) failed: %v", err)
	}
	defer Stop()

	if !Enabled() {
		t.Error("Enabled() should return true when enabled")
	}

	// Snapshot should succeed
	var buf bytes.Buffer
	if err := Snapshot(&buf); err != nil {
		t.Errorf("Snapshot() failed: %v", err)
	}

	// Should have written something
	if buf.Len() == 0 {
		t.Error("Snapshot() wrote nothing")
	}
}

func TestInit_DefaultBufferSize(t *testing.T) {
	// Clean state
	Stop()

	// Pass 0 for buffer size, should use default
	err := Init(true, 0)
	if err != nil {
		t.Fatalf("Init(true, 0) failed: %v", err)
	}
	defer Stop()

	if !Enabled() {
		t.Error("Enabled() should return true")
	}
}

func TestStop_Multiple(t *testing.T) {
	// Clean state
	Stop()

	err := Init(true, DefaultBufferSize)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Stop should be safe to call multiple times
	Stop()
	Stop()
	Stop()

	if Enabled() {
		t.Error("Enabled() should return false after Stop")
	}
}

func TestSnapshot_AfterStop(t *testing.T) {
	// Clean state
	Stop()

	err := Init(true, DefaultBufferSize)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	Stop()

	// Snapshot after stop should return error
	var buf bytes.Buffer
	if err := Snapshot(&buf); err != ErrNotEnabled {
		t.Errorf("Snapshot() after Stop = %v, want ErrNotEnabled", err)
	}
}
