// Package portmap provides port mapping functionality using PCP, NAT-PMP, and UPnP protocols.
// It manages port mapping lifecycle via a finite state machine (FSM).
package portmap

// State represents the current state of a PortMapper.
type State int

const (
	// StateIdle is the initial state before Start() is called.
	StateIdle State = iota

	// StateDiscovering indicates the mapper is searching for a compatible gateway.
	// This involves probing for PCP, NAT-PMP, and UPnP support.
	StateDiscovering

	// StateRequesting indicates a mapping request has been sent to the gateway.
	StateRequesting

	// StateActive indicates a mapping is active and usable.
	// The mapper will automatically refresh the mapping before expiry.
	StateActive

	// StateRefreshing indicates the mapper is refreshing an existing mapping.
	StateRefreshing

	// StateFailed indicates discovery or mapping failed.
	// The mapper may retry after a delay or on network change.
	StateFailed

	// StateStopped indicates the mapper has been explicitly stopped.
	// This is a terminal state.
	StateStopped
)

// String returns a human-readable representation of the state.
func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateDiscovering:
		return "discovering"
	case StateRequesting:
		return "requesting"
	case StateActive:
		return "active"
	case StateRefreshing:
		return "refreshing"
	case StateFailed:
		return "failed"
	case StateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// IsTerminal returns true if this is a terminal state (stopped).
func (s State) IsTerminal() bool {
	return s == StateStopped
}

// CanTransitionTo returns true if transitioning from this state to the target state is valid.
func (s State) CanTransitionTo(target State) bool {
	switch s {
	case StateIdle:
		// Can only start discovering or be stopped
		return target == StateDiscovering || target == StateStopped

	case StateDiscovering:
		// Can find gateway (requesting), fail, be stopped, or restart on network change
		return target == StateRequesting || target == StateFailed || target == StateStopped || target == StateDiscovering

	case StateRequesting:
		// Can succeed (active), fail, be stopped, or restart on network change
		return target == StateActive || target == StateFailed || target == StateStopped || target == StateDiscovering

	case StateActive:
		// Can need refresh, fail, be stopped, or restart on network change
		return target == StateRefreshing || target == StateFailed || target == StateStopped || target == StateDiscovering

	case StateRefreshing:
		// Can succeed (active), fail, be stopped, or restart on network change
		return target == StateActive || target == StateFailed || target == StateStopped || target == StateDiscovering

	case StateFailed:
		// Can retry (discovering), be stopped, or restart on network change
		return target == StateDiscovering || target == StateStopped

	case StateStopped:
		// Terminal state - no transitions allowed
		return false

	default:
		return false
	}
}

// Protocol identifies the transport protocol for port mapping.
type Protocol int

const (
	// ProtocolUDP requests a UDP port mapping.
	ProtocolUDP Protocol = iota

	// ProtocolTCP requests a TCP port mapping.
	ProtocolTCP
)

// String returns a human-readable representation of the protocol.
func (p Protocol) String() string {
	switch p {
	case ProtocolUDP:
		return "UDP"
	case ProtocolTCP:
		return "TCP"
	default:
		return "unknown"
	}
}
