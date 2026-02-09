package coord

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/web"
	"github.com/tunnelmesh/tunnelmesh/internal/mesh"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
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
//
// nolint:gocyclo // Admin overview aggregates data from multiple sources
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

	// Role binding management API
	s.adminMux.HandleFunc("/api/bindings", s.handleBindings)
	s.adminMux.HandleFunc("/api/bindings/", s.handleBindingByName)

	// S3 proxy for explorer
	s.adminMux.HandleFunc("/api/s3/", s.handleS3Proxy)

	// Panel management API
	s.adminMux.HandleFunc("/api/panels", s.handlePanels)
	s.adminMux.HandleFunc("/api/panels/", s.handlePanelByID)
	s.adminMux.HandleFunc("/api/user/permissions", s.handleUserPermissions)

	// System health check
	s.adminMux.HandleFunc("/api/system/health", s.handleSystemHealth)

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
	TTL        int64  `json:"ttl"`         // Time to live in seconds (0 = permanent)
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

	// Push the rule to peer(s) via relay (if relay is enabled)
	if s.relay != nil {
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
	}

	// Validate TTL bounds
	if req.TTL < 0 {
		s.jsonError(w, "ttl cannot be negative", http.StatusBadRequest)
		return
	}
	if req.TTL > 365*24*3600 { // Max 1 year
		s.jsonError(w, "ttl exceeds maximum (1 year)", http.StatusBadRequest)
		return
	}

	// Update coordinator's filter and persist to S3
	if s.filter != nil {
		// Calculate expiry if TTL specified
		var expires int64
		if req.TTL > 0 {
			expires = time.Now().Unix() + req.TTL
		}

		rule := routing.FilterRule{
			Port:       req.Port,
			Protocol:   routing.ProtocolFromString(req.Protocol),
			Action:     routing.ParseFilterAction(req.Action),
			Expires:    expires,
			SourcePeer: req.SourcePeer,
		}
		s.filter.AddTemporaryRule(rule)
		s.saveFilterRulesAsync()
	}

	// Validate TTL bounds
	if req.TTL < 0 {
		s.jsonError(w, "ttl cannot be negative", http.StatusBadRequest)
		return
	}
	if req.TTL > 365*24*3600 { // Max 1 year
		s.jsonError(w, "ttl exceeds maximum (1 year)", http.StatusBadRequest)
		return
	}

	// Update coordinator's filter and persist to S3
	if s.filter != nil {
		// Calculate expiry if TTL specified
		var expires int64
		if req.TTL > 0 {
			expires = time.Now().Unix() + req.TTL
		}

		rule := routing.FilterRule{
			Port:       req.Port,
			Protocol:   routing.ProtocolFromString(req.Protocol),
			Action:     routing.ParseFilterAction(req.Action),
			Expires:    expires,
			SourcePeer: req.SourcePeer,
		}
		s.filter.AddTemporaryRule(rule)
		s.saveFilterRulesAsync()
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

	// Push the rule removal to peer(s) via relay (if relay is enabled)
	if s.relay != nil {
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
	}

	// Update coordinator's filter and persist to S3
	if s.filter != nil {
		proto := routing.ProtocolFromString(req.Protocol)
		s.filter.RemoveTemporaryRuleForPeer(req.Port, proto, req.SourcePeer)
		s.saveFilterRulesAsync()
	}

	// Update coordinator's filter and persist to S3
	if s.filter != nil {
		proto := routing.ProtocolFromString(req.Protocol)
		s.filter.RemoveTemporaryRuleForPeer(req.Port, proto, req.SourcePeer)
		s.saveFilterRulesAsync()
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
	RoleName     string `json:"role_name"`
	BucketScope  string `json:"bucket_scope,omitempty"`
	ObjectPrefix string `json:"object_prefix,omitempty"`
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

		binding := auth.NewGroupBindingWithPrefix(groupName, req.RoleName, req.BucketScope, req.ObjectPrefix)
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
	QuotaBytes  int64  `json:"quota_bytes,omitempty"` // Per-share quota in bytes (0 = unlimited within global quota)
	ExpiresAt   string `json:"expires_at,omitempty"`  // ISO 8601 date when share expires (empty = use default)
	GuestRead   *bool  `json:"guest_read,omitempty"`  // Allow all mesh users to read (nil = true, false = owner only)
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

	// Validate share name (DNS-safe: alphanumeric and hyphens, 1-63 chars)
	if len(req.Name) > 63 {
		s.jsonError(w, "name too long (max 63 characters)", http.StatusBadRequest)
		return
	}
	for i, c := range req.Name {
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		isValidHyphen := c == '-' && i > 0 && i < len(req.Name)-1
		if !isAlphaNum && !isValidHyphen {
			s.jsonError(w, "name must be alphanumeric with optional hyphens (not at start/end)", http.StatusBadRequest)
			return
		}
	}

	// Validate quota (must be non-negative and <= 1TB)
	const maxQuotaBytes = 1024 * 1024 * 1024 * 1024 // 1TB
	if req.QuotaBytes < 0 {
		s.jsonError(w, "quota_bytes must be non-negative", http.StatusBadRequest)
		return
	}
	if req.QuotaBytes > maxQuotaBytes {
		s.jsonError(w, "quota_bytes exceeds maximum (1TB)", http.StatusBadRequest)
		return
	}

	// Get owner from TLS client certificate - required for share creation
	ownerID := s.getRequestOwner(r)
	if ownerID == "" {
		s.jsonError(w, "client certificate required for share creation", http.StatusUnauthorized)
		return
	}

	// Build share options
	opts := &s3.FileShareOptions{}
	if req.ExpiresAt != "" {
		expiresAt, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			// Try parsing as date-only (YYYY-MM-DD)
			expiresAt, err = time.Parse("2006-01-02", req.ExpiresAt)
			if err != nil {
				s.jsonError(w, "invalid expires_at format (use ISO 8601)", http.StatusBadRequest)
				return
			}
			// Set to end of day in UTC
			expiresAt = expiresAt.Add(23*time.Hour + 59*time.Minute + 59*time.Second)
		}
		if expiresAt.Before(time.Now()) {
			s.jsonError(w, "expires_at must be in the future", http.StatusBadRequest)
			return
		}
		opts.ExpiresAt = expiresAt.UTC()
	}
	if req.GuestRead != nil {
		opts.GuestRead = *req.GuestRead
		opts.GuestReadSet = true
	}

	share, err := s.fileShareMgr.Create(req.Name, req.Description, ownerID, req.QuotaBytes, opts)
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
	ExpiresAt string   `json:"expires_at,omitempty"`
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
			// Calculate expiration date for human users (service users don't auto-expire)
			if !u.IsService() {
				expiresAt := u.LastSeen.Add(time.Duration(auth.GetUserExpirationDays()) * 24 * time.Hour)
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
// Returns the user ID (derived from public key) for RBAC purposes, falling back to peer name
// if user ID is not available.
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

	// Look up the user ID for this peer (derived from their public key)
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()

	if info, ok := s.peers[peerName]; ok && info.userID != "" {
		return info.userID
	}

	// Fall back to peer name if user ID not available
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

// --- Role Binding API Handlers ---

// RoleBindingRequest is the request body for creating a user role binding.
type RoleBindingRequest struct {
	UserID       string `json:"user_id"`
	RoleName     string `json:"role_name"`
	BucketScope  string `json:"bucket_scope,omitempty"`
	ObjectPrefix string `json:"object_prefix,omitempty"`
}

// handleBindings handles GET (list) and POST (create) for user role bindings.
// BindingInfo represents a role binding (user or group) for the UI.
type BindingInfo struct {
	Name         string `json:"name"`
	UserID       string `json:"user_id,omitempty"`
	GroupName    string `json:"group_name,omitempty"`
	RoleName     string `json:"role_name"`
	BucketScope  string `json:"bucket_scope,omitempty"`
	ObjectPrefix string `json:"object_prefix,omitempty"`
	Protected    bool   `json:"protected"`
}

func (s *Server) handleBindings(w http.ResponseWriter, r *http.Request) {
	if s.s3Authorizer == nil {
		s.jsonError(w, "authorization not enabled", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		// Collect both user and group bindings into a unified format
		var result []BindingInfo

		// Add user bindings
		for _, b := range s.s3Authorizer.Bindings.List() {
			protected := s.fileShareMgr != nil && s.fileShareMgr.IsProtectedBinding(b)
			result = append(result, BindingInfo{
				Name:         b.Name,
				UserID:       b.UserID,
				RoleName:     b.RoleName,
				BucketScope:  b.BucketScope,
				ObjectPrefix: b.ObjectPrefix,
				Protected:    protected,
			})
		}

		// Add group bindings
		for _, gb := range s.s3Authorizer.GroupBindings.List() {
			protected := s.isProtectedGroupBinding(gb)
			result = append(result, BindingInfo{
				Name:         gb.Name,
				GroupName:    gb.GroupName,
				RoleName:     gb.RoleName,
				BucketScope:  gb.BucketScope,
				ObjectPrefix: gb.ObjectPrefix,
				Protected:    protected,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)

	case http.MethodPost:
		var req RoleBindingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.UserID == "" {
			s.jsonError(w, "user_id is required", http.StatusBadRequest)
			return
		}
		if req.RoleName == "" {
			s.jsonError(w, "role_name is required", http.StatusBadRequest)
			return
		}

		binding := auth.NewRoleBindingWithPrefix(req.UserID, req.RoleName, req.BucketScope, req.ObjectPrefix)
		s.s3Authorizer.Bindings.Add(binding)

		// Persist
		if s.s3SystemStore != nil {
			if err := s.s3SystemStore.SaveBindings(s.s3Authorizer.Bindings.List()); err != nil {
				log.Warn().Err(err).Msg("failed to persist bindings")
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(binding)

	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleBindingByName handles GET and DELETE for a specific role binding.
func (s *Server) handleBindingByName(w http.ResponseWriter, r *http.Request) {
	if s.s3Authorizer == nil {
		s.jsonError(w, "authorization not enabled", http.StatusServiceUnavailable)
		return
	}

	// Parse path: /api/bindings/{name}
	path := strings.TrimPrefix(r.URL.Path, "/api/bindings/")
	bindingName := strings.TrimSuffix(path, "/")

	if bindingName == "" {
		s.jsonError(w, "binding name required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		binding := s.s3Authorizer.Bindings.Get(bindingName)
		if binding == nil {
			s.jsonError(w, "binding not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(binding)

	case http.MethodDelete:
		// Try user binding first
		binding := s.s3Authorizer.Bindings.Get(bindingName)
		if binding != nil {
			// Check if this is a protected file share owner binding
			if s.fileShareMgr != nil && s.fileShareMgr.IsProtectedBinding(binding) {
				s.jsonError(w, "cannot delete file share owner binding; delete the file share instead", http.StatusForbidden)
				return
			}

			s.s3Authorizer.Bindings.Remove(bindingName)

			// Persist
			if s.s3SystemStore != nil {
				if err := s.s3SystemStore.SaveBindings(s.s3Authorizer.Bindings.List()); err != nil {
					log.Warn().Err(err).Msg("failed to persist bindings")
				}
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
			return
		}

		// Try group binding
		groupBinding := s.s3Authorizer.GroupBindings.Get(bindingName)
		if groupBinding != nil {
			// Check if this is a protected group binding
			if s.isProtectedGroupBinding(groupBinding) {
				s.jsonError(w, "cannot delete built-in or file share group binding", http.StatusForbidden)
				return
			}

			s.s3Authorizer.GroupBindings.Remove(bindingName)

			// Persist
			if s.s3SystemStore != nil {
				if err := s.s3SystemStore.SaveGroupBindings(s.s3Authorizer.GroupBindings.List()); err != nil {
					log.Warn().Err(err).Msg("failed to persist group bindings")
				}
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
			return
		}

		s.jsonError(w, "binding not found", http.StatusNotFound)

	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// isProtectedGroupBinding checks if a group binding is protected from deletion.
// Bindings for built-in groups and file share bindings are protected.
func (s *Server) isProtectedGroupBinding(gb *auth.GroupBinding) bool {
	// Check if the group is a built-in group (protected by RBAC)
	if s.s3Authorizer != nil {
		group := s.s3Authorizer.Groups.Get(gb.GroupName)
		if group != nil && group.Builtin {
			return true
		}
	}

	// File share bindings are also protected
	if s.fileShareMgr != nil && s.fileShareMgr.IsProtectedGroupBinding(gb) {
		return true
	}

	return false
}

// --- S3 Explorer API Handlers ---

// S3ObjectInfo represents an S3 object for the explorer API.
type S3ObjectInfo struct {
	Key          string `json:"key"`
	Size         int64  `json:"size"`
	LastModified string `json:"last_modified"`
	Expires      string `json:"expires,omitempty"`       // Optional expiration date
	TombstonedAt string `json:"tombstoned_at,omitempty"` // When the object was deleted (tombstoned)
	ContentType  string `json:"content_type,omitempty"`
	IsPrefix     bool   `json:"is_prefix,omitempty"` // True for "folder" prefixes
}

// S3BucketInfo represents an S3 bucket for the explorer API.
type S3BucketInfo struct {
	Name       string `json:"name"`
	CreatedAt  string `json:"created_at"`
	Writable   bool   `json:"writable"`
	UsedBytes  int64  `json:"used_bytes"`
	QuotaBytes int64  `json:"quota_bytes,omitempty"` // Per-bucket quota (from file share, 0 = unlimited)
}

// S3QuotaInfo represents overall S3 storage quota for the explorer API.
type S3QuotaInfo struct {
	MaxBytes   int64 `json:"max_bytes"`
	UsedBytes  int64 `json:"used_bytes"`
	AvailBytes int64 `json:"avail_bytes"`
}

// S3BucketsResponse is the response for the buckets list endpoint.
type S3BucketsResponse struct {
	Buckets []S3BucketInfo `json:"buckets"`
	Quota   S3QuotaInfo    `json:"quota"`
}

// validateS3Name validates a bucket or object key name to prevent path traversal.
// This is the API-level validation that runs before any S3 store operations.
// The s3.Store also has its own validateName function as defense-in-depth,
// ensuring protection even if the API layer is bypassed or refactored.
// Both functions perform identical checks; duplication is intentional for security.
func validateS3Name(name string) error {
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	// Check for null bytes which could truncate paths on some filesystems
	if strings.ContainsRune(name, 0) {
		return fmt.Errorf("null bytes not allowed")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid name")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("path traversal not allowed")
	}
	// Check for absolute paths or parent directory references
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") {
		return fmt.Errorf("absolute paths not allowed")
	}
	return nil
}

// handleS3Proxy routes S3 explorer API requests.
//
// Security: This endpoint is registered on adminMux which is only served over HTTPS
// on the coordinator's mesh IP (via Server.setupAdminRoutes). It is NOT accessible
// from the public internet. All requests are authenticated via mTLS - the client
// must present a valid mesh certificate. The caller's identity is extracted from
// the TLS client certificate for authorization decisions.
func (s *Server) handleS3Proxy(w http.ResponseWriter, r *http.Request) {
	if s.s3Store == nil {
		s.jsonError(w, "S3 storage not enabled", http.StatusServiceUnavailable)
		return
	}

	// Strip /api/s3 prefix
	path := strings.TrimPrefix(r.URL.Path, "/api/s3")
	path = strings.TrimPrefix(path, "/")

	// Route: /api/s3/buckets
	if path == "buckets" || path == "" {
		s.handleS3ListBuckets(w, r)
		return
	}

	// Route: /api/s3/buckets/{bucket}/objects/...
	if strings.HasPrefix(path, "buckets/") {
		parts := strings.SplitN(strings.TrimPrefix(path, "buckets/"), "/", 2)
		bucket := parts[0]

		// URL decode bucket name
		if decoded, err := url.PathUnescape(bucket); err == nil {
			bucket = decoded
		}

		// Validate bucket name
		if err := validateS3Name(bucket); err != nil {
			s.jsonError(w, "invalid bucket name: "+err.Error(), http.StatusBadRequest)
			return
		}

		if len(parts) == 1 || parts[1] == "" || parts[1] == "objects" {
			// List objects in bucket
			s.handleS3ListObjects(w, r, bucket)
			return
		}
		if strings.HasPrefix(parts[1], "objects/") {
			objPath := strings.TrimPrefix(parts[1], "objects/")

			// Check for version subresources (e.g., objects/{key}/versions, objects/{key}/restore)
			// Split on /versions or /restore to extract key and subresource
			var key, subresource string
			if idx := strings.Index(objPath, "/versions"); idx >= 0 {
				key = objPath[:idx]
				subresource = "versions"
			} else if idx := strings.Index(objPath, "/restore"); idx >= 0 {
				key = objPath[:idx]
				subresource = "restore"
			} else {
				key = objPath
			}

			// URL decode object key
			if decoded, err := url.PathUnescape(key); err == nil {
				key = decoded
			}

			// Validate object key
			if err := validateS3Name(key); err != nil {
				s.jsonError(w, "invalid object key: "+err.Error(), http.StatusBadRequest)
				return
			}

			// Route based on subresource
			switch subresource {
			case "versions":
				s.handleS3ListVersions(w, r, bucket, key)
			case "restore":
				s.handleS3RestoreVersion(w, r, bucket, key)
			default:
				s.handleS3Object(w, r, bucket, key)
			}
			return
		}
	}

	s.jsonError(w, "not found", http.StatusNotFound)
}

// handleS3ListBuckets returns all buckets (admin view).
func (s *Server) handleS3ListBuckets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	buckets, err := s.s3Store.ListBuckets()
	if err != nil {
		s.jsonError(w, "failed to list buckets", http.StatusInternalServerError)
		return
	}

	// Get quota stats for per-bucket usage
	quotaStats := s.s3Store.QuotaStats()

	bucketInfos := make([]S3BucketInfo, 0, len(buckets))
	for _, b := range buckets {
		// System bucket is read-only (could be extended to check RBAC)
		writable := b.Name != auth.SystemBucket
		usedBytes := int64(0)
		if quotaStats != nil {
			usedBytes = quotaStats.PerBucket[b.Name]
		}

		// Look up per-bucket quota from file share if this is a file share bucket
		var quotaBytes int64
		if s.fileShareMgr != nil && strings.HasPrefix(b.Name, s3.FileShareBucketPrefix) {
			shareName := strings.TrimPrefix(b.Name, s3.FileShareBucketPrefix)
			if share := s.fileShareMgr.Get(shareName); share != nil {
				quotaBytes = share.QuotaBytes
			}
		}

		bucketInfos = append(bucketInfos, S3BucketInfo{
			Name:       b.Name,
			CreatedAt:  b.CreatedAt.Format(time.RFC3339),
			Writable:   writable,
			UsedBytes:  usedBytes,
			QuotaBytes: quotaBytes,
		})
	}

	// Build response with quota info
	resp := S3BucketsResponse{
		Buckets: bucketInfos,
	}
	if quotaStats != nil {
		resp.Quota = S3QuotaInfo{
			MaxBytes:   quotaStats.MaxBytes,
			UsedBytes:  quotaStats.UsedBytes,
			AvailBytes: quotaStats.AvailableBytes,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleS3ListObjects returns objects in a bucket with optional prefix/delimiter.
func (s *Server) handleS3ListObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	prefix := r.URL.Query().Get("prefix")
	delimiter := r.URL.Query().Get("delimiter")

	objects, _, _, err := s.s3Store.ListObjects(bucket, prefix, "", 1000)
	if err != nil {
		switch {
		case errors.Is(err, s3.ErrBucketNotFound):
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrAccessDenied):
			s.jsonError(w, "access denied", http.StatusForbidden)
		default:
			s.jsonError(w, "failed to list objects", http.StatusInternalServerError)
		}
		return
	}

	result := make([]S3ObjectInfo, 0)
	prefixSet := make(map[string]bool)

	for _, obj := range objects {
		// Handle delimiter (folder grouping)
		if delimiter != "" {
			keyAfterPrefix := strings.TrimPrefix(obj.Key, prefix)
			if idx := strings.Index(keyAfterPrefix, delimiter); idx >= 0 {
				// This is a "folder" - add common prefix
				commonPrefix := prefix + keyAfterPrefix[:idx+1]
				if !prefixSet[commonPrefix] {
					prefixSet[commonPrefix] = true
					result = append(result, S3ObjectInfo{
						Key:      commonPrefix,
						IsPrefix: true,
					})
				}
				continue
			}
		}

		info := S3ObjectInfo{
			Key:          obj.Key,
			Size:         obj.Size,
			LastModified: obj.LastModified.Format(time.RFC3339),
			ContentType:  obj.ContentType,
		}
		if obj.Expires != nil {
			info.Expires = obj.Expires.Format(time.RFC3339)
		}
		if obj.TombstonedAt != nil {
			info.TombstonedAt = obj.TombstonedAt.Format(time.RFC3339)
		}
		result = append(result, info)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// handleS3Object handles GET/PUT/DELETE for a specific object.
func (s *Server) handleS3Object(w http.ResponseWriter, r *http.Request, bucket, key string) {
	switch r.Method {
	case http.MethodGet:
		// Check for versionId query param
		versionID := r.URL.Query().Get("versionId")
		if versionID != "" {
			s.handleS3GetObjectVersion(w, bucket, key, versionID)
		} else {
			s.handleS3GetObject(w, bucket, key)
		}
	case http.MethodPut:
		s.handleS3PutObject(w, r, bucket, key)
	case http.MethodDelete:
		s.handleS3DeleteObject(w, bucket, key)
	case http.MethodHead:
		s.handleS3HeadObject(w, bucket, key)
	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleS3GetObject returns the object content.
func (s *Server) handleS3GetObject(w http.ResponseWriter, bucket, key string) {
	reader, meta, err := s.s3Store.GetObject(bucket, key)
	if err != nil {
		switch {
		case errors.Is(err, s3.ErrBucketNotFound):
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrObjectNotFound):
			s.jsonError(w, "object not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrAccessDenied):
			s.jsonError(w, "access denied", http.StatusForbidden)
		default:
			s.jsonError(w, "failed to get object", http.StatusInternalServerError)
		}
		return
	}
	defer func() { _ = reader.Close() }()

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))

	_, _ = io.Copy(w, reader)
}

// handleS3GetObjectVersion returns a specific version of an object.
func (s *Server) handleS3GetObjectVersion(w http.ResponseWriter, bucket, key, versionID string) {
	reader, meta, err := s.s3Store.GetObjectVersion(bucket, key, versionID)
	if err != nil {
		switch {
		case errors.Is(err, s3.ErrBucketNotFound):
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrObjectNotFound):
			s.jsonError(w, "version not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrAccessDenied):
			s.jsonError(w, "access denied", http.StatusForbidden)
		default:
			s.jsonError(w, "failed to get object version", http.StatusInternalServerError)
		}
		return
	}
	defer func() { _ = reader.Close() }()

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))
	w.Header().Set("X-Version-Id", meta.VersionID)

	_, _ = io.Copy(w, reader)
}

// MaxS3ObjectSize is the maximum size for S3 object uploads (10MB).
const MaxS3ObjectSize = 10 * 1024 * 1024

// handleS3PutObject creates or updates an object.
func (s *Server) handleS3PutObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	// Check bucket write permission (could be extended to full RBAC)
	if bucket == auth.SystemBucket {
		s.jsonError(w, "bucket is read-only", http.StatusForbidden)
		return
	}

	// Check if object is tombstoned (read-only)
	if existingMeta, err := s.s3Store.HeadObject(bucket, key); err == nil && existingMeta.IsTombstoned() {
		s.jsonError(w, "object is deleted and read-only", http.StatusForbidden)
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Limit request body size to prevent DoS
	r.Body = http.MaxBytesReader(w, r.Body, MaxS3ObjectSize)

	// Read body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			s.jsonError(w, "object too large (max 10MB)", http.StatusRequestEntityTooLarge)
			return
		}
		s.jsonError(w, "failed to read body", http.StatusBadRequest)
		return
	}

	meta, err := s.s3Store.PutObject(bucket, key, bytes.NewReader(body), int64(len(body)), contentType, nil)
	if err != nil {
		s.jsonError(w, "failed to store object: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("ETag", meta.ETag)
	w.WriteHeader(http.StatusOK)
}

// handleS3DeleteObject deletes an object.
func (s *Server) handleS3DeleteObject(w http.ResponseWriter, bucket, key string) {
	// Check bucket write permission (could be extended to full RBAC)
	if bucket == auth.SystemBucket {
		s.jsonError(w, "bucket is read-only", http.StatusForbidden)
		return
	}

	err := s.s3Store.DeleteObject(bucket, key)
	if err != nil {
		switch {
		case errors.Is(err, s3.ErrBucketNotFound):
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrObjectNotFound):
			s.jsonError(w, "object not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrAccessDenied):
			s.jsonError(w, "access denied", http.StatusForbidden)
		default:
			s.jsonError(w, "failed to delete object", http.StatusInternalServerError)
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleS3HeadObject returns object metadata.
func (s *Server) handleS3HeadObject(w http.ResponseWriter, bucket, key string) {
	meta, err := s.s3Store.HeadObject(bucket, key)
	if err != nil {
		switch {
		case errors.Is(err, s3.ErrBucketNotFound), errors.Is(err, s3.ErrObjectNotFound):
			w.WriteHeader(http.StatusNotFound)
		case errors.Is(err, s3.ErrAccessDenied):
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
}

// --- S3 Version API Handlers ---

// S3VersionInfo represents a version for the API response.
type S3VersionInfo struct {
	VersionID    string `json:"version_id"`
	Size         int64  `json:"size"`
	ETag         string `json:"etag"`
	LastModified string `json:"last_modified"`
	IsCurrent    bool   `json:"is_current"`
}

// handleS3ListVersions returns all versions of an object.
func (s *Server) handleS3ListVersions(w http.ResponseWriter, r *http.Request, bucket, key string) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	versions, err := s.s3Store.ListVersions(bucket, key)
	if err != nil {
		switch {
		case errors.Is(err, s3.ErrBucketNotFound):
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrObjectNotFound):
			s.jsonError(w, "object not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrAccessDenied):
			s.jsonError(w, "access denied", http.StatusForbidden)
		default:
			s.jsonError(w, "failed to list versions", http.StatusInternalServerError)
		}
		return
	}

	// Convert to API response format
	result := make([]S3VersionInfo, 0, len(versions))
	for _, v := range versions {
		result = append(result, S3VersionInfo{
			VersionID:    v.VersionID,
			Size:         v.Size,
			ETag:         v.ETag,
			LastModified: v.LastModified.Format(time.RFC3339),
			IsCurrent:    v.IsCurrent,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// RestoreVersionRequest is the request body for restoring a version.
type RestoreVersionRequest struct {
	VersionID string `json:"version_id"`
}

// handleS3RestoreVersion restores a previous version of an object.
func (s *Server) handleS3RestoreVersion(w http.ResponseWriter, r *http.Request, bucket, key string) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check bucket write permission
	if bucket == auth.SystemBucket {
		s.jsonError(w, "bucket is read-only", http.StatusForbidden)
		return
	}

	var req RestoreVersionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.VersionID == "" {
		s.jsonError(w, "version_id is required", http.StatusBadRequest)
		return
	}

	meta, err := s.s3Store.RestoreVersion(bucket, key, req.VersionID)
	if err != nil {
		switch {
		case errors.Is(err, s3.ErrBucketNotFound):
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrObjectNotFound):
			s.jsonError(w, "version not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrAccessDenied):
			s.jsonError(w, "access denied", http.StatusForbidden)
		default:
			s.jsonError(w, "failed to restore version: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "restored",
		"version_id": meta.VersionID,
	})
}

// --- Panel Management API ---

// UserPermissions is the response for the user permissions endpoint.
type UserPermissions struct {
	UserID  string   `json:"user_id"`
	IsAdmin bool     `json:"is_admin"`
	Panels  []string `json:"panels"`
}

// handleUserPermissions returns the current user's accessible panels.
func (s *Server) handleUserPermissions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.s3Authorizer == nil {
		s.jsonError(w, "authorizer not configured", http.StatusServiceUnavailable)
		return
	}

	userID := s.getRequestOwner(r)
	if userID == "" {
		userID = "guest"
	}

	isAdmin := s.s3Authorizer.IsAdmin(userID)
	panels := s.s3Authorizer.GetAccessiblePanels(userID)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(UserPermissions{
		UserID:  userID,
		IsAdmin: isAdmin,
		Panels:  panels,
	})
}

// allowedPluginSchemes defines the allowed URL schemes for external panel plugins.
// Only https and http are allowed to prevent javascript:, file://, data:, etc.
var allowedPluginSchemes = map[string]bool{
	"https": true,
	"http":  true,
}

// validatePluginURL validates that a plugin URL is safe to load.
// Returns an error if the URL scheme is not allowed or the URL is invalid.
func validatePluginURL(pluginURL string) error {
	parsed, err := url.Parse(pluginURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if parsed.Scheme == "" {
		return errors.New("URL must have a scheme (https:// or http://)")
	}

	if !allowedPluginSchemes[parsed.Scheme] {
		return fmt.Errorf("scheme %q not allowed (must be https or http)", parsed.Scheme)
	}

	if parsed.Host == "" {
		return errors.New("URL must have a host")
	}

	return nil
}

// persistExternalPanels saves external panels to the system store.
// Returns an error if persistence fails.
func (s *Server) persistExternalPanels() error {
	if s.s3SystemStore == nil || s.s3Authorizer == nil || s.s3Authorizer.PanelRegistry == nil {
		return nil // No storage configured, skip persistence
	}

	// Get all external panels
	externalPanels := s.s3Authorizer.PanelRegistry.ListExternal()
	panelPtrs := make([]*auth.PanelDefinition, len(externalPanels))
	for i := range externalPanels {
		panelPtrs[i] = &externalPanels[i]
	}

	return s.s3SystemStore.SavePanels(panelPtrs)
}

// handlePanels handles GET (list) and POST (register) for panels.
func (s *Server) handlePanels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handlePanelsList(w, r)
	case http.MethodPost:
		s.handlePanelRegister(w, r)
	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePanelsList returns all registered panels.
func (s *Server) handlePanelsList(w http.ResponseWriter, r *http.Request) {
	if s.s3Authorizer == nil || s.s3Authorizer.PanelRegistry == nil {
		s.jsonError(w, "panel registry not configured", http.StatusServiceUnavailable)
		return
	}

	// Filter by external if query param present
	externalOnly := r.URL.Query().Get("external") == "true"

	var panels []auth.PanelDefinition
	if externalOnly {
		panels = s.s3Authorizer.PanelRegistry.ListExternal()
	} else {
		panels = s.s3Authorizer.PanelRegistry.List()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"panels": panels,
	})
}

// handlePanelRegister registers a new external panel (admin only).
func (s *Server) handlePanelRegister(w http.ResponseWriter, r *http.Request) {
	if s.s3Authorizer == nil || s.s3Authorizer.PanelRegistry == nil {
		s.jsonError(w, "panel registry not configured", http.StatusServiceUnavailable)
		return
	}

	userID := s.getRequestOwner(r)
	if !s.s3Authorizer.IsAdmin(userID) {
		s.jsonError(w, "admin access required", http.StatusForbidden)
		return
	}

	var panel auth.PanelDefinition
	if err := json.NewDecoder(r.Body).Decode(&panel); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Force external flag for API-registered panels
	panel.External = true
	panel.CreatedBy = userID

	// Validate PluginURL for external panels (security: prevent javascript:, file://, etc.)
	if panel.PluginURL != "" {
		if err := validatePluginURL(panel.PluginURL); err != nil {
			s.jsonError(w, fmt.Sprintf("invalid plugin URL: %v", err), http.StatusBadRequest)
			return
		}
	}

	if err := s.s3Authorizer.PanelRegistry.Register(panel); err != nil {
		s.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Persist external panels - rollback on failure
	if err := s.persistExternalPanels(); err != nil {
		// Rollback: unregister the panel
		_ = s.s3Authorizer.PanelRegistry.Unregister(panel.ID)
		log.Error().Err(err).Str("panel_id", panel.ID).Msg("failed to persist panel, rolling back")
		s.jsonError(w, "failed to persist panel registration", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "registered",
		"panel":  s.s3Authorizer.PanelRegistry.Get(panel.ID),
	})
}

// handlePanelByID handles GET, PATCH, DELETE for a specific panel.
func (s *Server) handlePanelByID(w http.ResponseWriter, r *http.Request) {
	// Extract panel ID from path: /api/panels/{id}
	panelID := strings.TrimPrefix(r.URL.Path, "/api/panels/")
	if panelID == "" {
		s.jsonError(w, "panel ID required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handlePanelGet(w, r, panelID)
	case http.MethodPatch:
		s.handlePanelUpdate(w, r, panelID)
	case http.MethodDelete:
		s.handlePanelDelete(w, r, panelID)
	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePanelGet returns a specific panel.
func (s *Server) handlePanelGet(w http.ResponseWriter, _ *http.Request, panelID string) {
	if s.s3Authorizer == nil || s.s3Authorizer.PanelRegistry == nil {
		s.jsonError(w, "panel registry not configured", http.StatusServiceUnavailable)
		return
	}

	panel := s.s3Authorizer.PanelRegistry.Get(panelID)
	if panel == nil {
		s.jsonError(w, "panel not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(panel)
}

// handlePanelUpdate updates a panel (admin only).
func (s *Server) handlePanelUpdate(w http.ResponseWriter, r *http.Request, panelID string) {
	if s.s3Authorizer == nil || s.s3Authorizer.PanelRegistry == nil {
		s.jsonError(w, "panel registry not configured", http.StatusServiceUnavailable)
		return
	}

	userID := s.getRequestOwner(r)
	if !s.s3Authorizer.IsAdmin(userID) {
		s.jsonError(w, "admin access required", http.StatusForbidden)
		return
	}

	panel := s.s3Authorizer.PanelRegistry.Get(panelID)
	if panel == nil {
		s.jsonError(w, "panel not found", http.StatusNotFound)
		return
	}

	var update auth.PanelDefinition
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Validate PluginURL if provided (security: prevent javascript:, file://, etc.)
	if update.PluginURL != "" {
		if err := validatePluginURL(update.PluginURL); err != nil {
			s.jsonError(w, fmt.Sprintf("invalid plugin URL: %v", err), http.StatusBadRequest)
			return
		}
	}

	if err := s.s3Authorizer.PanelRegistry.Update(panelID, update); err != nil {
		s.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Persist external panels
	if err := s.persistExternalPanels(); err != nil {
		// Log but don't fail - panel is already updated in memory
		log.Warn().Err(err).Str("panel_id", panelID).Msg("failed to persist panel update")
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "updated",
		"panel":  s.s3Authorizer.PanelRegistry.Get(panelID),
	})
}

// handlePanelDelete unregisters an external panel (admin only).
func (s *Server) handlePanelDelete(w http.ResponseWriter, r *http.Request, panelID string) {
	if s.s3Authorizer == nil || s.s3Authorizer.PanelRegistry == nil {
		s.jsonError(w, "panel registry not configured", http.StatusServiceUnavailable)
		return
	}

	userID := s.getRequestOwner(r)
	if !s.s3Authorizer.IsAdmin(userID) {
		s.jsonError(w, "admin access required", http.StatusForbidden)
		return
	}

	// Get panel before deletion for potential rollback
	panel := s.s3Authorizer.PanelRegistry.Get(panelID)
	if panel == nil {
		s.jsonError(w, "panel not found", http.StatusNotFound)
		return
	}
	panelCopy := *panel

	if err := s.s3Authorizer.PanelRegistry.Unregister(panelID); err != nil {
		s.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Persist external panels - rollback on failure
	if err := s.persistExternalPanels(); err != nil {
		// Rollback: re-register the panel
		_ = s.s3Authorizer.PanelRegistry.Register(panelCopy)
		log.Error().Err(err).Str("panel_id", panelID).Msg("failed to persist panel deletion, rolling back")
		s.jsonError(w, "failed to persist panel deletion", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "deleted",
	})
}

// handleSystemHealth checks the integrity of system metadata stored in S3.
// Returns version information and metadata health status for each critical file.
//
// Security: This endpoint is mounted on adminMux, which is only accessible from
// within the mesh network via HTTPS. Network-level access control is enforced by
// binding to the coordinator's mesh IP address.
//
// GET /api/system/health
func (s *Server) handleSystemHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.s3SystemStore == nil {
		s.jsonError(w, "S3 not enabled", http.StatusServiceUnavailable)
		return
	}

	results := make(map[string]interface{})
	store := s.s3SystemStore.Raw()

	// Check each critical file
	files := []string{
		s3.UsersPath,
		s3.BindingsPath,
		s3.GroupsPath,
		s3.GroupBindingsPath,
		s3.StatsHistoryPath,
		s3.WGConcentratorPath,
		s3.DNSCachePath,
		s3.DNSAliasPath,
	}

	for _, path := range files {
		versions, err := store.ListVersions(s3.SystemBucket, path)

		fileInfo := make(map[string]interface{})
		fileInfo["exists"] = err == nil && len(versions) > 0
		fileInfo["version_count"] = len(versions)

		if len(versions) > 0 {
			fileInfo["latest_version"] = versions[0].VersionID
			fileInfo["latest_timestamp"] = versions[0].LastModified
			fileInfo["latest_size_bytes"] = versions[0].Size
		}

		if err != nil {
			fileInfo["error"] = err.Error()
		}

		results[path] = fileInfo
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"files":  results,
	}); err != nil {
		log.Warn().Err(err).Msg("failed to encode health response")
	}
}
