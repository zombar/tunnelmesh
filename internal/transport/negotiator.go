package transport

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// NegotiatorConfig holds negotiator settings.
type NegotiatorConfig struct {
	ProbeTimeout      time.Duration
	ConnectionTimeout time.Duration
	MaxRetries        int
	RetryDelay        time.Duration
	ParallelProbes    int
	EnableFallback    bool
}

// DefaultNegotiatorConfig returns sensible defaults.
func DefaultNegotiatorConfig() NegotiatorConfig {
	return NegotiatorConfig{
		ProbeTimeout:      5 * time.Second,
		ConnectionTimeout: 30 * time.Second,
		MaxRetries:        3,
		RetryDelay:        time.Second,
		ParallelProbes:    3,
		EnableFallback:    true,
	}
}

// NegotiationResult contains the result of transport negotiation.
type NegotiationResult struct {
	Transport  TransportType
	Connection Connection
	Latency    time.Duration
	Upgradable bool // Can upgrade to better transport later
}

// ProbeResult contains the result of probing a transport.
type ProbeResult struct {
	Type    TransportType
	Latency time.Duration
	Error   error
}

// Negotiator handles transport selection and connection establishment.
type Negotiator struct {
	registry *Registry
	config   NegotiatorConfig
}

// NewNegotiator creates a new transport negotiator.
func NewNegotiator(registry *Registry, config NegotiatorConfig) *Negotiator {
	return &Negotiator{
		registry: registry,
		config:   config,
	}
}

// Negotiate selects and establishes the best transport for a peer.
func (n *Negotiator) Negotiate(ctx context.Context, peerInfo *PeerInfo, dialOpts DialOptions) (*NegotiationResult, error) {
	order := n.registry.GetPreferredOrder(peerInfo.Name)
	if len(order) == 0 {
		return nil, fmt.Errorf("no transports available for peer %s", peerInfo.Name)
	}

	log.Debug().
		Str("peer", peerInfo.Name).
		Interface("order", order).
		Msg("negotiating transport")

	// Try each transport in preference order
	var lastErr error
	for _, transportType := range order {
		transport, ok := n.registry.Get(transportType)
		if !ok {
			log.Debug().
				Str("peer", peerInfo.Name).
				Str("transport", string(transportType)).
				Msg("transport not registered, skipping")
			continue
		}

		// Set up dial options
		opts := dialOpts
		opts.PeerInfo = peerInfo
		opts.PeerName = peerInfo.Name
		if opts.Timeout == 0 {
			opts.Timeout = n.config.ConnectionTimeout
		}

		// Try to dial with retries
		for attempt := 0; attempt <= n.config.MaxRetries; attempt++ {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(n.config.RetryDelay):
				}
			}

			log.Debug().
				Str("peer", peerInfo.Name).
				Str("transport", string(transportType)).
				Int("attempt", attempt+1).
				Msg("attempting connection")

			conn, err := transport.Dial(ctx, opts)
			if err == nil {
				log.Info().
					Str("peer", peerInfo.Name).
					Str("transport", string(transportType)).
					Msg("connection established")

				return &NegotiationResult{
					Transport:  transportType,
					Connection: conn,
					Upgradable: n.canUpgrade(transportType, order),
				}, nil
			}

			lastErr = err
			log.Debug().
				Err(err).
				Str("peer", peerInfo.Name).
				Str("transport", string(transportType)).
				Int("attempt", attempt+1).
				Msg("connection attempt failed")
		}

		if !n.config.EnableFallback {
			break
		}
	}

	return nil, fmt.Errorf("all transports failed for peer %s: %w", peerInfo.Name, lastErr)
}

// canUpgrade checks if we can upgrade from the current transport.
func (n *Negotiator) canUpgrade(current TransportType, order []TransportType) bool {
	for _, t := range order {
		if t == current {
			return false // Current is already the preferred
		}
		if _, ok := n.registry.Get(t); ok {
			return true // A higher-priority transport exists
		}
	}
	return false
}

// ProbeAll tests all applicable transports for a peer in parallel.
func (n *Negotiator) ProbeAll(ctx context.Context, peerInfo *PeerInfo) []ProbeResult {
	order := n.registry.GetPreferredOrder(peerInfo.Name)
	if len(order) == 0 {
		return nil
	}

	// Limit parallel probes
	maxProbes := n.config.ParallelProbes
	if maxProbes > len(order) {
		maxProbes = len(order)
	}

	results := make([]ProbeResult, len(order))
	var wg sync.WaitGroup

	// Create a semaphore to limit parallelism
	sem := make(chan struct{}, maxProbes)

	for i, transportType := range order {
		transport, ok := n.registry.Get(transportType)
		if !ok {
			results[i] = ProbeResult{
				Type:  transportType,
				Error: fmt.Errorf("transport not registered"),
			}
			continue
		}

		wg.Add(1)
		go func(idx int, tt TransportType, t Transport) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			probeCtx, cancel := context.WithTimeout(ctx, n.config.ProbeTimeout)
			defer cancel()

			latency, err := t.Probe(probeCtx, ProbeOptions{
				PeerInfo: peerInfo,
				Timeout:  n.config.ProbeTimeout,
			})

			results[idx] = ProbeResult{
				Type:    tt,
				Latency: latency,
				Error:   err,
			}
		}(i, transportType, transport)
	}

	wg.Wait()
	return results
}

// SelectBest selects the best available transport based on probe results.
func (n *Negotiator) SelectBest(results []ProbeResult, order []TransportType) TransportType {
	// Build a map for O(1) lookup
	resultMap := make(map[TransportType]ProbeResult, len(results))
	for _, r := range results {
		resultMap[r.Type] = r
	}

	// Return the first successful transport in preference order
	for _, t := range order {
		if r, ok := resultMap[t]; ok && r.Error == nil {
			return t
		}
	}

	return TransportRelay // Fallback to relay
}

// DirectUpgradeResult is sent when a direct connection succeeds after initial relay.
type DirectUpgradeResult struct {
	Connection Connection
	Transport  TransportType
	Error      error
}

// NegotiateRelayFirst returns immediately with relay, then races direct transports in background.
// This is DERP-like behavior: instant connectivity via relay, upgrade to direct when available.
// The upgradeChan receives a result when a direct connection succeeds (or all fail).
func (n *Negotiator) NegotiateRelayFirst(ctx context.Context, peerInfo *PeerInfo, dialOpts DialOptions, upgradeChan chan<- DirectUpgradeResult) (*NegotiationResult, error) {
	order := n.registry.GetPreferredOrder(peerInfo.Name)
	if len(order) == 0 {
		return nil, fmt.Errorf("no transports available for peer %s", peerInfo.Name)
	}

	log.Debug().
		Str("peer", peerInfo.Name).
		Interface("order", order).
		Msg("relay-first negotiation starting")

	// Separate relay from direct transports
	var directTransports []TransportType
	for _, t := range order {
		if t != TransportRelay {
			directTransports = append(directTransports, t)
		}
	}

	// Start background direct connection attempts
	if len(directTransports) > 0 && upgradeChan != nil {
		go n.raceDirectTransports(ctx, peerInfo, dialOpts, directTransports, upgradeChan)
	}

	// Return relay as the immediate result
	return &NegotiationResult{
		Transport:  TransportRelay,
		Connection: nil, // Caller will use persistent relay
		Upgradable: len(directTransports) > 0,
	}, nil
}

// raceDirectTransports tries direct transports in order and sends result to upgradeChan.
func (n *Negotiator) raceDirectTransports(ctx context.Context, peerInfo *PeerInfo, dialOpts DialOptions, transports []TransportType, upgradeChan chan<- DirectUpgradeResult) {
	defer close(upgradeChan)

	log.Debug().
		Str("peer", peerInfo.Name).
		Interface("transports", transports).
		Msg("starting direct transport race")

	var lastErr error
	for _, transportType := range transports {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			upgradeChan <- DirectUpgradeResult{Error: ctx.Err()}
			return
		default:
		}

		transport, ok := n.registry.Get(transportType)
		if !ok {
			continue
		}

		opts := dialOpts
		opts.PeerInfo = peerInfo
		opts.PeerName = peerInfo.Name
		if opts.Timeout == 0 {
			opts.Timeout = n.config.ConnectionTimeout
		}

		// Try with retries (but fewer than normal since we have relay working)
		maxRetries := 2 // Reduced from normal since relay is working
		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					upgradeChan <- DirectUpgradeResult{Error: ctx.Err()}
					return
				case <-time.After(n.config.RetryDelay):
				}
			}

			log.Debug().
				Str("peer", peerInfo.Name).
				Str("transport", string(transportType)).
				Int("attempt", attempt+1).
				Msg("attempting direct connection (relay-first mode)")

			conn, err := transport.Dial(ctx, opts)
			if err == nil {
				log.Info().
					Str("peer", peerInfo.Name).
					Str("transport", string(transportType)).
					Msg("direct connection established, upgrade available")

				upgradeChan <- DirectUpgradeResult{
					Connection: conn,
					Transport:  transportType,
				}
				return
			}

			lastErr = err
			log.Debug().
				Err(err).
				Str("peer", peerInfo.Name).
				Str("transport", string(transportType)).
				Int("attempt", attempt+1).
				Msg("direct connection attempt failed")
		}

		if !n.config.EnableFallback {
			break
		}
	}

	// All direct transports failed - relay continues working
	log.Debug().
		Str("peer", peerInfo.Name).
		Msg("all direct transports failed, continuing with relay")

	upgradeChan <- DirectUpgradeResult{Error: lastErr}
}
