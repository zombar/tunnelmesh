package connection

import "fmt"

// State represents the connection state for a peer.
type State int

const (
	// StateDisconnected indicates no active connection to the peer.
	StateDisconnected State = iota

	// StateConnecting indicates an outbound connection attempt is in progress.
	StateConnecting

	// StateConnected indicates an active, healthy connection exists.
	StateConnected

	// StateReconnecting indicates the connection was lost and reconnection is in progress.
	StateReconnecting

	// StateClosed indicates the connection has been explicitly closed (terminal state).
	StateClosed
)

// String returns the string representation of the state.
func (s State) String() string {
	switch s {
	case StateDisconnected:
		return "disconnected"
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	case StateReconnecting:
		return "reconnecting"
	case StateClosed:
		return "closed"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// IsTerminal returns true if this is a terminal state (no further transitions allowed).
func (s State) IsTerminal() bool {
	return s == StateClosed
}

// IsActive returns true if the connection is usable for sending/receiving data.
func (s State) IsActive() bool {
	return s == StateConnected
}

// CanTransitionTo returns true if a transition to the target state is valid.
func (s State) CanTransitionTo(target State) bool {
	if s.IsTerminal() {
		return false // Cannot transition from terminal state
	}

	switch s {
	case StateDisconnected:
		// Can start connecting, receive incoming connection, or be closed
		return target == StateConnecting || target == StateConnected || target == StateClosed

	case StateConnecting:
		// Can succeed (connected), fail (disconnected), or be cancelled (closed)
		return target == StateConnected || target == StateDisconnected || target == StateClosed

	case StateConnected:
		// Can lose connection (reconnecting), be explicitly closed, or disconnect
		return target == StateReconnecting || target == StateDisconnected || target == StateClosed

	case StateReconnecting:
		// Can succeed (connected), give up (disconnected), or be cancelled (closed)
		return target == StateConnected || target == StateDisconnected || target == StateClosed

	default:
		return false
	}
}

// TransitionError is returned when an invalid state transition is attempted.
type TransitionError struct {
	From    State
	To      State
	Peer    string
	Message string
}

func (e *TransitionError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("invalid state transition for peer %s: %s -> %s: %s",
			e.Peer, e.From, e.To, e.Message)
	}
	return fmt.Sprintf("invalid state transition for peer %s: %s -> %s",
		e.Peer, e.From, e.To)
}

// NewTransitionError creates a new transition error.
func NewTransitionError(from, to State, peer, message string) *TransitionError {
	return &TransitionError{
		From:    from,
		To:      to,
		Peer:    peer,
		Message: message,
	}
}
