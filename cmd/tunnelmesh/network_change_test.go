package main

import (
	"testing"
	"time"
)

func TestNetworkChangeState(t *testing.T) {
	state := newNetworkChangeState()

	// Initially not in bypass window
	if state.inBypassWindow(10 * time.Second) {
		t.Error("expected not in bypass window initially")
	}

	// After recording change, should be in window
	state.recordChange()
	if !state.inBypassWindow(10 * time.Second) {
		t.Error("expected in bypass window after recording change")
	}

	// After window expires, should not be in window
	time.Sleep(15 * time.Millisecond)
	if state.inBypassWindow(10 * time.Millisecond) {
		t.Error("expected not in bypass window after expiry")
	}
}

func TestNetworkChangeStateMultipleRecords(t *testing.T) {
	state := newNetworkChangeState()

	// Record change
	state.recordChange()
	time.Sleep(5 * time.Millisecond)

	// Should still be in a long window
	if !state.inBypassWindow(1 * time.Second) {
		t.Error("expected in bypass window")
	}

	// Record another change (resets timer)
	state.recordChange()

	// Should be in window again with fresh timer
	if !state.inBypassWindow(10 * time.Millisecond) {
		t.Error("expected in bypass window after second record")
	}
}
