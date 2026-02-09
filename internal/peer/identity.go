// Package peer provides the core peer functionality for tunnelmesh.
package peer

import (
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

// PeerIdentity holds the immutable identity of this peer in the mesh.
// This information is established at startup and doesn't change during runtime.
// nolint:revive // PeerIdentity name kept for clarity despite stuttering
type PeerIdentity struct {
	Name          string
	PubKeyEncoded string
	SSHPort       int
	UDPPort       int
	MeshCIDR      string
	MeshIP        string
	Domain        string
	Version       string
	Config        *config.PeerConfig
	Location      *proto.GeoLocation // Geographic location (manual or IP-derived)
}

// NewPeerIdentity creates a PeerIdentity from config and registration response.
func NewPeerIdentity(cfg *config.PeerConfig, pubKeyEncoded string, udpPort int, version string, resp *proto.RegisterResponse) *PeerIdentity {
	// Use coordinator-assigned name if different from config (handles auto-suffix for duplicates)
	name := cfg.Name
	if resp.PeerName != "" && resp.PeerName != cfg.Name {
		name = resp.PeerName
	}

	return &PeerIdentity{
		Name:          name,
		PubKeyEncoded: pubKeyEncoded,
		SSHPort:       cfg.SSHPort,
		UDPPort:       udpPort,
		MeshCIDR:      resp.MeshCIDR,
		MeshIP:        resp.MeshIP,
		Domain:        resp.Domain,
		Version:       version,
		Config:        cfg,
	}
}

// GetLocalIPs returns current local IP addresses excluding the mesh network.
func (p *PeerIdentity) GetLocalIPs() (publicIPs, privateIPs []string, behindNAT bool) {
	return proto.GetLocalIPsExcluding(p.MeshCIDR)
}
