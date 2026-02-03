package coord

import (
	"bytes"
	"encoding/json"
	"io"
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

		peerInfo := AdminPeerInfo{
			Name:           info.peer.Name,
			MeshIP:         info.peer.MeshIP,
			PublicIPs:      info.peer.PublicIPs,
			PrivateIPs:     info.peer.PrivateIPs,
			SSHPort:        info.peer.SSHPort,
			UDPPort:        info.peer.UDPPort,
			LastSeen:       info.peer.LastSeen,
			Online:         online,
			Connectable:    info.peer.Connectable,
			BehindNAT:      info.peer.BehindNAT,
			RegisteredAt:   info.registeredAt,
			HeartbeatCount: info.heartbeatCount,
			Stats:          info.stats,
			Version:        info.peer.Version,
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

// setupAdminRoutes registers the admin API routes and static file server.
func (s *Server) setupAdminRoutes() {
	// API endpoints
	s.mux.HandleFunc("/admin/api/overview", s.handleAdminOverview)

	// WireGuard client management endpoints (if enabled)
	if s.cfg.WireGuard.Enabled {
		s.mux.HandleFunc("/admin/api/wireguard/clients", s.handleWGClients)
		s.mux.HandleFunc("/admin/api/wireguard/clients/", s.handleWGClientByID)
	}

	// Serve embedded static files
	staticFS, _ := fs.Sub(web.Assets, ".")
	fileServer := http.FileServer(http.FS(staticFS))

	// Serve index.html at /admin/ and /admin
	s.mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
	s.mux.Handle("/admin/", http.StripPrefix("/admin/", fileServer))
}

// handleWGClients handles GET (list) and POST (create) for WireGuard clients.
func (s *Server) handleWGClients(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleWGClientsList(w, r)
	case http.MethodPost:
		s.handleWGClientCreate(w, r)
	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleWGClientsList returns all WireGuard clients by proxying to the concentrator.
func (s *Server) handleWGClientsList(w http.ResponseWriter, _ *http.Request) {
	// Proxy to concentrator via relay
	respBody, err := s.relay.SendAPIRequest("GET /clients", nil, 10*time.Second)
	if err != nil {
		s.jsonError(w, "concentrator not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Parse API response
	var apiResp struct {
		StatusCode int             `json:"status_code"`
		Body       json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		s.jsonError(w, "invalid response from concentrator", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if apiResp.StatusCode != 200 {
		w.WriteHeader(apiResp.StatusCode)
	}
	_, _ = w.Write(apiResp.Body)
}

// handleWGClientCreate creates a new WireGuard client by proxying to the concentrator.
func (s *Server) handleWGClientCreate(w http.ResponseWriter, r *http.Request) {
	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.jsonError(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	// Proxy to concentrator via relay
	respBody, err := s.relay.SendAPIRequest("POST /clients", body, 10*time.Second)
	if err != nil {
		s.jsonError(w, "concentrator not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Parse API response
	var apiResp struct {
		StatusCode int             `json:"status_code"`
		Body       json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		s.jsonError(w, "invalid response from concentrator", http.StatusBadGateway)
		return
	}

	// Try to extract client info for DNS cache update
	if apiResp.StatusCode == 201 {
		var createResp struct {
			Client struct {
				DNSName string `json:"dns_name"`
				MeshIP  string `json:"mesh_ip"`
			} `json:"client"`
		}
		if err := json.Unmarshal(apiResp.Body, &createResp); err == nil && createResp.Client.DNSName != "" {
			s.peersMu.Lock()
			s.dnsCache[createResp.Client.DNSName] = createResp.Client.MeshIP
			s.peersMu.Unlock()
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(apiResp.StatusCode)
	_, _ = w.Write(apiResp.Body)
}

// handleWGClientByID handles GET, PATCH, DELETE for a specific WireGuard client by proxying to the concentrator.
func (s *Server) handleWGClientByID(w http.ResponseWriter, r *http.Request) {
	// Extract client ID from path
	id := strings.TrimPrefix(r.URL.Path, "/admin/api/wireguard/clients/")
	if id == "" {
		s.jsonError(w, "client ID required", http.StatusBadRequest)
		return
	}

	var method string
	var body []byte
	var err error

	switch r.Method {
	case http.MethodGet:
		method = "GET /clients/" + id

	case http.MethodPatch:
		method = "PATCH /clients/" + id
		body, err = io.ReadAll(r.Body)
		if err != nil {
			s.jsonError(w, "failed to read request body", http.StatusBadRequest)
			return
		}

	case http.MethodDelete:
		method = "DELETE /clients/" + id

	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Proxy to concentrator via relay
	respBody, err := s.relay.SendAPIRequest(method, body, 10*time.Second)
	if err != nil {
		s.jsonError(w, "concentrator not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Parse API response
	var apiResp struct {
		StatusCode int             `json:"status_code"`
		Body       json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		s.jsonError(w, "invalid response from concentrator", http.StatusBadGateway)
		return
	}

	// Note: DNS cache cleanup for deleted clients is handled by periodic DNS sync

	w.Header().Set("Content-Type", "application/json")
	if apiResp.StatusCode != 200 {
		w.WriteHeader(apiResp.StatusCode)
	}
	_, _ = w.Write(apiResp.Body)
}
