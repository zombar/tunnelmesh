package portmap

// Observer is notified of port mapping state changes.
// Implementations must be safe for concurrent use.
type Observer interface {
	// OnPortMapStateChanged is called when the port mapper transitions between states.
	OnPortMapStateChanged(pm *PortMapper, oldState, newState State)

	// OnMappingAcquired is called when a port mapping is successfully acquired or refreshed.
	// The mapping parameter contains the details of the active mapping.
	OnMappingAcquired(pm *PortMapper, mapping *Mapping)

	// OnMappingLost is called when a previously active mapping is lost.
	// This can happen due to refresh failure, network change, or explicit stop.
	// The reason parameter contains the error that caused the loss, if any.
	OnMappingLost(pm *PortMapper, reason error)
}

// NoOpObserver is an Observer that does nothing.
// Useful for testing or as a default.
type NoOpObserver struct{}

func (NoOpObserver) OnPortMapStateChanged(*PortMapper, State, State) {}
func (NoOpObserver) OnMappingAcquired(*PortMapper, *Mapping)         {}
func (NoOpObserver) OnMappingLost(*PortMapper, error)                {}

// FuncObserver wraps callback functions into an Observer.
type FuncObserver struct {
	StateChanged    func(pm *PortMapper, oldState, newState State)
	MappingAcquired func(pm *PortMapper, mapping *Mapping)
	MappingLost     func(pm *PortMapper, reason error)
}

func (f FuncObserver) OnPortMapStateChanged(pm *PortMapper, oldState, newState State) {
	if f.StateChanged != nil {
		f.StateChanged(pm, oldState, newState)
	}
}

func (f FuncObserver) OnMappingAcquired(pm *PortMapper, mapping *Mapping) {
	if f.MappingAcquired != nil {
		f.MappingAcquired(pm, mapping)
	}
}

func (f FuncObserver) OnMappingLost(pm *PortMapper, reason error) {
	if f.MappingLost != nil {
		f.MappingLost(pm, reason)
	}
}
