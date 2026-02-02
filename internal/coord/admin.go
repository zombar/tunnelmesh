package coord

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/coord/web"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

// AdminOverview is the response for the admin overview endpoint.
type AdminOverview struct {
	ServerUptime    string          `json:"server_uptime"`
	ServerVersion   string          `json:"server_version"`
	TotalPeers      int             `json:"total_peers"`
	OnlinePeers     int             `json:"online_peers"`
	TotalHeartbeats uint64          `json:"total_heartbeats"`
	MeshCIDR        string          `json:"mesh_cidr"`
	DomainSuffix    string          `json:"domain_suffix"`
	Peers           []AdminPeerInfo `json:"peers"`
}

// AdminPeerInfo contains peer information for the admin UI.
type AdminPeerInfo struct {
	Name                string           `json:"name"`
	MeshIP              string           `json:"mesh_ip"`
	PublicIPs           []string         `json:"public_ips"`
	PrivateIPs          []string         `json:"private_ips"`
	SSHPort             int              `json:"ssh_port"`
	UDPPort             int              `json:"udp_port"`
	UDPExternalAddr4    string           `json:"udp_external_addr4,omitempty"`
	UDPExternalAddr6    string           `json:"udp_external_addr6,omitempty"`
	LastSeen            time.Time        `json:"last_seen"`
	Online              bool             `json:"online"`
	Connectable         bool             `json:"connectable"`
	BehindNAT           bool             `json:"behind_nat"`
	RegisteredAt        time.Time        `json:"registered_at"`
	HeartbeatCount      uint64           `json:"heartbeat_count"`
	Stats               *proto.PeerStats `json:"stats,omitempty"`
	BytesSentRate       float64          `json:"bytes_sent_rate"`
	BytesReceivedRate   float64          `json:"bytes_received_rate"`
	PacketsSentRate     float64          `json:"packets_sent_rate"`
	PacketsReceivedRate float64          `json:"packets_received_rate"`
	PreferredTransport  string           `json:"preferred_transport"`
	Version             string           `json:"version,omitempty"`
}

// handleAdminOverview returns the admin overview data.
func (s *Server) handleAdminOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.peersMu.RLock()
	defer s.peersMu.RUnlock()

	now := time.Now()
	onlineThreshold := 2 * time.Minute

	overview := AdminOverview{
		ServerUptime:    time.Since(s.serverStats.startTime).Round(time.Second).String(),
		ServerVersion:   s.version,
		TotalPeers:      len(s.peers),
		TotalHeartbeats: s.serverStats.totalHeartbeats,
		MeshCIDR:        s.cfg.MeshCIDR,
		DomainSuffix:    s.cfg.DomainSuffix,
		Peers:           make([]AdminPeerInfo, 0, len(s.peers)),
	}

	for _, info := range s.peers {
		online := now.Sub(info.peer.LastSeen) < onlineThreshold
		if online {
			overview.OnlinePeers++
		}

		preferredTransport := info.preferredTransport
		if preferredTransport == "" {
			preferredTransport = "auto"
		}

		peerInfo := AdminPeerInfo{
			Name:               info.peer.Name,
			MeshIP:             info.peer.MeshIP,
			PublicIPs:          info.peer.PublicIPs,
			PrivateIPs:         info.peer.PrivateIPs,
			SSHPort:            info.peer.SSHPort,
			UDPPort:            info.peer.UDPPort,
			LastSeen:           info.peer.LastSeen,
			Online:             online,
			Connectable:        info.peer.Connectable,
			BehindNAT:          info.peer.BehindNAT,
			RegisteredAt:       info.registeredAt,
			HeartbeatCount:     info.heartbeatCount,
			Stats:              info.stats,
			PreferredTransport: preferredTransport,
			Version:            info.peer.Version,
		}

		// Get UDP endpoint addresses if available
		if s.holePunch != nil {
			if ep, ok := s.holePunch.GetEndpoint(info.peer.Name); ok {
				peerInfo.UDPExternalAddr4 = ep.ExternalAddr4
				peerInfo.UDPExternalAddr6 = ep.ExternalAddr6
			}
		}

		// Calculate rates if we have previous stats
		if info.prevStats != nil && info.stats != nil && !info.lastStatsTime.IsZero() {
			// Rate is calculated as delta over 30 seconds (heartbeat interval)
			peerInfo.BytesSentRate = float64(info.stats.BytesSent-info.prevStats.BytesSent) / 30.0
			peerInfo.BytesReceivedRate = float64(info.stats.BytesReceived-info.prevStats.BytesReceived) / 30.0
			peerInfo.PacketsSentRate = float64(info.stats.PacketsSent-info.prevStats.PacketsSent) / 30.0
			peerInfo.PacketsReceivedRate = float64(info.stats.PacketsReceived-info.prevStats.PacketsReceived) / 30.0
		}

		overview.Peers = append(overview.Peers, peerInfo)
	}

	// Sort peers by mesh IP for consistent ordering
	sort.Slice(overview.Peers, func(i, j int) bool {
		ipI := net.ParseIP(overview.Peers[i].MeshIP)
		ipJ := net.ParseIP(overview.Peers[j].MeshIP)
		if ipI == nil || ipJ == nil {
			return overview.Peers[i].MeshIP < overview.Peers[j].MeshIP
		}
		return bytes.Compare(ipI.To16(), ipJ.To16()) < 0
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(overview)
}

// TransportRequest is the request body for setting transport preference.
type TransportRequest struct {
	Preferred string `json:"preferred"`
}

// handleSetTransport sets the preferred transport for a peer.
func (s *Server) handleSetTransport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract peer name from path: /admin/api/peers/{name}/transport
	path := r.URL.Path
	parts := strings.Split(strings.TrimPrefix(path, "/admin/api/peers/"), "/")
	if len(parts) < 2 || parts[1] != "transport" {
		s.jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	peerName := parts[0]

	var req TransportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Validate transport type
	validTransports := map[string]bool{"auto": true, "udp": true, "ssh": true, "relay": true}
	if !validTransports[req.Preferred] {
		s.jsonError(w, "invalid transport type", http.StatusBadRequest)
		return
	}

	s.peersMu.Lock()
	info, exists := s.peers[peerName]
	if exists {
		info.preferredTransport = req.Preferred
	}
	s.peersMu.Unlock()

	if !exists {
		s.jsonError(w, "peer not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleReconnect signals a peer to reconnect.
func (s *Server) handleReconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract peer name from path: /admin/api/peers/{name}/reconnect
	path := r.URL.Path
	parts := strings.Split(strings.TrimPrefix(path, "/admin/api/peers/"), "/")
	if len(parts) < 2 || parts[1] != "reconnect" {
		s.jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	peerName := parts[0]

	s.peersMu.Lock()
	info, exists := s.peers[peerName]
	if exists {
		info.reconnectRequested = true
	}
	s.peersMu.Unlock()

	if !exists {
		s.jsonError(w, "peer not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":      true,
		"message": "Reconnect requested for " + peerName,
	})
}

// setupAdminRoutes registers the admin API routes and static file server.
func (s *Server) setupAdminRoutes() {
	// API endpoints
	s.mux.HandleFunc("/admin/api/overview", s.handleAdminOverview)
	s.mux.HandleFunc("/admin/api/peers/", s.handleAdminPeerAction)

	// Serve embedded static files
	staticFS, _ := fs.Sub(web.Assets, ".")
	fileServer := http.FileServer(http.FS(staticFS))

	// Serve index.html at /admin/ and /admin
	s.mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
	s.mux.Handle("/admin/", http.StripPrefix("/admin/", fileServer))
}

// handleAdminPeerAction routes peer-specific admin actions.
func (s *Server) handleAdminPeerAction(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if strings.HasSuffix(path, "/transport") {
		s.handleSetTransport(w, r)
	} else if strings.HasSuffix(path, "/reconnect") {
		s.handleReconnect(w, r)
	} else {
		s.jsonError(w, "unknown action", http.StatusNotFound)
	}
}
