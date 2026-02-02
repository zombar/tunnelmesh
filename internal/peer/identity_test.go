package peer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

func TestNewPeerIdentity(t *testing.T) {
	cfg := &config.PeerConfig{
		Name:    "test-node",
		SSHPort: 2222,
	}
	pubKeyEncoded := "ssh-ed25519 AAAAC3..."
	resp := &proto.RegisterResponse{
		MeshIP:   "10.99.0.1",
		MeshCIDR: "10.99.0.0/16",
		Domain:   ".tunnelmesh",
	}

	identity := NewPeerIdentity(cfg, pubKeyEncoded, 2223, resp)

	require.NotNil(t, identity)
	assert.Equal(t, "test-node", identity.Name)
	assert.Equal(t, pubKeyEncoded, identity.PubKeyEncoded)
	assert.Equal(t, 2222, identity.SSHPort)
	assert.Equal(t, 2223, identity.UDPPort)
	assert.Equal(t, "10.99.0.1", identity.MeshIP)
	assert.Equal(t, "10.99.0.0/16", identity.MeshCIDR)
	assert.Equal(t, ".tunnelmesh", identity.Domain)
	assert.Same(t, cfg, identity.Config)
}

func TestPeerIdentity_GetLocalIPs(t *testing.T) {
	identity := &PeerIdentity{
		MeshCIDR: "10.99.0.0/16",
	}

	publicIPs, privateIPs, behindNAT := identity.GetLocalIPs()

	// Should return IPs excluding mesh CIDR
	// The actual values depend on the local machine, so just verify the function works
	_ = publicIPs
	_ = privateIPs
	_ = behindNAT
	// No assertion on specific values since they depend on local network config
}

func TestPeerIdentity_GetLocalIPs_ExcludesMeshNetwork(t *testing.T) {
	// This test verifies that mesh IPs are excluded
	identity := &PeerIdentity{
		MeshCIDR: "10.99.0.0/16",
	}

	_, privateIPs, _ := identity.GetLocalIPs()

	// Verify no 10.99.x.x IPs are returned
	for _, ip := range privateIPs {
		assert.NotRegexp(t, `^10\.99\.`, ip, "mesh network IPs should be excluded")
	}
}
