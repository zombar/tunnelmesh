package peer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/coord"
	"github.com/tunnelmesh/tunnelmesh/internal/netmon"
)

func TestMeshNode_RunNetworkMonitor_ContextCancel(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan netmon.Event)

	done := make(chan struct{})
	go func() {
		node.RunNetworkMonitor(ctx, events)
		close(done)
	}()

	// Cancel context
	cancel()

	// Should exit when context is cancelled
	select {
	case <-done:
		// OK - exited properly
	case <-time.After(2 * time.Second):
		t.Fatal("RunNetworkMonitor did not exit on context cancel")
	}
}

func TestMeshNode_RunNetworkMonitor_ChannelClose(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	ctx := context.Background()
	events := make(chan netmon.Event)

	done := make(chan struct{})
	go func() {
		node.RunNetworkMonitor(ctx, events)
		close(done)
	}()

	// Close channel
	close(events)

	// Should exit when channel is closed
	select {
	case <-done:
		// OK - exited properly
	case <-time.After(2 * time.Second):
		t.Fatal("RunNetworkMonitor did not exit on channel close")
	}
}

func TestMeshNode_HandleNetworkChange(t *testing.T) {
	identity := &PeerIdentity{
		Name:          "test-node",
		PubKeyEncoded: "test-key",
		SSHPort:       2222,
		MeshCIDR:      "10.99.0.0/16",
		Config: &config.PeerConfig{
			Name:    "test-node",
			SSHPort: 2222,
		},
	}
	// Client will fail to connect, but we're testing the logic flow
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	// Add a tunnel
	node.TunnelMgr().Add("peer1", newMockTunnel())

	// Verify node is not in bypass window initially
	assert.False(t, node.InNetworkBypassWindow())

	// Handle network change (will fail to re-register but should still update state)
	event := netmon.Event{
		Type:      netmon.ChangeAddressAdded,
		Interface: "en0",
	}
	node.HandleNetworkChange(event)

	// Should now be in bypass window
	assert.True(t, node.InNetworkBypassWindow())

	// Tunnels should be closed
	assert.Empty(t, node.TunnelMgr().List())
}

func TestMeshNode_HandleNetworkChange_RecordsChange(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	// Should not be in bypass window initially
	assert.False(t, node.InNetworkBypassWindow())

	// Handle network change
	event := netmon.Event{
		Type:      netmon.ChangeInterfaceUp,
		Interface: "en0",
	}
	node.HandleNetworkChange(event)

	// Should now be in bypass window
	assert.True(t, node.InNetworkBypassWindow())
}
