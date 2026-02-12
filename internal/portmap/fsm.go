package portmap

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/portmap/client"
)

// Default configuration values.
const (
	DefaultLifetime      = 2 * time.Hour
	DefaultRefreshMargin = 30 * time.Second
	DefaultProbeTimeout  = 5 * time.Second
)

// Config holds configuration for a PortMapper.
type Config struct {
	// Protocol is the transport protocol to map (UDP or TCP).
	Protocol Protocol

	// LocalPort is the local port to map.
	LocalPort int

	// Client is the port mapping client to use.
	// If nil, a default multi-protocol client will be created.
	Client client.Client

	// Observers are notified of state changes.
	Observers []Observer

	// Lifetime is the requested mapping lifetime. Default: 2 hours.
	Lifetime time.Duration

	// RefreshMargin is how early to refresh before expiry. Default: 30 seconds.
	RefreshMargin time.Duration

	// ProbeTimeout is the timeout for gateway discovery. Default: 5 seconds.
	ProbeTimeout time.Duration
}

// PortMapper manages port mapping lifecycle via a finite state machine.
type PortMapper struct {
	mu sync.RWMutex

	// Configuration (immutable after creation)
	config Config

	// State
	state     State
	lastError error

	// Active mapping (when StateActive)
	mapping *Mapping

	// Client (from config or created)
	pmClient client.Client
	gateway  net.IP

	// Observers
	observers []Observer

	// Refresh timer
	refreshTimer *time.Timer

	// Lifecycle
	ctx         context.Context
	cancel      context.CancelFunc
	runningOnce sync.Once
	stoppedOnce sync.Once

	// Channels for state machine
	networkChangeCh chan struct{}
	stopCh          chan struct{}
}

// New creates a new PortMapper with the given configuration.
func New(cfg Config) *PortMapper {
	// Apply defaults
	if cfg.Lifetime == 0 {
		cfg.Lifetime = DefaultLifetime
	}
	if cfg.RefreshMargin == 0 {
		cfg.RefreshMargin = DefaultRefreshMargin
	}
	if cfg.ProbeTimeout == 0 {
		cfg.ProbeTimeout = DefaultProbeTimeout
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &PortMapper{
		config:          cfg,
		state:           StateIdle,
		pmClient:        cfg.Client,
		observers:       cfg.Observers,
		ctx:             ctx,
		cancel:          cancel,
		networkChangeCh: make(chan struct{}, 1),
		stopCh:          make(chan struct{}),
	}
}

// State returns the current state of the port mapper.
func (pm *PortMapper) State() State {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.state
}

// Mapping returns the current active mapping, or nil if not active.
func (pm *PortMapper) Mapping() *Mapping {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if pm.mapping == nil {
		return nil
	}
	// Return a copy to prevent external modification
	m := *pm.mapping
	return &m
}

// IsActive returns true if there is an active, usable mapping.
func (pm *PortMapper) IsActive() bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.state == StateActive && pm.mapping != nil
}

// LastError returns the last error that occurred, if any.
func (pm *PortMapper) LastError() error {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.lastError
}

// Start begins the port mapping process.
// The state machine will run in a background goroutine.
func (pm *PortMapper) Start() error {
	var alreadyRunning bool
	pm.runningOnce.Do(func() {
		go pm.run()
	})
	if alreadyRunning {
		return errors.New("port mapper already started")
	}
	return nil
}

// Stop stops the port mapper and releases any active mapping.
func (pm *PortMapper) Stop() error {
	pm.stoppedOnce.Do(func() {
		close(pm.stopCh)
		pm.cancel()
	})

	// Wait for run loop to exit
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Clean up mapping if active
	if pm.mapping != nil && pm.pmClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = pm.pmClient.DeleteMapping(ctx, pm.clientMapping())
		cancel()

		pm.notifyMappingLost(errors.New("stopped"))
		pm.mapping = nil
	}

	if pm.refreshTimer != nil {
		pm.refreshTimer.Stop()
		pm.refreshTimer = nil
	}

	pm.setState(StateStopped)
	return nil
}

// NetworkChanged signals that the network configuration has changed.
// This will cause the mapper to restart discovery.
func (pm *PortMapper) NetworkChanged() {
	select {
	case pm.networkChangeCh <- struct{}{}:
	default:
		// Already a network change pending
	}
}

// run is the main state machine loop.
func (pm *PortMapper) run() {
	for {
		// Check for stop
		select {
		case <-pm.stopCh:
			return
		default:
		}

		// Check for network change (non-blocking)
		select {
		case <-pm.networkChangeCh:
			pm.handleNetworkChange()
			continue
		default:
		}

		pm.mu.RLock()
		state := pm.state
		pm.mu.RUnlock()

		switch state {
		case StateIdle:
			pm.handleIdle()
		case StateDiscovering:
			pm.handleDiscovering()
		case StateRequesting:
			pm.handleRequesting()
		case StateActive:
			pm.handleActive()
		case StateRefreshing:
			pm.handleRefreshing()
		case StateFailed:
			pm.handleFailed()
		case StateStopped:
			return
		}
	}
}

func (pm *PortMapper) handleIdle() {
	pm.transitionTo(StateDiscovering)
}

func (pm *PortMapper) handleDiscovering() {
	if pm.pmClient == nil {
		pm.mu.Lock()
		pm.lastError = errors.New("no client configured")
		pm.mu.Unlock()
		pm.transitionTo(StateFailed)
		return
	}

	ctx, cancel := context.WithTimeout(pm.ctx, pm.config.ProbeTimeout)
	defer cancel()

	gateway, err := pm.pmClient.Probe(ctx)
	if err != nil {
		pm.mu.Lock()
		pm.lastError = err
		pm.mu.Unlock()

		log.Debug().Err(err).Msg("portmap: gateway discovery failed")
		pm.transitionTo(StateFailed)
		return
	}

	pm.mu.Lock()
	pm.gateway = gateway
	pm.mu.Unlock()

	log.Debug().Str("gateway", gateway.String()).Msg("portmap: gateway discovered")
	pm.transitionTo(StateRequesting)
}

func (pm *PortMapper) handleRequesting() {
	ctx, cancel := context.WithTimeout(pm.ctx, pm.config.ProbeTimeout)
	defer cancel()

	protocol := client.UDP
	if pm.config.Protocol == ProtocolTCP {
		protocol = client.TCP
	}

	mapping, err := pm.pmClient.RequestMapping(ctx, protocol, pm.config.LocalPort, pm.config.Lifetime)
	if err != nil {
		pm.mu.Lock()
		pm.lastError = err
		pm.mu.Unlock()

		log.Debug().Err(err).Msg("portmap: mapping request failed")
		pm.transitionTo(StateFailed)
		return
	}

	now := time.Now()
	pm.mu.Lock()
	pm.mapping = &Mapping{
		Protocol:     pm.config.Protocol,
		InternalPort: mapping.InternalPort,
		ExternalPort: mapping.ExternalPort,
		ExternalIP:   mapping.ExternalIP,
		Gateway:      mapping.Gateway,
		ClientType:   pm.pmClient.Name(),
		Lifetime:     mapping.Lifetime,
		CreatedAt:    now,
		ExpiresAt:    now.Add(mapping.Lifetime),
	}
	pm.lastError = nil
	pm.mu.Unlock()

	log.Info().
		Str("external", pm.mapping.ExternalAddr()).
		Str("client", pm.pmClient.Name()).
		Dur("lifetime", mapping.Lifetime).
		Msg("portmap: mapping acquired")

	pm.notifyMappingAcquired()
	pm.transitionTo(StateActive)
}

func (pm *PortMapper) handleActive() {
	pm.mu.RLock()
	mapping := pm.mapping
	pm.mu.RUnlock()

	if mapping == nil {
		pm.transitionTo(StateFailed)
		return
	}

	// Calculate when to refresh
	refreshIn := mapping.TimeToExpiry() - pm.config.RefreshMargin
	if refreshIn < 0 {
		refreshIn = 0
	}

	// Set up refresh timer
	pm.mu.Lock()
	if pm.refreshTimer != nil {
		pm.refreshTimer.Stop()
	}
	pm.refreshTimer = time.NewTimer(refreshIn)
	timer := pm.refreshTimer
	pm.mu.Unlock()

	select {
	case <-pm.stopCh:
		return
	case <-pm.networkChangeCh:
		timer.Stop()
		pm.handleNetworkChange()
		return
	case <-timer.C:
		pm.transitionTo(StateRefreshing)
	}
}

func (pm *PortMapper) handleRefreshing() {
	ctx, cancel := context.WithTimeout(pm.ctx, pm.config.ProbeTimeout)
	defer cancel()

	pm.mu.RLock()
	oldMapping := pm.clientMapping()
	pm.mu.RUnlock()

	if oldMapping == nil {
		pm.transitionTo(StateFailed)
		return
	}

	newMapping, err := pm.pmClient.RefreshMapping(ctx, oldMapping)
	if err != nil {
		pm.mu.Lock()
		pm.lastError = err
		pm.mu.Unlock()

		log.Warn().Err(err).Msg("portmap: refresh failed")
		pm.notifyMappingLost(err)

		pm.mu.Lock()
		pm.mapping = nil
		pm.mu.Unlock()

		// Try to re-establish
		pm.transitionTo(StateDiscovering)
		return
	}

	now := time.Now()
	pm.mu.Lock()
	pm.mapping = &Mapping{
		Protocol:     pm.config.Protocol,
		InternalPort: newMapping.InternalPort,
		ExternalPort: newMapping.ExternalPort,
		ExternalIP:   newMapping.ExternalIP,
		Gateway:      newMapping.Gateway,
		ClientType:   pm.pmClient.Name(),
		Lifetime:     newMapping.Lifetime,
		CreatedAt:    pm.mapping.CreatedAt, // Keep original creation time
		ExpiresAt:    now.Add(newMapping.Lifetime),
	}
	pm.mu.Unlock()

	log.Debug().
		Str("external", pm.mapping.ExternalAddr()).
		Dur("lifetime", newMapping.Lifetime).
		Msg("portmap: mapping refreshed")

	pm.notifyMappingAcquired()
	pm.transitionTo(StateActive)
}

func (pm *PortMapper) handleFailed() {
	// Wait for stop, network change, or retry
	select {
	case <-pm.stopCh:
		return
	case <-pm.networkChangeCh:
		pm.transitionTo(StateDiscovering)
	case <-time.After(30 * time.Second):
		// Retry after delay
		pm.transitionTo(StateDiscovering)
	}
}

func (pm *PortMapper) handleNetworkChange() {
	pm.mu.Lock()
	hadMapping := pm.mapping != nil
	pm.mapping = nil
	pm.gateway = nil
	pm.mu.Unlock()

	log.Debug().Msg("portmap: network changed, restarting discovery")

	if hadMapping {
		pm.notifyMappingLost(errors.New("network changed"))
	}

	pm.transitionTo(StateDiscovering)
}

// transitionTo changes state and notifies observers.
func (pm *PortMapper) transitionTo(newState State) {
	pm.mu.Lock()
	oldState := pm.state
	// If already stopped, ignore transition attempts from background goroutines
	if oldState == StateStopped {
		pm.mu.Unlock()
		return
	}
	if oldState == newState {
		pm.mu.Unlock()
		return
	}
	if !oldState.CanTransitionTo(newState) {
		pm.mu.Unlock()
		log.Warn().
			Str("from", oldState.String()).
			Str("to", newState.String()).
			Msg("portmap: invalid state transition")
		return
	}
	pm.state = newState
	pm.mu.Unlock()

	log.Debug().
		Str("from", oldState.String()).
		Str("to", newState.String()).
		Msg("portmap: state transition")

	// Notify observers
	for _, obs := range pm.observers {
		obs.OnPortMapStateChanged(pm, oldState, newState)
	}
}

// setState sets state without transition validation (for Stop).
func (pm *PortMapper) setState(newState State) {
	oldState := pm.state
	if oldState == newState {
		return
	}
	pm.state = newState

	// Notify observers (lock already held)
	for _, obs := range pm.observers {
		obs.OnPortMapStateChanged(pm, oldState, newState)
	}
}

// clientMapping converts our Mapping to client.Mapping.
func (pm *PortMapper) clientMapping() *client.Mapping {
	if pm.mapping == nil {
		return nil
	}
	protocol := client.UDP
	if pm.mapping.Protocol == ProtocolTCP {
		protocol = client.TCP
	}
	return &client.Mapping{
		Protocol:     protocol,
		InternalPort: pm.mapping.InternalPort,
		ExternalPort: pm.mapping.ExternalPort,
		ExternalIP:   pm.mapping.ExternalIP,
		Gateway:      pm.mapping.Gateway,
		Lifetime:     pm.mapping.Lifetime,
	}
}

func (pm *PortMapper) notifyMappingAcquired() {
	pm.mu.RLock()
	mapping := pm.mapping
	pm.mu.RUnlock()

	if mapping == nil {
		return
	}

	for _, obs := range pm.observers {
		obs.OnMappingAcquired(pm, mapping)
	}
}

func (pm *PortMapper) notifyMappingLost(reason error) {
	for _, obs := range pm.observers {
		obs.OnMappingLost(pm, reason)
	}
}
