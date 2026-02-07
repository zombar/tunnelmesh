package coord

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/web"
	"github.com/tunnelmesh/tunnelmesh/internal/mesh"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

// redirectToCanonicalDomain returns middleware that redirects .tm and .mesh requests
// to the canonical .tunnelmesh domain.
func redirectToCanonicalDomain(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		// Strip port if present
		if colonIdx := strings.LastIndex(host, ":"); colonIdx != -1 {
			host = host[:colonIdx]
		}

		// Check if using an alias suffix
		for _, alias := range []string{mesh.AliasTM, mesh.AliasMesh} {
			if strings.HasSuffix(host, alias) {
				// Redirect to canonical domain
				canonical := strings.TrimSuffix(host, alias) + mesh.DomainSuffix
				scheme := "https"
				if r.TLS == nil {
					scheme = "http"
				}
				targetURL := scheme + "://" + canonical + r.URL.RequestURI()
				http.Redirect(w, r, targetURL, http.StatusMovedPermanently)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// AdminOverview is the response for the admin overview endpoint.
type AdminOverview struct {
	ServerUptime     string          `json:"server_uptime"`
	ServerVersion    string          `json:"server_version"`
	TotalPeers       int             `json:"total_peers"`
	OnlinePeers      int             `json:"online_peers"`
	TotalHeartbeats  uint64          `json:"total_heartbeats"`
	MeshCIDR         string          `json:"mesh_cidr"`
	DomainSuffix     string          `json:"domain_suffix"`
	LocationsEnabled bool            `json:"locations_enabled"` // Whether node location tracking is enabled
	Peers            []AdminPeerInfo `json:"peers"`
}

// AdminPeerInfo contains peer information for the admin UI.
type AdminPeerInfo struct {
	Name                string             `json:"name"`
	MeshIP              string             `json:"mesh_ip"`
	PublicIPs           []string           `json:"public_ips"`
	PrivateIPs          []string           `json:"private_ips"`
	SSHPort             int                `json:"ssh_port"`
	UDPPort             int                `json:"udp_port"`
	UDPExternalAddr4    string             `json:"udp_external_addr4,omitempty"`
	UDPExternalAddr6    string             `json:"udp_external_addr6,omitempty"`
	LastSeen            time.Time          `json:"last_seen"`
	Online              bool               `json:"online"`
	Connectable         bool               `json:"connectable"`
	BehindNAT           bool               `json:"behind_nat"`
	RegisteredAt        time.Time          `json:"registered_at"`
	HeartbeatCount      uint64             `json:"heartbeat_count"`
	Stats               *proto.PeerStats   `json:"stats,omitempty"`
	BytesSentRate       float64            `json:"bytes_sent_rate"`
	BytesReceivedRate   float64            `json:"bytes_received_rate"`
	PacketsSentRate     float64            `json:"packets_sent_rate"`
	PacketsReceivedRate float64            `json:"packets_received_rate"`
	Version             string             `json:"version,omitempty"`
	Location            *proto.GeoLocation `json:"location,omitempty"`
	History             []StatsDataPoint   `json:"history,omitempty"`
	// Exit node info
	AllowsExitTraffic bool     `json:"allows_exit_traffic,omitempty"`
	ExitNode          string   `json:"exit_node,omitempty"`
	ExitClients       []string `json:"exit_clients,omitempty"`
	// Connection info (peer -> transport type)
	Connections map[string]string `json:"connections,omitempty"`
	// DNS aliases for this peer
	Aliases []string `json:"aliases,omitempty"`
	// Latency metrics
	CoordinatorRTTMs int64            `json:"coordinator_rtt_ms,omitempty"`
	PeerLatencies    map[string]int64 `json:"peer_latencies,omitempty"`
}

// handleAdminOverview returns the admin overview data.
// Query params:
//   - history=N: include last N stats data points per peer (default: 0)
//   - since=<RFC3339>: include stats data points since this timestamp
//   - maxPoints=N: downsample history to at most N points (for chart display)
func (s *Server) handleAdminOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse history query param
	historyLimit := 0
	if h := r.URL.Query().Get("history"); h != "" {
		if n, err := strconv.Atoi(h); err == nil && n > 0 {
			historyLimit = n
		}
	}

	// Parse since query param (RFC3339 timestamp)
	var sinceTime time.Time
	if since := r.URL.Query().Get("since"); since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			sinceTime = t
		}
	}

	// Parse maxPoints query param for downsampling
	maxPoints := 0
	if mp := r.URL.Query().Get("maxPoints"); mp != "" {
		if n, err := strconv.Atoi(mp); err == nil && n > 0 {
			maxPoints = n
		}
	}

	s.peersMu.RLock()
	defer s.peersMu.RUnlock()

	now := time.Now()
	onlineThreshold := 2 * time.Minute

	overview := AdminOverview{
		ServerUptime:     time.Since(s.serverStats.startTime).Round(time.Second).String(),
		ServerVersion:    s.version,
		TotalPeers:       len(s.peers),
		TotalHeartbeats:  s.serverStats.totalHeartbeats,
		MeshCIDR:         mesh.CIDR,
		DomainSuffix:     mesh.DomainSuffix,
		LocationsEnabled: s.cfg.Locations,
		Peers:            make([]AdminPeerInfo, 0, len(s.peers)),
	}

	// Build exit client map (which clients use which exit node)
	exitClients := make(map[string][]string) // exitNodeName -> [clientNames]
	for _, info := range s.peers {
		if info.peer.ExitNode != "" {
			exitClients[info.peer.ExitNode] = append(exitClients[info.peer.ExitNode], info.peer.Name)
		}
	}

	for _, info := range s.peers {
		online := now.Sub(info.peer.LastSeen) < onlineThreshold
		if online {
			overview.OnlinePeers++
		}

		peerInfo := AdminPeerInfo{
			Name:              info.peer.Name,
			MeshIP:            info.peer.MeshIP,
			PublicIPs:         info.peer.PublicIPs,
			PrivateIPs:        info.peer.PrivateIPs,
			SSHPort:           info.peer.SSHPort,
			UDPPort:           info.peer.UDPPort,
			LastSeen:          info.peer.LastSeen,
			Online:            online,
			Connectable:       info.peer.Connectable,
			BehindNAT:         info.peer.BehindNAT,
			RegisteredAt:      info.registeredAt,
			HeartbeatCount:    info.heartbeatCount,
			Stats:             info.stats,
			Version:           info.peer.Version,
			AllowsExitTraffic: info.peer.AllowsExitTraffic,
			ExitNode:          info.peer.ExitNode,
			Aliases:           info.aliases,
		}

		// Include exit clients if this peer allows exit traffic
		if info.peer.AllowsExitTraffic {
			peerInfo.ExitClients = exitClients[info.peer.Name]
		}

		// Include connection types from stats (peer -> transport type)
		if info.stats != nil && len(info.stats.Connections) > 0 {
			peerInfo.Connections = info.stats.Connections
		}

		// Include latency metrics
		peerInfo.CoordinatorRTTMs = info.coordinatorRTT
		if len(info.peerLatencies) > 0 {
			peerInfo.PeerLatencies = info.peerLatencies
		}

		// Only include location if the feature is enabled
		if s.cfg.Locations {
			peerInfo.Location = info.peer.Location
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
			// Use actual time delta for rate calculation (more accurate than fixed interval)
			delta := info.lastStatsTime.Sub(info.prevStatsTime).Seconds()
			if delta > 0 {
				peerInfo.BytesSentRate = float64(info.stats.BytesSent-info.prevStats.BytesSent) / delta
				peerInfo.BytesReceivedRate = float64(info.stats.BytesReceived-info.prevStats.BytesReceived) / delta
				peerInfo.PacketsSentRate = float64(info.stats.PacketsSent-info.prevStats.PacketsSent) / delta
				peerInfo.PacketsReceivedRate = float64(info.stats.PacketsReceived-info.prevStats.PacketsReceived) / delta
			}
		}

		// Include history if requested
		if !sinceTime.IsZero() {
			// Time-based history query (for charts)
			peerInfo.History = s.statsHistory.GetHistorySince(info.peer.Name, sinceTime)
			// Downsample if needed
			if maxPoints > 0 && len(peerInfo.History) > maxPoints {
				peerInfo.History = downsampleHistory(peerInfo.History, maxPoints)
			}
		} else if historyLimit > 0 {
			// Count-based history query (legacy)
			peerInfo.History = s.statsHistory.GetHistory(info.peer.Name, historyLimit)
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
// Note: Admin routes have no authentication - access is controlled by network binding.
// Admin is only accessible from inside the mesh via HTTPS on mesh IP (https://this.tunnelmesh/)
// Requires join_mesh to be configured.
func (s *Server) setupAdminRoutes() {
	if s.cfg.JoinMesh == nil {
		// Admin panel requires join_mesh - coordinator must be a mesh peer
		return
	}

	// Serve embedded static files
	staticFS, _ := fs.Sub(web.Assets, ".")
	fileServer := http.FileServer(http.FS(staticFS))

	// Create separate adminMux for HTTPS server on mesh IP
	// Serve at root - dedicated server doesn't need /admin/ prefix
	s.adminMux = http.NewServeMux()

	s.adminMux.HandleFunc("/api/overview", s.handleAdminOverview)
	s.adminMux.HandleFunc("/api/events", s.handleSSE)

	if s.cfg.WireGuard.Enabled {
		s.adminMux.HandleFunc("/api/wireguard/clients", s.handleWGClients)
		s.adminMux.HandleFunc("/api/wireguard/clients/", s.handleWGClientByID)
	}

	// Filter rule management
	s.adminMux.HandleFunc("/api/filter/rules", s.handleFilterRules)

	// Group management API
	s.adminMux.HandleFunc("/api/groups", s.handleGroups)
	s.adminMux.HandleFunc("/api/groups/", s.handleGroupByName)

	// File share management API
	s.adminMux.HandleFunc("/api/shares", s.handleShares)
	s.adminMux.HandleFunc("/api/shares/", s.handleShareByName)

	// User management API
	s.adminMux.HandleFunc("/api/users", s.handleUsers)

	// Expose metrics on admin interface for Prometheus scraping via mesh IP
	s.adminMux.Handle("/metrics", promhttp.Handler())

	s.adminMux.Handle("/", fileServer)
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
	// Extract client ID from path (admin served at /api/wireguard/clients/)
	id := strings.TrimPrefix(r.URL.Path, "/api/wireguard/clients/")
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

// downsampleHistory reduces the number of data points using uniform sampling.
// This preserves the general shape of the data while reducing payload size for charts.
func downsampleHistory(data []StatsDataPoint, targetPoints int) []StatsDataPoint {
	if len(data) <= targetPoints || targetPoints <= 0 {
		return data
	}

	// Use uniform sampling - pick evenly spaced points
	step := float64(len(data)-1) / float64(targetPoints-1)
	result := make([]StatsDataPoint, targetPoints)

	for i := 0; i < targetPoints; i++ {
		idx := int(float64(i) * step)
		if idx >= len(data) {
			idx = len(data) - 1
		}
		result[i] = data[idx]
	}

	return result
}

// MonitoringProxyConfig holds configuration for reverse proxying to monitoring services.
type MonitoringProxyConfig struct {
	PrometheusURL string // e.g., "http://localhost:9090"
	GrafanaURL    string // e.g., "http://localhost:3000"
}

// SetupMonitoringProxies registers reverse proxy handlers for Prometheus and Grafana.
// These are registered on the adminMux for access via https://this.tunnelmesh/
// Prometheus should be configured with --web.route-prefix=/prometheus/
// Grafana should be configured with GF_SERVER_SERVE_FROM_SUB_PATH=true
func (s *Server) SetupMonitoringProxies(cfg MonitoringProxyConfig) {
	if s.adminMux == nil {
		return
	}

	if cfg.PrometheusURL != "" {
		promURL, err := url.Parse(cfg.PrometheusURL)
		if err == nil {
			// Create custom reverse proxy that preserves the path
			proxy := &httputil.ReverseProxy{
				Director: func(req *http.Request) {
					req.URL.Scheme = promURL.Scheme
					req.URL.Host = promURL.Host
					req.Host = promURL.Host
					// Path already includes /prometheus/ prefix which Prometheus expects
				},
			}
			s.adminMux.HandleFunc("/prometheus/", func(w http.ResponseWriter, r *http.Request) {
				proxy.ServeHTTP(w, r)
			})
		}
	}

	if cfg.GrafanaURL != "" {
		grafanaURL, err := url.Parse(cfg.GrafanaURL)
		if err == nil {
			// Create custom reverse proxy that preserves the path
			proxy := &httputil.ReverseProxy{
				Director: func(req *http.Request) {
					req.URL.Scheme = grafanaURL.Scheme
					req.URL.Host = grafanaURL.Host
					req.Host = grafanaURL.Host
					// Path already includes /grafana/ prefix which Grafana expects
				},
			}
			s.adminMux.HandleFunc("/grafana/", func(w http.ResponseWriter, r *http.Request) {
				proxy.ServeHTTP(w, r)
			})
		}
	}
}

// FilterRulesRequest is the request for adding/removing filter rules.
type FilterRulesRequest struct {
	PeerName   string `json:"peer"`        // Target peer name
	Port       uint16 `json:"port"`        // Port number
	Protocol   string `json:"protocol"`    // "tcp" or "udp"
	Action     string `json:"action"`      // "allow" or "deny"
	SourcePeer string `json:"source_peer"` // Source peer (optional, empty = any peer)
}

// FilterRulesResponse is the response for listing filter rules.
type FilterRulesResponse struct {
	PeerName    string           `json:"peer"`
	DefaultDeny bool             `json:"default_deny"`
	Rules       []FilterRuleInfo `json:"rules"`
	Error       string           `json:"error,omitempty"` // Set when query failed (peer offline/timeout)
}

// FilterRuleInfo represents a filter rule for API responses.
type FilterRuleInfo struct {
	Port       uint16 `json:"port"`
	Protocol   string `json:"protocol"`
	Action     string `json:"action"`
	Source     string `json:"source"`      // "coordinator", "config", "temporary"
	Expires    int64  `json:"expires"`     // Unix timestamp, 0=permanent
	SourcePeer string `json:"source_peer"` // Source peer (empty = any peer)
}

// handleFilterRules handles GET (list) and POST/DELETE for filter rules.
// GET: List rules for a peer (requires ?peer=name query param)
// POST: Add a temporary rule to a peer
// DELETE: Remove a temporary rule from a peer
func (s *Server) handleFilterRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleFilterRulesList(w, r)
	case http.MethodPost:
		s.handleFilterRuleAdd(w, r)
	case http.MethodDelete:
		s.handleFilterRuleRemove(w, r)
	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleFilterRulesList returns the filter rules for a peer by querying the peer directly.
func (s *Server) handleFilterRulesList(w http.ResponseWriter, r *http.Request) {
	peerName := r.URL.Query().Get("peer")
	if peerName == "" {
		s.jsonError(w, "peer parameter required", http.StatusBadRequest)
		return
	}

	// Query the peer for their current filter rules
	rulesJSON, err := s.relay.QueryFilterRules(peerName, 10*time.Second)
	if err != nil {
		// Peer not connected or timeout - return empty rules with error message
		log.Debug().Err(err).Str("peer", peerName).Msg("failed to query peer filter rules")
		resp := FilterRulesResponse{
			PeerName:    peerName,
			DefaultDeny: s.cfg.Filter.IsDefaultDeny(),
			Rules:       []FilterRuleInfo{},
			Error:       "Peer offline or unreachable",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	// Parse the response from the peer
	var peerRules []struct {
		Port       uint16 `json:"port"`
		Protocol   string `json:"protocol"`
		Action     string `json:"action"`
		SourcePeer string `json:"source_peer"`
		Source     string `json:"source"`
	}
	if err := json.Unmarshal(rulesJSON, &peerRules); err != nil {
		log.Error().Err(err).Str("peer", peerName).Msg("failed to parse peer filter rules")
		s.jsonError(w, "failed to parse peer filter rules", http.StatusInternalServerError)
		return
	}

	// Convert to response format
	rules := make([]FilterRuleInfo, 0, len(peerRules))
	for _, r := range peerRules {
		rules = append(rules, FilterRuleInfo{
			Port:       r.Port,
			Protocol:   r.Protocol,
			Action:     r.Action,
			Source:     r.Source,
			Expires:    0,
			SourcePeer: r.SourcePeer,
		})
	}

	resp := FilterRulesResponse{
		PeerName:    peerName,
		DefaultDeny: s.cfg.Filter.IsDefaultDeny(),
		Rules:       rules,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleFilterRuleAdd adds a temporary filter rule to a peer.
func (s *Server) handleFilterRuleAdd(w http.ResponseWriter, r *http.Request) {
	var req FilterRulesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.PeerName == "" {
		s.jsonError(w, "peer is required", http.StatusBadRequest)
		return
	}
	if req.Port == 0 {
		s.jsonError(w, "port is required", http.StatusBadRequest)
		return
	}
	if req.Protocol != "tcp" && req.Protocol != "udp" {
		s.jsonError(w, "protocol must be 'tcp' or 'udp'", http.StatusBadRequest)
		return
	}
	if req.Action != "allow" && req.Action != "deny" {
		s.jsonError(w, "action must be 'allow' or 'deny'", http.StatusBadRequest)
		return
	}
	// Prevent self-targeting: a peer can't have a rule filtering traffic from itself
	if req.SourcePeer != "" && req.SourcePeer == req.PeerName {
		s.jsonError(w, "a peer cannot filter traffic from itself", http.StatusBadRequest)
		return
	}

	// Push the rule to peer(s) via relay
	if req.PeerName == "__all__" {
		// Broadcast to all connected peers (skip self-referencing rules)
		for _, peerName := range s.relay.GetConnectedPeerNames() {
			if req.SourcePeer != "" && req.SourcePeer == peerName {
				continue // Skip: peer can't filter traffic from itself
			}
			s.relay.PushFilterRuleAdd(peerName, req.Port, req.Protocol, req.Action, req.SourcePeer)
		}
	} else {
		s.relay.PushFilterRuleAdd(req.PeerName, req.Port, req.Protocol, req.Action, req.SourcePeer)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleFilterRuleRemove removes a temporary filter rule from a peer.
func (s *Server) handleFilterRuleRemove(w http.ResponseWriter, r *http.Request) {
	var req FilterRulesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.PeerName == "" {
		s.jsonError(w, "peer is required", http.StatusBadRequest)
		return
	}
	if req.Port == 0 {
		s.jsonError(w, "port is required", http.StatusBadRequest)
		return
	}
	if req.Protocol != "tcp" && req.Protocol != "udp" {
		s.jsonError(w, "protocol must be 'tcp' or 'udp'", http.StatusBadRequest)
		return
	}
	// Prevent self-targeting
	if req.SourcePeer != "" && req.SourcePeer == req.PeerName {
		s.jsonError(w, "a peer cannot filter traffic from itself", http.StatusBadRequest)
		return
	}

	// Push the rule removal to peer(s) via relay
	if req.PeerName == "__all__" {
		// Broadcast to all connected peers (skip self-referencing rules)
		for _, peerName := range s.relay.GetConnectedPeerNames() {
			if req.SourcePeer != "" && req.SourcePeer == peerName {
				continue
			}
			s.relay.PushFilterRuleRemove(peerName, req.Port, req.Protocol, req.SourcePeer)
		}
	} else {
		s.relay.PushFilterRuleRemove(req.PeerName, req.Port, req.Protocol, req.SourcePeer)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// --- Group API Handlers ---

// handleGroups handles GET (list) and POST (create) for groups.
func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGroupsList(w, r)
	case http.MethodPost:
		s.handleGroupCreate(w, r)
	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGroupsList returns all groups.
func (s *Server) handleGroupsList(w http.ResponseWriter, _ *http.Request) {
	if s.s3Authorizer == nil || s.s3Authorizer.Groups == nil {
		s.jsonError(w, "groups not enabled", http.StatusServiceUnavailable)
		return
	}

	groups := s.s3Authorizer.Groups.List()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(groups)
}

// GroupCreateRequest is the request body for creating a group.
type GroupCreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// handleGroupCreate creates a new group.
func (s *Server) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
	if s.s3Authorizer == nil || s.s3Authorizer.Groups == nil {
		s.jsonError(w, "groups not enabled", http.StatusServiceUnavailable)
		return
	}

	var req GroupCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		s.jsonError(w, "name is required", http.StatusBadRequest)
		return
	}

	// Check if group already exists
	if s.s3Authorizer.Groups.Get(req.Name) != nil {
		s.jsonError(w, "group already exists", http.StatusConflict)
		return
	}

	// Create the group
	group, err := s.s3Authorizer.Groups.Create(req.Name, req.Description)
	if err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Persist
	if s.s3SystemStore != nil {
		if err := s.s3SystemStore.SaveGroups(s.s3Authorizer.Groups.List()); err != nil {
			log.Warn().Err(err).Msg("failed to persist groups")
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(group)
}

// handleGroupByName handles GET, DELETE, and sub-resources for a specific group.
func (s *Server) handleGroupByName(w http.ResponseWriter, r *http.Request) {
	if s.s3Authorizer == nil || s.s3Authorizer.Groups == nil {
		s.jsonError(w, "groups not enabled", http.StatusServiceUnavailable)
		return
	}

	// Parse path: /api/groups/{name} or /api/groups/{name}/members[/{id}]
	path := strings.TrimPrefix(r.URL.Path, "/api/groups/")
	parts := strings.Split(path, "/")
	groupName := parts[0]

	if groupName == "" {
		s.jsonError(w, "group name required", http.StatusBadRequest)
		return
	}

	// Check if this is a sub-resource request
	if len(parts) > 1 {
		switch parts[1] {
		case "members":
			s.handleGroupMembers(w, r, groupName, parts[2:])
			return
		case "bindings":
			s.handleGroupBindings(w, r, groupName)
			return
		}
	}

	// Direct group operations
	switch r.Method {
	case http.MethodGet:
		group := s.s3Authorizer.Groups.Get(groupName)
		if group == nil {
			s.jsonError(w, "group not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(group)

	case http.MethodDelete:
		group := s.s3Authorizer.Groups.Get(groupName)
		if group == nil {
			s.jsonError(w, "group not found", http.StatusNotFound)
			return
		}
		if group.Builtin {
			s.jsonError(w, "cannot delete built-in group", http.StatusForbidden)
			return
		}
		_ = s.s3Authorizer.Groups.Delete(groupName)

		// Persist
		if s.s3SystemStore != nil {
			if err := s.s3SystemStore.SaveGroups(s.s3Authorizer.Groups.List()); err != nil {
				log.Warn().Err(err).Msg("failed to persist groups")
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})

	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// GroupMemberRequest is the request body for adding a member.
type GroupMemberRequest struct {
	UserID string `json:"user_id"`
}

// handleGroupMembers handles member management for a group.
func (s *Server) handleGroupMembers(w http.ResponseWriter, r *http.Request, groupName string, pathParts []string) {
	group := s.s3Authorizer.Groups.Get(groupName)
	if group == nil {
		s.jsonError(w, "group not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		// List members
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(group.Members)

	case http.MethodPost:
		// Add member
		var req GroupMemberRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.UserID == "" {
			s.jsonError(w, "user_id is required", http.StatusBadRequest)
			return
		}

		if err := s.s3Authorizer.Groups.AddMember(groupName, req.UserID); err != nil {
			s.jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Persist
		if s.s3SystemStore != nil {
			if err := s.s3SystemStore.SaveGroups(s.s3Authorizer.Groups.List()); err != nil {
				log.Warn().Err(err).Msg("failed to persist groups")
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "added"})

	case http.MethodDelete:
		// Remove member - user ID in path
		if len(pathParts) == 0 || pathParts[0] == "" {
			s.jsonError(w, "user_id required in path", http.StatusBadRequest)
			return
		}
		userID := pathParts[0]

		if err := s.s3Authorizer.Groups.RemoveMember(groupName, userID); err != nil {
			s.jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Persist
		if s.s3SystemStore != nil {
			if err := s.s3SystemStore.SaveGroups(s.s3Authorizer.Groups.List()); err != nil {
				log.Warn().Err(err).Msg("failed to persist groups")
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "removed"})

	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// GroupBindingRequest is the request body for granting a role to a group.
type GroupBindingRequest struct {
	RoleName    string `json:"role_name"`
	BucketScope string `json:"bucket_scope,omitempty"`
}

// handleGroupBindings handles role bindings for a group.
func (s *Server) handleGroupBindings(w http.ResponseWriter, r *http.Request, groupName string) {
	group := s.s3Authorizer.Groups.Get(groupName)
	if group == nil {
		s.jsonError(w, "group not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		bindings := s.s3Authorizer.GroupBindings.GetForGroup(groupName)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(bindings)

	case http.MethodPost:
		var req GroupBindingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.RoleName == "" {
			s.jsonError(w, "role_name is required", http.StatusBadRequest)
			return
		}

		binding := auth.NewGroupBinding(groupName, req.RoleName, req.BucketScope)
		s.s3Authorizer.GroupBindings.Add(binding)

		// Persist
		if s.s3SystemStore != nil {
			if err := s.s3SystemStore.SaveGroupBindings(s.s3Authorizer.GroupBindings.List()); err != nil {
				log.Warn().Err(err).Msg("failed to persist group bindings")
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(binding)

	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- File Share API Handlers ---

// handleShares handles GET (list) and POST (create) for file shares.
func (s *Server) handleShares(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleSharesList(w, r)
	case http.MethodPost:
		s.handleShareCreate(w, r)
	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSharesList returns all file shares.
func (s *Server) handleSharesList(w http.ResponseWriter, _ *http.Request) {
	if s.fileShareMgr == nil {
		s.jsonError(w, "file shares not enabled", http.StatusServiceUnavailable)
		return
	}

	shares := s.fileShareMgr.List()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(shares)
}

// ShareCreateRequest is the request body for creating a file share.
type ShareCreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// handleShareCreate creates a new file share.
func (s *Server) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	if s.fileShareMgr == nil {
		s.jsonError(w, "file shares not enabled", http.StatusServiceUnavailable)
		return
	}

	var req ShareCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		s.jsonError(w, "name is required", http.StatusBadRequest)
		return
	}

	// Get owner from TLS client certificate if available
	ownerID := s.getRequestOwner(r)

	share, err := s.fileShareMgr.Create(req.Name, req.Description, ownerID)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			s.jsonError(w, err.Error(), http.StatusConflict)
		} else {
			s.jsonError(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Persist group bindings (file share creates them)
	if s.s3SystemStore != nil {
		if err := s.s3SystemStore.SaveGroupBindings(s.s3Authorizer.GroupBindings.List()); err != nil {
			log.Warn().Err(err).Msg("failed to persist group bindings")
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(share)
}

// handleShareByName handles GET and DELETE for a specific file share.
func (s *Server) handleShareByName(w http.ResponseWriter, r *http.Request) {
	if s.fileShareMgr == nil {
		s.jsonError(w, "file shares not enabled", http.StatusServiceUnavailable)
		return
	}

	// Parse path: /api/shares/{name}
	path := strings.TrimPrefix(r.URL.Path, "/api/shares/")
	shareName := strings.TrimSuffix(path, "/")

	if shareName == "" {
		s.jsonError(w, "share name required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		share := s.fileShareMgr.Get(shareName)
		if share == nil {
			s.jsonError(w, "share not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(share)

	case http.MethodDelete:
		share := s.fileShareMgr.Get(shareName)
		if share == nil {
			s.jsonError(w, "share not found", http.StatusNotFound)
			return
		}

		if err := s.fileShareMgr.Delete(shareName); err != nil {
			s.jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Persist group bindings (file share removes them)
		if s.s3SystemStore != nil {
			if err := s.s3SystemStore.SaveGroupBindings(s.s3Authorizer.GroupBindings.List()); err != nil {
				log.Warn().Err(err).Msg("failed to persist group bindings")
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})

	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- User API Handlers ---

// UserInfo represents user information for the API.
type UserInfo struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	PublicKey string   `json:"public_key,omitempty"`
	IsService bool     `json:"is_service"`
	Groups    []string `json:"groups"`
	Expired   bool     `json:"expired"`
	LastSeen  string   `json:"last_seen,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
}

// handleUsers handles GET for listing users.
func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.s3SystemStore == nil {
		s.jsonError(w, "user management not enabled", http.StatusServiceUnavailable)
		return
	}

	users, err := s.s3SystemStore.LoadUsers()
	if err != nil {
		s.jsonError(w, "failed to load users", http.StatusInternalServerError)
		return
	}

	// Build user info with groups
	result := make([]UserInfo, 0, len(users))
	for _, u := range users {
		groups := []string{}
		if s.s3Authorizer != nil && s.s3Authorizer.Groups != nil {
			groups = s.s3Authorizer.Groups.GetGroupsForUser(u.ID)
		}

		info := UserInfo{
			ID:        u.ID,
			Name:      u.Name,
			IsService: u.IsService(),
			Groups:    groups,
			Expired:   u.IsExpired(),
		}
		if !u.LastSeen.IsZero() {
			info.LastSeen = u.LastSeen.Format(time.RFC3339)
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
// It tries to get the peer name from the TLS client certificate,
// falling back to "admin" if not available.
func (s *Server) getRequestOwner(r *http.Request) string {
	// Try to get peer name from TLS client certificate
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		cn := r.TLS.PeerCertificates[0].Subject.CommonName
		// CN format is "peername.tunnelmesh" - extract just the peer name
		if strings.HasSuffix(cn, ".tunnelmesh") {
			return strings.TrimSuffix(cn, ".tunnelmesh")
		}
		return cn
	}
	// Fallback to admin if no TLS client certificate
	return "admin"
}
