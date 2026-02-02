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
		PublicIP:    "127.0.0.1", // Use localhost with closed port
		PrivateIPs:  []string{"127.0.0.1"},
		SSHPort:     59998, // Port that should not be listening
		Connectable: true,  // Peer can accept incoming connections
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
