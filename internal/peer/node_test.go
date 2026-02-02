package peer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/coord"
)

func TestNewMeshNode(t *testing.T) {
	identity := &PeerIdentity{
		Name:          "test-node",
		PubKeyEncoded: "test-key",
		SSHPort:       2222,
		MeshCIDR:      "10.99.0.0/16",
		MeshIP:        "10.99.0.1",
		Config: &config.PeerConfig{
			Name:    "test-node",
			SSHPort: 2222,
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")

	node := NewMeshNode(identity, client)

	require.NotNil(t, node)
	assert.Same(t, identity, node.Identity())
	assert.Same(t, client, node.Client())
	assert.NotNil(t, node.TunnelMgr())
	assert.NotNil(t, node.Router())
}

func TestMeshNode_TriggerDiscovery(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	// Should be able to trigger discovery
	triggered := node.TriggerDiscovery()
	assert.True(t, triggered)

	// Channel should have one item
	select {
	case <-node.DiscoveryChan():
		// OK - received the trigger
	default:
		t.Fatal("discovery channel should have a pending trigger")
	}
}

func TestMeshNode_TriggerDiscovery_AlreadyPending(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	// First trigger should succeed
	triggered1 := node.TriggerDiscovery()
	assert.True(t, triggered1)

	// Second trigger should return false (already pending)
	triggered2 := node.TriggerDiscovery()
	assert.False(t, triggered2)
}

func TestMeshNode_NetworkChangeState(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	// Initially not in bypass window
	assert.False(t, node.InNetworkBypassWindow())

	// Record network change
	node.RecordNetworkChange()

	// Should now be in bypass window
	assert.True(t, node.InNetworkBypassWindow())
}

func TestMeshNode_HeartbeatState(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	// Initial state should be empty
	pub, priv, nat := node.GetHeartbeatIPs()
	assert.Nil(t, pub)
	assert.Nil(t, priv)
	assert.False(t, nat)

	// Update state
	node.SetHeartbeatIPs([]string{"1.2.3.4"}, []string{"192.168.1.1"}, true)

	// Should return updated values
	pub, priv, nat = node.GetHeartbeatIPs()
	assert.Equal(t, []string{"1.2.3.4"}, pub)
	assert.Equal(t, []string{"192.168.1.1"}, priv)
	assert.True(t, nat)
}

func TestMeshNode_CollectStats(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	// Add some tunnels
	node.TunnelMgr().Add("peer1", newMockTunnel())
	node.TunnelMgr().Add("peer2", newMockTunnel())

	// Without forwarder, should still return stats with tunnel count
	stats := node.CollectStats()

	// Stats from nil forwarder should have zero values but correct tunnel count
	require.NotNil(t, stats)
	assert.Equal(t, 2, stats.ActiveTunnels)
}

func TestMeshNode_IPsChanged(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	// Initially should detect change (no last IPs set)
	assert.True(t, node.IPsChanged([]string{"1.2.3.4"}, []string{"192.168.1.1"}, false))

	// Set heartbeat IPs
	node.SetHeartbeatIPs([]string{"1.2.3.4"}, []string{"192.168.1.1"}, false)

	// Same IPs should not show change
	assert.False(t, node.IPsChanged([]string{"1.2.3.4"}, []string{"192.168.1.1"}, false))

	// Different public IPs should show change
	assert.True(t, node.IPsChanged([]string{"5.6.7.8"}, []string{"192.168.1.1"}, false))

	// Different private IPs should show change
	assert.True(t, node.IPsChanged([]string{"1.2.3.4"}, []string{"10.0.0.1"}, false))

	// Different NAT status should show change
	assert.True(t, node.IPsChanged([]string{"1.2.3.4"}, []string{"192.168.1.1"}, true))
}
