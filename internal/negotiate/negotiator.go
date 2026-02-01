// Package negotiate handles connection negotiation between peers.
package negotiate

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/rs/zerolog/log"
)

// Strategy represents a connection strategy.
type Strategy int

const (
	// StrategyDirect means connecting directly to the peer.
	StrategyDirect Strategy = iota
	// StrategyReverse means the peer should connect to us.
	StrategyReverse
	// StrategyRelay means using a relay server (not implemented).
	StrategyRelay
)

// String returns a string representation of the strategy.
func (s Strategy) String() string {
	switch s {
	case StrategyDirect:
		return "direct"
	case StrategyReverse:
		return "reverse"
	case StrategyRelay:
		return "relay"
	default:
		return "unknown"
	}
}

// PeerInfo contains information about a peer needed for negotiation.
type PeerInfo struct {
	ID         string
	PublicIP   string
	PrivateIPs []string
	SSHPort    int
}

// BestAddress returns the best address to try for this peer.
func (p *PeerInfo) BestAddress() string {
	if p.PublicIP != "" {
		return fmt.Sprintf("%s:%d", p.PublicIP, p.SSHPort)
	}
	if len(p.PrivateIPs) > 0 {
		return fmt.Sprintf("%s:%d", p.PrivateIPs[0], p.SSHPort)
	}
	return ""
}

// AllAddresses returns all possible addresses for this peer.
func (p *PeerInfo) AllAddresses() []string {
	var addrs []string
	if p.PublicIP != "" {
		addrs = append(addrs, fmt.Sprintf("%s:%d", p.PublicIP, p.SSHPort))
	}
	for _, ip := range p.PrivateIPs {
		addrs = append(addrs, fmt.Sprintf("%s:%d", ip, p.SSHPort))
	}
	return addrs
}

// ProbeResult contains the result of a connectivity probe.
type ProbeResult struct {
	Address   string
	Reachable bool
	Latency   time.Duration
	Strategy  Strategy
	Error     error
}

// NegotiationResult contains the final negotiation result.
type NegotiationResult struct {
	Strategy Strategy
	Address  string
	Latency  time.Duration
}

// Config holds negotiator configuration.
type Config struct {
	ProbeTimeout   time.Duration
	MaxRetries     int
	RetryDelay     time.Duration
	AllowReverse   bool
	PreferredOrder []Strategy
}

// Validate checks if the configuration is valid and applies defaults.
func (c *Config) Validate() error {
	if c.ProbeTimeout == 0 {
		c.ProbeTimeout = 5 * time.Second
	}
	if c.MaxRetries == 0 {
		c.MaxRetries = 3
	}
	if c.RetryDelay == 0 {
		c.RetryDelay = 500 * time.Millisecond
	}
	if len(c.PreferredOrder) == 0 {
		c.PreferredOrder = []Strategy{StrategyDirect, StrategyReverse}
	}
	return nil
}

// Negotiator handles connection negotiation.
type Negotiator struct {
	config Config
}

// NewNegotiator creates a new Negotiator with the given config.
func NewNegotiator(cfg Config) *Negotiator {
	cfg.Validate()
	return &Negotiator{config: cfg}
}

// ProbeAddress tests if an address is reachable.
func (n *Negotiator) ProbeAddress(ctx context.Context, addr string) ProbeResult {
	result := ProbeResult{
		Address:  addr,
		Strategy: StrategyDirect,
	}

	start := time.Now()

	dialer := &net.Dialer{
		Timeout: n.config.ProbeTimeout,
	}

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		result.Error = err
		result.Reachable = false
		log.Debug().
			Str("addr", addr).
			Err(err).
			Msg("probe failed")
		return result
	}
	defer conn.Close()

	result.Latency = time.Since(start)
	result.Reachable = true

	log.Debug().
		Str("addr", addr).
		Dur("latency", result.Latency).
		Msg("probe succeeded")

	return result
}

// ProbeAll probes all addresses for a peer.
func (n *Negotiator) ProbeAll(ctx context.Context, peer *PeerInfo) []ProbeResult {
	addrs := peer.AllAddresses()
	results := make([]ProbeResult, 0, len(addrs))

	for _, addr := range addrs {
		result := n.ProbeAddress(ctx, addr)
		results = append(results, result)
	}

	return results
}

// SelectStrategy selects the best connection strategy from probe results.
func (n *Negotiator) SelectStrategy(probes []ProbeResult) (ProbeResult, bool) {
	var best *ProbeResult

	for i := range probes {
		probe := &probes[i]
		if !probe.Reachable {
			continue
		}

		if best == nil || probe.Latency < best.Latency {
			best = probe
		}
	}

	if best == nil {
		return ProbeResult{}, false
	}

	return *best, true
}

// Negotiate determines the best way to connect to a peer.
func (n *Negotiator) Negotiate(ctx context.Context, peer *PeerInfo) (*NegotiationResult, error) {
	log.Info().
		Str("peer", peer.ID).
		Str("public_ip", peer.PublicIP).
		Strs("private_ips", peer.PrivateIPs).
		Msg("negotiating connection")

	// Try direct connection first
	probes := n.ProbeAll(ctx, peer)

	if best, ok := n.SelectStrategy(probes); ok {
		log.Info().
			Str("peer", peer.ID).
			Str("strategy", best.Strategy.String()).
			Str("address", best.Address).
			Dur("latency", best.Latency).
			Msg("selected direct connection")

		return &NegotiationResult{
			Strategy: best.Strategy,
			Address:  best.Address,
			Latency:  best.Latency,
		}, nil
	}

	// If direct fails and reverse is allowed, recommend reverse
	if n.config.AllowReverse {
		log.Info().
			Str("peer", peer.ID).
			Msg("direct connection failed, recommending reverse tunnel")

		return &NegotiationResult{
			Strategy: StrategyReverse,
		}, nil
	}

	return nil, fmt.Errorf("no connectivity to peer %s", peer.ID)
}

// NegotiateWithRetry negotiates with retry logic.
func (n *Negotiator) NegotiateWithRetry(ctx context.Context, peer *PeerInfo) (*NegotiationResult, error) {
	var lastErr error

	for i := 0; i < n.config.MaxRetries; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(n.config.RetryDelay):
			}
		}

		result, err := n.Negotiate(ctx, peer)
		if err == nil {
			return result, nil
		}
		lastErr = err

		log.Debug().
			Str("peer", peer.ID).
			Int("attempt", i+1).
			Err(err).
			Msg("negotiation attempt failed")
	}

	return nil, fmt.Errorf("negotiation failed after %d attempts: %w", n.config.MaxRetries, lastErr)
}

// RequestReverse sends a request for the peer to initiate a reverse connection.
// This is called when direct connection fails.
type ReverseRequest struct {
	FromPeerID string
	ToPeerID   string
	ListenAddr string
}

// ReverseHandler is called when a reverse connection request is received.
type ReverseHandler func(req *ReverseRequest) error
