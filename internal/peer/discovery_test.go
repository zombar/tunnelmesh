package peer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/coord"
	"github.com/tunnelmesh/tunnelmesh/internal/transport"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

// mockTunnel is used in tests to simulate a tunnel connection.
// Defined in heartbeat_test.go

func TestMeshNode_buildTransportPeerInfo(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	peer := proto.Peer{
		Name:             "peer1",
		PublicIPs:        []string{"1.2.3.4", "5.6.7.8"},
		PrivateIPs:       []string{"192.168.1.10"},
		SSHPort:          2222,
		UDPPort:          51820,
		MeshIP:           "10.42.0.5",
		Connectable:      true,
		ExternalEndpoint: "1.2.3.4:51820",
	}

	peerInfo := node.buildTransportPeerInfo(peer)

	require.NotNil(t, peerInfo)
	assert.Equal(t, "peer1", peerInfo.Name)
	assert.Equal(t, []string{"1.2.3.4", "5.6.7.8"}, peerInfo.PublicIPs)
	assert.Equal(t, []string{"192.168.1.10"}, peerInfo.PrivateIPs)
	assert.Equal(t, 2222, peerInfo.SSHPort)
	assert.Equal(t, 51820, peerInfo.UDPPort)
	assert.True(t, peerInfo.Connectable)
	assert.Equal(t, "1.2.3.4:51820", peerInfo.ExternalEndpoint)
}

func TestMeshNode_buildTransportPeerInfo_NoPublicIP(t *testing.T) {
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

	peerInfo := node.buildTransportPeerInfo(peer)

	assert.Empty(t, peerInfo.PublicIPs) // Should be empty when no public IPs
	assert.False(t, peerInfo.Connectable)
}

func TestMeshNode_EstablishTunnel_NoTransportNegotiator(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)
	// Don't set a transport negotiator

	peer := proto.Peer{
		Name:       "peer1",
		PrivateIPs: []string{"192.168.1.10"},
		SSHPort:    2222,
	}

	// Should not panic, just return early
	ctx := context.Background()
	node.EstablishTunnel(ctx, peer)
	// No assertions needed - just verify it doesn't panic
}

func TestMeshNode_ConnectingState(t *testing.T) {
	// Test that connecting state tracking prevents duplicate connection attempts
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	// Initially not connecting
	assert.False(t, node.Connections.IsConnecting("peer1"))

	// Start connecting - should succeed
	assert.True(t, node.Connections.StartConnecting("peer1", "10.0.0.1", nil))
	assert.True(t, node.Connections.IsConnecting("peer1"))

	// Try to start connecting again - should fail (already connecting)
	assert.False(t, node.Connections.StartConnecting("peer1", "10.0.0.1", nil))

	// Clear connecting
	node.Connections.ClearConnecting("peer1")
	assert.False(t, node.Connections.IsConnecting("peer1"))

	// Can start connecting again after clearing
	assert.True(t, node.Connections.StartConnecting("peer1", "10.0.0.1", nil))
	assert.True(t, node.Connections.IsConnecting("peer1"))
}

func TestMeshNode_ConnectingState_MultiplePeers(t *testing.T) {
	// Test that connecting state is tracked per-peer
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	// Can connect to multiple peers simultaneously
	assert.True(t, node.Connections.StartConnecting("peer1", "10.0.0.1", nil))
	assert.True(t, node.Connections.StartConnecting("peer2", "10.0.0.2", nil))
	assert.True(t, node.Connections.StartConnecting("peer3", "10.0.0.3", nil))

	// All are connecting
	assert.True(t, node.Connections.IsConnecting("peer1"))
	assert.True(t, node.Connections.IsConnecting("peer2"))
	assert.True(t, node.Connections.IsConnecting("peer3"))

	// Clear one doesn't affect others
	node.Connections.ClearConnecting("peer2")
	assert.True(t, node.Connections.IsConnecting("peer1"))
	assert.False(t, node.Connections.IsConnecting("peer2"))
	assert.True(t, node.Connections.IsConnecting("peer3"))
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

	ctx, cancel := context.WithCancel(context.Background())

	// Should exit when context is cancelled without panicking
	done := make(chan struct{})
	go func() {
		node.RunPeerDiscovery(ctx)
		close(done)
	}()

	// Let the discovery start, then cancel
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// OK - exited properly
	case <-time.After(30 * time.Second):
		t.Fatal("peer discovery loop did not exit on context cancel")
	}
}

func TestMeshNode_RefreshAuthorizedKeys_NoSSHTransport(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)
	node.SSHTransport = nil

	// Should not panic when SSHTransport is nil
	node.RefreshAuthorizedKeys()
}

func TestTransportTypeConstants(t *testing.T) {
	// Verify the transport type constants are what we expect
	assert.Equal(t, "ssh", string(transport.TransportSSH))
	assert.Equal(t, "udp", string(transport.TransportUDP))
	assert.Equal(t, "relay", string(transport.TransportRelay))
	assert.Equal(t, "auto", string(transport.TransportAuto))
}

func TestMeshNode_shouldInitiateConnection(t *testing.T) {
	tests := []struct {
		name           string
		ourKey         string
		peerKey        string
		expectInitiate bool
	}{
		{
			name:           "our key is lower - we initiate",
			ourKey:         "AAAA",
			peerKey:        "BBBB",
			expectInitiate: true,
		},
		{
			name:           "our key is higher - peer initiates",
			ourKey:         "BBBB",
			peerKey:        "AAAA",
			expectInitiate: false,
		},
		{
			name:           "keys are equal - we don't initiate",
			ourKey:         "AAAA",
			peerKey:        "AAAA",
			expectInitiate: false,
		},
		{
			name:           "our key is empty - legacy behavior, we initiate",
			ourKey:         "",
			peerKey:        "BBBB",
			expectInitiate: true,
		},
		{
			name:           "peer key is empty - legacy behavior, we initiate",
			ourKey:         "AAAA",
			peerKey:        "",
			expectInitiate: true,
		},
		{
			name:           "both keys empty - legacy behavior, we initiate",
			ourKey:         "",
			peerKey:        "",
			expectInitiate: true,
		},
		{
			name:           "realistic base64 keys - lower initiates",
			ourKey:         "c3NoLWVkMjU1MTkgQUFBQUMz...",
			peerKey:        "c3NoLWVkMjU1MTkgQkJCQkMz...",
			expectInitiate: true,
		},
		{
			name:           "realistic base64 keys - higher waits",
			ourKey:         "c3NoLWVkMjU1MTkgQkJCQkMz...",
			peerKey:        "c3NoLWVkMjU1MTkgQUFBQUMz...",
			expectInitiate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity := &PeerIdentity{
				Name:          "test-node",
				PubKeyEncoded: tt.ourKey,
				Config: &config.PeerConfig{
					Name: "test-node",
				},
			}
			client := coord.NewClient("http://localhost:8080", "test-token")
			node := NewMeshNode(identity, client)

			peer := proto.Peer{
				Name:      "peer1",
				PublicKey: tt.peerKey,
			}

			result := node.shouldInitiateConnection(peer)
			assert.Equal(t, tt.expectInitiate, result, "shouldInitiateConnection mismatch")
		})
	}
}

func TestMeshNode_EstablishTunnel_RespectsKeyComparison(t *testing.T) {
	// When our key is higher, EstablishTunnel should not initiate
	identity := &PeerIdentity{
		Name:          "test-node",
		PubKeyEncoded: "ZZZZ", // Higher than peer's key
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	peer := proto.Peer{
		Name:      "peer1",
		PublicKey: "AAAA", // Lower key - peer should initiate
	}

	// Should return early without attempting connection
	ctx := context.Background()
	node.EstablishTunnel(ctx, peer)

	// Verify no connection attempt was started
	assert.False(t, node.Connections.IsConnecting("peer1"),
		"should not start connecting when peer has lower pubkey")
}

func TestMeshNode_EstablishTunnelForced_IgnoresKeyComparison(t *testing.T) {
	// EstablishTunnelForced should initiate even when our key is higher
	identity := &PeerIdentity{
		Name:          "test-node",
		PubKeyEncoded: "ZZZZ", // Higher than peer's key
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)
	// No transport negotiator set - will return early after marking connecting

	peer := proto.Peer{
		Name:      "peer1",
		PublicKey: "AAAA", // Lower key - normally peer would initiate
	}

	ctx := context.Background()
	node.EstablishTunnelForced(ctx, peer)

	// With forced initiation, it should attempt to connect (but fail due to no negotiator)
	// The key point is it didn't skip due to pubkey comparison
	// Since there's no transport negotiator, it will log and return, but it did try
}
