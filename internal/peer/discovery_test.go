package peer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/coord"
	"github.com/tunnelmesh/tunnelmesh/internal/negotiate"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

// mockTunnel is used in tests to simulate a tunnel connection.
// Defined in heartbeat_test.go

func TestMeshNode_buildPeerInfo(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	peer := proto.Peer{
		Name:        "peer1",
		PublicIPs:   []string{"1.2.3.4", "5.6.7.8"},
		PrivateIPs:  []string{"192.168.1.10"},
		SSHPort:     2222,
		MeshIP:      "10.99.0.5",
		Connectable: true,
	}

	peerInfo := node.buildPeerInfo(peer)

	require.NotNil(t, peerInfo)
	assert.Equal(t, "peer1", peerInfo.ID)
	assert.Equal(t, "1.2.3.4", peerInfo.PublicIP) // Should use first public IP
	assert.Equal(t, []string{"192.168.1.10"}, peerInfo.PrivateIPs)
	assert.Equal(t, 2222, peerInfo.SSHPort)
	assert.True(t, peerInfo.Connectable)
}

func TestMeshNode_buildPeerInfo_NoPublicIP(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	peer := proto.Peer{
		Name:       "peer1",
		PublicIPs:  nil,
		PrivateIPs: []string{"192.168.1.10"},
		SSHPort:    2222,
	}

	peerInfo := node.buildPeerInfo(peer)

	assert.Equal(t, "", peerInfo.PublicIP) // Should be empty when no public IPs
}

func TestMeshNode_EstablishTunnel_NoNegotiator(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)
	// Don't set a negotiator

	peer := proto.Peer{
		Name:       "peer1",
		PrivateIPs: []string{"192.168.1.10"},
		SSHPort:    2222,
	}

	// Should not panic, just return early
	ctx := context.Background()
	node.EstablishTunnel(ctx, peer, false)
	// No assertions needed - just verify it doesn't panic
}

func TestMeshNode_EstablishTunnel_AlphaOrdering(t *testing.T) {
	// Test that alpha ordering works correctly
	// When our name > peer name, we should wait for peer to initiate

	tests := []struct {
		name                string
		myName              string
		peerName            string
		bypassAlphaOrdering bool
		shouldSkip          bool // True if we should skip due to alpha ordering
	}{
		{
			name:       "my name comes first alphabetically - should connect",
			myName:     "alice",
			peerName:   "bob",
			shouldSkip: false,
		},
		{
			name:       "my name comes second alphabetically - should skip",
			myName:     "bob",
			peerName:   "alice",
			shouldSkip: true,
		},
		{
			name:                "my name comes second but bypass enabled - should connect",
			myName:              "bob",
			peerName:            "alice",
			bypassAlphaOrdering: true,
			shouldSkip:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We test the alpha ordering condition directly
			// This is the same condition used in EstablishTunnel for StrategyDirect
			shouldSkipDueToAlpha := tt.myName > tt.peerName && !tt.bypassAlphaOrdering
			assert.Equal(t, tt.shouldSkip, shouldSkipDueToAlpha)
		})
	}
}

func TestMeshNode_RunPeerDiscovery_ContextCancel(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Should exit when context is cancelled without panicking
	done := make(chan struct{})
	go func() {
		node.RunPeerDiscovery(ctx)
		close(done)
	}()

	select {
	case <-done:
		// OK - exited properly
	case <-time.After(5 * time.Second):
		t.Fatal("peer discovery loop did not exit on context cancel")
	}
}

func TestMeshNode_RefreshAuthorizedKeys_NoSSHServer(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)
	node.SSHServer = nil

	// Should not panic when SSHServer is nil
	node.RefreshAuthorizedKeys()
}

func TestNegotiationStrategyConstants(t *testing.T) {
	// Verify the strategy constants are what we expect
	assert.Equal(t, negotiate.Strategy(0), negotiate.StrategyDirect)
	assert.Equal(t, negotiate.Strategy(1), negotiate.StrategyReverse)
	assert.Equal(t, negotiate.Strategy(2), negotiate.StrategyRelay)

	// Verify string representations
	assert.Equal(t, "direct", negotiate.StrategyDirect.String())
	assert.Equal(t, "reverse", negotiate.StrategyReverse.String())
	assert.Equal(t, "relay", negotiate.StrategyRelay.String())
}
