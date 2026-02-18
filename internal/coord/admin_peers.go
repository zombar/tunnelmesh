package coord

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
)

// PeerInfo represents peer information for the API.
type PeerInfo struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	PublicKey string   `json:"public_key,omitempty"`
	IsService bool     `json:"is_service"`
	Groups    []string `json:"groups"`
	Expired   bool     `json:"expired"`
	LastSeen  string   `json:"last_seen,omitempty"`
	ExpiresAt string   `json:"expires_at,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
}

// handlePeersMgmt handles GET for listing peers in peer management.
func (s *Server) handlePeersMgmt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.s3SystemStore == nil {
		s.jsonError(w, "peer management not enabled", http.StatusServiceUnavailable)
		return
	}

	peers, err := s.s3SystemStore.LoadPeers(r.Context())
	if err != nil {
		s.jsonError(w, "failed to load peers", http.StatusInternalServerError)
		return
	}

	// Build peer info with groups
	result := make([]PeerInfo, 0, len(peers))
	for _, u := range peers {
		groups := []string{}
		if s.s3Authorizer != nil && s.s3Authorizer.Groups != nil {
			groups = s.s3Authorizer.Groups.GetGroupsForPeer(u.ID)
		}

		info := PeerInfo{
			ID:        u.ID,
			Name:      u.Name,
			IsService: u.IsService(),
			Groups:    groups,
			Expired:   u.IsExpired(),
		}
		if !u.LastSeen.IsZero() {
			info.LastSeen = u.LastSeen.Format(time.RFC3339)
			// Calculate expiration date for human peers (service peers don't auto-expire)
			if !u.IsService() {
				expiresAt := u.LastSeen.Add(time.Duration(auth.GetPeerExpirationDays()) * 24 * time.Hour)
				info.ExpiresAt = expiresAt.Format(time.RFC3339)
			}
		}
		if !u.CreatedAt.IsZero() {
			info.CreatedAt = u.CreatedAt.Format(time.RFC3339)
		}
		result = append(result, info)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// getRequestOwner extracts the owner identity from the request.
// It returns the peer name from the TLS client certificate, or looks up the peer
// by their mesh IP if no certificate is available (browser access from within mesh).
// Returns the peer ID (derived from public key) for RBAC purposes, falling back to peer name
// if peer ID is not available.
func (s *Server) getRequestOwner(r *http.Request) string {
	var peerName string

	// Get peer name from TLS client certificate
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		cn := r.TLS.PeerCertificates[0].Subject.CommonName
		// CN format is "peername.tunnelmesh" - extract just the peer name
		if strings.HasSuffix(cn, ".tunnelmesh") {
			peerName = strings.TrimSuffix(cn, ".tunnelmesh")
		} else {
			peerName = cn
		}
	}

	// No client certificate - try to identify by mesh IP
	// This handles browser access from within the mesh where the browser
	// doesn't have a client certificate but the request originates from a peer
	if peerName == "" {
		peerName = s.getPeerByRemoteAddr(r.RemoteAddr)
	}

	if peerName == "" {
		return ""
	}

	// Look up the peer ID for this peer (derived from their public key)
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()

	if info, ok := s.peers[peerName]; ok && info.peerID != "" {
		return info.peerID
	}

	// Fall back to peer name if peer ID not available
	return peerName
}

// getPeerByRemoteAddr looks up a peer by their mesh IP address.
// Returns the peer name if found, empty string otherwise.
func (s *Server) getPeerByRemoteAddr(remoteAddr string) string {
	// Extract IP from "ip:port" format
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		// Maybe it's just an IP without port
		host = remoteAddr
	}

	s.peersMu.RLock()
	defer s.peersMu.RUnlock()

	for _, info := range s.peers {
		if info.peer.MeshIP == host {
			return info.peer.Name
		}
	}
	return ""
}
