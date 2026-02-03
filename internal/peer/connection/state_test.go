package connection

import (
	"testing"
)

func TestState_String(t *testing.T) {
	tests := []struct {
		state    State
		expected string
	}{
		{StateDisconnected, "disconnected"},
		{StateConnecting, "connecting"},
		{StateConnected, "connected"},
		{StateReconnecting, "reconnecting"},
		{StateClosed, "closed"},
		{State(99), "unknown(99)"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.state.String(); got != tt.expected {
				t.Errorf("State.String() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestState_IsTerminal(t *testing.T) {
	tests := []struct {
		state    State
		terminal bool
	}{
		{StateDisconnected, false},
		{StateConnecting, false},
		{StateConnected, false},
		{StateReconnecting, false},
		{StateClosed, true},
	}

	for _, tt := range tests {
		t.Run(tt.state.String(), func(t *testing.T) {
			if got := tt.state.IsTerminal(); got != tt.terminal {
				t.Errorf("State.IsTerminal() = %v, want %v", got, tt.terminal)
			}
		})
	}
}

func TestState_IsActive(t *testing.T) {
	tests := []struct {
		state  State
		active bool
	}{
		{StateDisconnected, false},
		{StateConnecting, false},
		{StateConnected, true},
		{StateReconnecting, false},
		{StateClosed, false},
	}

	for _, tt := range tests {
		t.Run(tt.state.String(), func(t *testing.T) {
			if got := tt.state.IsActive(); got != tt.active {
				t.Errorf("State.IsActive() = %v, want %v", got, tt.active)
			}
		})
	}
}

func TestState_CanTransitionTo(t *testing.T) {
	// Valid transitions
	validTransitions := []struct {
		from State
		to   State
	}{
		// From Disconnected
		{StateDisconnected, StateConnecting},
		{StateDisconnected, StateConnected},
		{StateDisconnected, StateClosed},

		// From Connecting
		{StateConnecting, StateConnected},
		{StateConnecting, StateDisconnected},
		{StateConnecting, StateClosed},

		// From Connected
		{StateConnected, StateReconnecting},
		{StateConnected, StateDisconnected},
		{StateConnected, StateClosed},

		// From Reconnecting
		{StateReconnecting, StateConnected},
		{StateReconnecting, StateDisconnected},
		{StateReconnecting, StateClosed},
	}

	for _, tt := range validTransitions {
		t.Run(tt.from.String()+"->"+tt.to.String(), func(t *testing.T) {
			if !tt.from.CanTransitionTo(tt.to) {
				t.Errorf("%s.CanTransitionTo(%s) = false, want true", tt.from, tt.to)
			}
		})
	}

	// Invalid transitions
	invalidTransitions := []struct {
		from State
		to   State
	}{
		// From Disconnected - cannot go to Reconnecting
		{StateDisconnected, StateReconnecting},

		// From Connecting - cannot go to Reconnecting
		{StateConnecting, StateReconnecting},

		// From Connected - cannot go to Connecting
		{StateConnected, StateConnecting},

		// From Reconnecting - cannot go to Connecting
		{StateReconnecting, StateConnecting},

		// From Closed - cannot go anywhere
		{StateClosed, StateDisconnected},
		{StateClosed, StateConnecting},
		{StateClosed, StateConnected},
		{StateClosed, StateReconnecting},
		{StateClosed, StateClosed},

		// Self-transitions (not allowed)
		{StateDisconnected, StateDisconnected},
		{StateConnecting, StateConnecting},
		{StateConnected, StateConnected},
		{StateReconnecting, StateReconnecting},
	}

	for _, tt := range invalidTransitions {
		t.Run(tt.from.String()+"->"+tt.to.String()+"_invalid", func(t *testing.T) {
			if tt.from.CanTransitionTo(tt.to) {
				t.Errorf("%s.CanTransitionTo(%s) = true, want false", tt.from, tt.to)
			}
		})
	}
}

func TestTransitionError(t *testing.T) {
	err := NewTransitionError(StateConnected, StateConnecting, "peer1", "")
	expected := "invalid state transition for peer peer1: connected -> connecting"
	if err.Error() != expected {
		t.Errorf("TransitionError.Error() = %q, want %q", err.Error(), expected)
	}

	errWithMsg := NewTransitionError(StateConnected, StateConnecting, "peer1", "not allowed")
	expectedWithMsg := "invalid state transition for peer peer1: connected -> connecting: not allowed"
	if errWithMsg.Error() != expectedWithMsg {
		t.Errorf("TransitionError.Error() = %q, want %q", errWithMsg.Error(), expectedWithMsg)
	}
}
