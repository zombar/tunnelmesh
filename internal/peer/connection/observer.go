package connection

import "time"

// Transition represents a state change event.
type Transition struct {
	// PeerName is the name of the peer whose state changed.
	PeerName string

	// From is the previous state.
	From State

	// To is the new state.
	To State

	// Timestamp is when the transition occurred.
	Timestamp time.Time

	// Reason is a human-readable description of why the transition occurred.
	Reason string

	// Error is non-nil if the transition was caused by an error.
	Error error
}

// Observer receives notifications about state transitions.
// Implementations should not block as notifications are delivered synchronously.
type Observer interface {
	// OnTransition is called when a peer's connection state changes.
	// This is called synchronously while holding the FSM lock, so implementations
	// should not block or call back into the FSM.
	OnTransition(t Transition)
}

// ObserverFunc is an adapter that allows using ordinary functions as Observers.
type ObserverFunc func(Transition)

// OnTransition implements the Observer interface.
func (f ObserverFunc) OnTransition(t Transition) {
	f(t)
}

// MultiObserver combines multiple observers into one.
type MultiObserver struct {
	observers []Observer
}

// NewMultiObserver creates a new MultiObserver with the given observers.
func NewMultiObserver(observers ...Observer) *MultiObserver {
	return &MultiObserver{
		observers: observers,
	}
}

// Add adds an observer to the multi-observer.
func (m *MultiObserver) Add(o Observer) {
	m.observers = append(m.observers, o)
}

// OnTransition notifies all observers of the transition.
func (m *MultiObserver) OnTransition(t Transition) {
	for _, o := range m.observers {
		o.OnTransition(t)
	}
}

// LoggingObserver logs all state transitions.
type LoggingObserver struct{}

// OnTransition logs the transition using zerolog.
func (l *LoggingObserver) OnTransition(t Transition) {
	// Import zerolog in the actual implementation
	// For now, this is a placeholder that will be connected to zerolog
}
