package negotiate

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStrategy_String(t *testing.T) {
	tests := []struct {
		s    Strategy
		want string
	}{
		{StrategyDirect, "direct"},
		{StrategyReverse, "reverse"},
		{StrategyRelay, "relay"},
		{Strategy(99), "unknown"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.s.String())
	}
}

func TestPeerInfo_BestAddress(t *testing.T) {
	info := &PeerInfo{
		ID:         "peer1",
		PublicIP:   "1.2.3.4",
		PrivateIPs: []string{"192.168.1.10", "10.0.0.5"},
		SSHPort:    2222,
	}

	// Best address should be first private IP (LAN preferred over public)
	assert.Equal(t, "192.168.1.10:2222", info.BestAddress())

	// Without private IPs, should use public
	info.PrivateIPs = nil
	assert.Equal(t, "1.2.3.4:2222", info.BestAddress())

	// No addresses available
	info.PublicIP = ""
	assert.Equal(t, "", info.BestAddress())
}

func TestProbeResult(t *testing.T) {
	result := &ProbeResult{
		Address:   "1.2.3.4:2222",
		Reachable: true,
		Latency:   50 * time.Millisecond,
		Strategy:  StrategyDirect,
	}

	assert.True(t, result.Reachable)
	assert.Equal(t, StrategyDirect, result.Strategy)
}

func TestNegotiator_ProbeAddress(t *testing.T) {
	// Start a test TCP listener
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	neg := NewNegotiator(Config{
		ProbeTimeout:   1 * time.Second,
		MaxRetries:     1,
		RetryDelay:     100 * time.Millisecond,
		AllowReverse:   true,
		PreferredOrder: []Strategy{StrategyDirect, StrategyReverse},
	})

	ctx := context.Background()
	addr := listener.Addr().String()

	// Test reachable address
	result := neg.ProbeAddress(ctx, addr)
	assert.True(t, result.Reachable)
	assert.Equal(t, addr, result.Address)
	assert.GreaterOrEqual(t, result.Latency, time.Duration(0))

	// Test unreachable address
	result = neg.ProbeAddress(ctx, "127.0.0.1:59999")
	assert.False(t, result.Reachable)
}

func TestNegotiator_SelectStrategy(t *testing.T) {
	neg := NewNegotiator(Config{
		ProbeTimeout:   500 * time.Millisecond,
		MaxRetries:     1,
		RetryDelay:     100 * time.Millisecond,
		AllowReverse:   true,
		PreferredOrder: []Strategy{StrategyDirect, StrategyReverse},
	})

	tests := []struct {
		name      string
		probes    []ProbeResult
		wantStrat Strategy
		wantAddr  string
		wantOk    bool
	}{
		{
			name: "direct available",
			probes: []ProbeResult{
				{Address: "1.2.3.4:2222", Reachable: true, Strategy: StrategyDirect, Latency: 10 * time.Millisecond},
			},
			wantStrat: StrategyDirect,
			wantAddr:  "1.2.3.4:2222",
			wantOk:    true,
		},
		{
			name: "prefer lower latency",
			probes: []ProbeResult{
				{Address: "1.2.3.4:2222", Reachable: true, Strategy: StrategyDirect, Latency: 100 * time.Millisecond},
				{Address: "192.168.1.10:2222", Reachable: true, Strategy: StrategyDirect, Latency: 10 * time.Millisecond},
			},
			wantStrat: StrategyDirect,
			wantAddr:  "192.168.1.10:2222",
			wantOk:    true,
		},
		{
			name: "no reachable addresses",
			probes: []ProbeResult{
				{Address: "1.2.3.4:2222", Reachable: false},
				{Address: "192.168.1.10:2222", Reachable: false},
			},
			wantOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := neg.SelectStrategy(tt.probes)
			assert.Equal(t, tt.wantOk, ok)
			if ok {
				assert.Equal(t, tt.wantStrat, result.Strategy)
				assert.Equal(t, tt.wantAddr, result.Address)
			}
		})
	}
}

func TestNegotiator_NegotiateFallbackReverse(t *testing.T) {
	neg := NewNegotiator(Config{
		ProbeTimeout:   200 * time.Millisecond,
		MaxRetries:     1,
		RetryDelay:     50 * time.Millisecond,
		AllowReverse:   true,
		AllowRelay:     false,
		PreferredOrder: []Strategy{StrategyDirect, StrategyReverse},
	})

	ctx := context.Background()
	peer := &PeerInfo{
		ID:          "peer1",
		PublicIP:    "192.0.2.1", // TEST-NET address, not routable
		PrivateIPs:  []string{"10.0.0.99"},
		SSHPort:     2222,
		Connectable: true, // Peer can accept incoming connections
	}

	// When no direct connection works and peer is connectable, should recommend reverse
	result, err := neg.Negotiate(ctx, peer)
	require.NoError(t, err)
	assert.Equal(t, StrategyReverse, result.Strategy)
}

func TestNegotiator_NegotiateFallbackRelay(t *testing.T) {
	neg := NewNegotiator(Config{
		ProbeTimeout:   200 * time.Millisecond,
		MaxRetries:     1,
		RetryDelay:     50 * time.Millisecond,
		AllowReverse:   true,
		AllowRelay:     true,
		PreferredOrder: []Strategy{StrategyDirect, StrategyReverse},
	})

	ctx := context.Background()
	peer := &PeerInfo{
		ID:          "peer1",
		PublicIP:    "192.0.2.1", // TEST-NET address, not routable
		PrivateIPs:  []string{"10.0.0.99"},
		SSHPort:     2222,
		Connectable: false, // Peer cannot accept incoming connections (behind NAT)
	}

	// When no direct connection works AND peer is not connectable, should use relay
	result, err := neg.Negotiate(ctx, peer)
	require.NoError(t, err)
	assert.Equal(t, StrategyRelay, result.Strategy)
}

func TestNegotiator_NegotiateBothBehindNAT(t *testing.T) {
	neg := NewNegotiator(Config{
		ProbeTimeout:   200 * time.Millisecond,
		MaxRetries:     1,
		RetryDelay:     50 * time.Millisecond,
		AllowReverse:   true,
		AllowRelay:     true,
	})

	ctx := context.Background()
	peer := &PeerInfo{
		ID:          "peer1",
		PublicIP:    "", // No public IP
		PrivateIPs:  []string{"10.0.0.99"},
		SSHPort:     2222,
		Connectable: false, // Both sides behind NAT
	}

	// Both behind NAT - should fall back to relay
	result, err := neg.Negotiate(ctx, peer)
	require.NoError(t, err)
	assert.Equal(t, StrategyRelay, result.Strategy)
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid config",
			cfg: Config{
				ProbeTimeout:   1 * time.Second,
				MaxRetries:     3,
				RetryDelay:     100 * time.Millisecond,
				AllowReverse:   true,
				PreferredOrder: []Strategy{StrategyDirect},
			},
			wantErr: false,
		},
		{
			name: "zero timeout uses default",
			cfg: Config{
				MaxRetries:     3,
				PreferredOrder: []Strategy{StrategyDirect},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNegotiator_SetExceptionCIDRs(t *testing.T) {
	neg := NewNegotiator(Config{
		ProbeTimeout: 500 * time.Millisecond,
	})

	// Valid CIDRs
	err := neg.SetExceptionCIDRs([]string{"192.168.0.0/16", "10.0.0.0/8", "100.64.0.0/10"})
	require.NoError(t, err)

	neg.exceptionMu.RLock()
	assert.Len(t, neg.exceptionCIDRs, 3)
	neg.exceptionMu.RUnlock()
}

func TestNegotiator_SetExceptionCIDRs_Invalid(t *testing.T) {
	neg := NewNegotiator(Config{
		ProbeTimeout: 500 * time.Millisecond,
	})

	// Invalid CIDR
	err := neg.SetExceptionCIDRs([]string{"192.168.0.0/16", "not-a-cidr"})
	require.Error(t, err)
}

func TestNegotiator_isExcepted(t *testing.T) {
	neg := NewNegotiator(Config{
		ProbeTimeout: 500 * time.Millisecond,
	})

	err := neg.SetExceptionCIDRs([]string{"192.168.0.0/16", "100.64.0.0/10"})
	require.NoError(t, err)

	tests := []struct {
		ip       string
		excepted bool
	}{
		{"192.168.1.1", true},
		{"192.168.254.254", true},
		{"100.64.0.1", true},
		{"100.127.255.255", true},
		{"8.8.8.8", false},
		{"10.0.0.1", false},
		{"172.16.0.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			result := neg.isExcepted(tt.ip)
			assert.Equal(t, tt.excepted, result, "IP %s expected excepted=%v", tt.ip, tt.excepted)
		})
	}
}

func TestNegotiator_filterAddresses(t *testing.T) {
	neg := NewNegotiator(Config{
		ProbeTimeout: 500 * time.Millisecond,
	})

	// Set exceptions
	err := neg.SetExceptionCIDRs([]string{"100.64.0.0/10"})
	require.NoError(t, err)

	addrs := []string{
		"8.8.8.8:2222",
		"100.64.1.1:2222",  // Tailscale - should be filtered
		"192.168.1.1:2222",
		"100.127.0.1:2222", // Tailscale - should be filtered
	}

	filtered := neg.filterAddresses(addrs)

	assert.Len(t, filtered, 2)
	assert.Contains(t, filtered, "8.8.8.8:2222")
	assert.Contains(t, filtered, "192.168.1.1:2222")
	assert.NotContains(t, filtered, "100.64.1.1:2222")
	assert.NotContains(t, filtered, "100.127.0.1:2222")
}

func TestNegotiator_filterAddresses_NoExceptions(t *testing.T) {
	neg := NewNegotiator(Config{
		ProbeTimeout: 500 * time.Millisecond,
	})

	// No exceptions set
	addrs := []string{
		"8.8.8.8:2222",
		"100.64.1.1:2222",
	}

	filtered := neg.filterAddresses(addrs)

	// Should return all addresses unchanged
	assert.Equal(t, addrs, filtered)
}

func TestNegotiator_ProbeAll_WithExceptions(t *testing.T) {
	neg := NewNegotiator(Config{
		ProbeTimeout: 100 * time.Millisecond,
	})

	// Set exception for 100.64.0.0/10 (Tailscale)
	err := neg.SetExceptionCIDRs([]string{"100.64.0.0/10"})
	require.NoError(t, err)

	peer := &PeerInfo{
		ID:         "peer1",
		PublicIP:   "8.8.8.8",              // Will fail to connect but won't be filtered
		PrivateIPs: []string{"100.64.1.1"}, // Tailscale IP - should be filtered
		SSHPort:    2222,
	}

	ctx := context.Background()
	results := neg.ProbeAll(ctx, peer)

	// Should only have probed one address (the public IP)
	// The Tailscale IP should have been filtered out
	assert.Len(t, results, 1)
	assert.Equal(t, "8.8.8.8:2222", results[0].Address)
	// It won't be reachable but the important thing is the filtering
	assert.False(t, results[0].Reachable)
}

func TestPeerInfo_AllAddresses(t *testing.T) {
	info := &PeerInfo{
		ID:         "peer1",
		PublicIP:   "1.2.3.4",
		PrivateIPs: []string{"192.168.1.10", "10.0.0.5"},
		SSHPort:    2222,
	}

	// Private IPs should come first (LAN preferred)
	addrs := info.AllAddresses()
	assert.Len(t, addrs, 3)
	assert.Equal(t, "192.168.1.10:2222", addrs[0])
	assert.Equal(t, "10.0.0.5:2222", addrs[1])
	assert.Equal(t, "1.2.3.4:2222", addrs[2])
}
