package coord

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"sort"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/coord/web"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

// AdminOverview is the response for the admin overview endpoint.
type AdminOverview struct {
	ServerUptime    string           `json:"server_uptime"`
	TotalPeers      int              `json:"total_peers"`
	OnlinePeers     int              `json:"online_peers"`
	TotalHeartbeats uint64           `json:"total_heartbeats"`
	MeshCIDR        string           `json:"mesh_cidr"`
	DomainSuffix    string           `json:"domain_suffix"`
	Peers           []AdminPeerInfo  `json:"peers"`
	NetworkSettings *networkSettings `json:"network_settings,omitempty"`
}

// AdminPeerInfo contains peer information for the admin UI.
type AdminPeerInfo struct {
	Name                string           `json:"name"`
	MeshIP              string           `json:"mesh_ip"`
	PublicIPs           []string         `json:"public_ips"`
	PrivateIPs          []string         `json:"private_ips"`
	SSHPort             int              `json:"ssh_port"`
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

		peerInfo := AdminPeerInfo{
			Name:           info.peer.Name,
			MeshIP:         info.peer.MeshIP,
			PublicIPs:      info.peer.PublicIPs,
			PrivateIPs:     info.peer.PrivateIPs,
			SSHPort:        info.peer.SSHPort,
			LastSeen:       info.peer.LastSeen,
			Online:         online,
			Connectable:    info.peer.Connectable,
			BehindNAT:      info.peer.BehindNAT,
			RegisteredAt:   info.registeredAt,
			HeartbeatCount: info.heartbeatCount,
			Stats:          info.stats,
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

	// Include network settings
	s.netSettingsMu.RLock()
	settings := s.netSettings
	s.netSettingsMu.RUnlock()
	if settings.ExitNodePeer != "" || len(settings.Exceptions) > 0 {
		overview.NetworkSettings = &settings
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(overview)
}

// handleGetNetworkSettings returns the current network settings.
func (s *Server) handleGetNetworkSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.netSettingsMu.RLock()
	settings := s.netSettings
	s.netSettingsMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(settings)
}

// handleSetNetworkSettings updates the network settings.
func (s *Server) handleSetNetworkSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req networkSettings
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Validate exceptions are valid CIDRs
	for _, cidr := range req.Exceptions {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			s.jsonError(w, "invalid CIDR: "+cidr, http.StatusBadRequest)
			return
		}
	}

	// Validate exit node peer exists (if specified)
	if req.ExitNodePeer != "" {
		s.peersMu.RLock()
		_, exists := s.peers[req.ExitNodePeer]
		s.peersMu.RUnlock()
		if !exists {
			s.jsonError(w, "exit node peer not found: "+req.ExitNodePeer, http.StatusBadRequest)
			return
		}
	}

	s.netSettingsMu.Lock()
	s.netSettings = req
	s.netSettingsMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// setupAdminRoutes registers the admin API routes and static file server.
func (s *Server) setupAdminRoutes() {
	// API endpoints
	s.mux.HandleFunc("/admin/api/overview", s.handleAdminOverview)
	s.mux.HandleFunc("/admin/api/network-settings", s.handleNetworkSettings)

	// Serve embedded static files
	staticFS, _ := fs.Sub(web.Assets, ".")
	fileServer := http.FileServer(http.FS(staticFS))

	// Serve index.html at /admin/ and /admin
	s.mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
	s.mux.Handle("/admin/", http.StripPrefix("/admin/", fileServer))
}

// handleNetworkSettings handles both GET and POST for network settings.
func (s *Server) handleNetworkSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGetNetworkSettings(w, r)
	case http.MethodPost:
		s.handleSetNetworkSettings(w, r)
	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
