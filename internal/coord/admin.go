package coord

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
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
	// Exit node info
	AllowsExitTraffic bool     `json:"allows_exit_traffic,omitempty"`
	ExitPeer          string   `json:"exit_node,omitempty"`
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
//
// nolint:gocyclo // Admin overview aggregates data from multiple sources
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
		ServerUptime:     time.Since(s.serverStats.startTime).Round(time.Second).String(),
		ServerVersion:    s.version,
		TotalPeers:       len(s.peers),
		TotalHeartbeats:  s.serverStats.totalHeartbeats,
		MeshCIDR:         mesh.CIDR,
		DomainSuffix:     mesh.DomainSuffix,
		LocationsEnabled: s.cfg.Coordinator.Locations,
		Peers:            make([]AdminPeerInfo, 0, len(s.peers)),
	}

	// Build exit client map (which clients use which exit node)
	exitClients := make(map[string][]string) // exitNodeName -> [clientNames]
	for _, info := range s.peers {
		if info.peer.ExitPeer != "" {
			exitClients[info.peer.ExitPeer] = append(exitClients[info.peer.ExitPeer], info.peer.Name)
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
			ExitPeer:          info.peer.ExitPeer,
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
		if s.cfg.Coordinator.Locations {
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
func (s *Server) setupAdminRoutes() {
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

	// Peer management API
	s.adminMux.HandleFunc("/api/users", s.handlePeersMgmt)

	// Role binding management API
	s.adminMux.HandleFunc("/api/bindings", s.handleBindings)
	s.adminMux.HandleFunc("/api/bindings/", s.handleBindingByName)

	// DNS records
	s.adminMux.HandleFunc("/api/dns", s.handleDNS)

	// S3 bucket management API (specific routes before proxy catch-all)
	s.adminMux.HandleFunc("/api/s3/buckets", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.handleListBuckets(w, r)
		case http.MethodPost:
			s.handleCreateBucket(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	s.adminMux.HandleFunc("/api/s3/buckets/", func(w http.ResponseWriter, r *http.Request) {
		// Extract bucket name from path
		path := strings.TrimPrefix(r.URL.Path, "/api/s3/buckets/")
		path = strings.TrimSuffix(path, "/")

		// If path contains additional segments (e.g., "mybucket/objects"),
		// delegate to S3 proxy handler instead of bucket management
		if strings.Contains(path, "/") {
			s.handleS3Proxy(w, r)
			return
		}

		bucket := path

		switch r.Method {
		case http.MethodGet:
			s.handleGetBucket(w, r, bucket)
		case http.MethodPatch:
			s.handleUpdateBucket(w, r, bucket)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// S3 garbage collection (on-demand)
	s.adminMux.HandleFunc("/api/s3/gc", s.handleS3GC)

	// S3 proxy for explorer
	s.adminMux.HandleFunc("/api/s3/", s.handleS3Proxy)

	// Docker orchestration API (when coordinator has Docker enabled via join_mesh)
	s.adminMux.HandleFunc("/api/docker/containers", s.handleDockerContainers)
	s.adminMux.HandleFunc("/api/docker/containers/", func(w http.ResponseWriter, r *http.Request) {
		// Route to inspect or control based on path suffix
		if strings.HasSuffix(r.URL.Path, "/control") {
			s.handleDockerControl(w, r)
		} else {
			s.handleDockerContainerInspect(w, r)
		}
	})

	// Panel management API
	s.adminMux.HandleFunc("/api/panels", s.handlePanels)
	s.adminMux.HandleFunc("/api/panels/", s.handlePanelByID)
	s.adminMux.HandleFunc("/api/user/permissions", s.handlePeerPermissions)

	// System health check
	s.adminMux.HandleFunc("/api/system/health", s.handleSystemHealth)

	// Coordinator replication endpoint (mesh-only, used by other coordinators)
	// This MUST be on adminMux (not public mux) to ensure replication only happens within the mesh
	if s.replicator != nil {
		s.adminMux.HandleFunc("/api/replication/message", s.handleReplicationMessage)
	}

	// Relay endpoints on admin mux (mesh-only heartbeats and relay fallback)
	// Also registered on public mux for initial connections before mesh join.
	s.adminMux.HandleFunc("/api/v1/relay/persistent", s.handlePersistentRelay)
	s.adminMux.HandleFunc("/api/v1/relay/", s.handleRelay)
	s.adminMux.HandleFunc("/api/v1/relay-status", s.handleRelayStatus)

	// Expose metrics on admin interface for Prometheus scraping via mesh IP.
	// Uses a lazy handler because metricsRegistry is nil at setup time and
	// only set later via SetMetricsRegistry() after mesh join.
	s.adminMux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if gatherer, ok := s.metricsRegistry.(prometheus.Gatherer); ok {
			promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}).ServeHTTP(w, r)
		} else {
			promhttp.Handler().ServeHTTP(w, r)
		}
	})
	// Expose debug trace endpoint on admin interface only (mesh-only access)
	s.adminMux.HandleFunc("/debug/trace", s.handleTrace)

	// Peer site hosting (serves files from peer shares as web pages)
	s.adminMux.HandleFunc("/peers/", s.handlePeerSite)

	// Serve admin dashboard at /admin/ prefix
	s.adminMux.Handle("/admin/", http.StripPrefix("/admin/", fileServer))

	// Landing page at root, 404 for unknown paths
	s.adminMux.HandleFunc("/", s.handleLanding)
}

// handleLanding serves the landing page at "/" or returns 404 for unknown paths.
// If a custom landing page is configured via LandingPage config, it serves that file.
// Otherwise, it serves a built-in default landing page with a redirect to /admin/.
func (s *Server) handleLanding(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// Custom landing page from config
	if s.cfg.Coordinator.LandingPage != "" {
		data, err := os.ReadFile(s.cfg.Coordinator.LandingPage)
		if err != nil {
			log.Error().Err(err).Str("path", s.cfg.Coordinator.LandingPage).Msg("failed to read custom landing page")
			if os.IsNotExist(err) {
				http.Error(w, "landing page not found", http.StatusNotFound)
			} else {
				http.Error(w, "failed to read landing page", http.StatusInternalServerError)
			}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
		return
	}

	// Default built-in landing page
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(defaultLandingPage))
}

const defaultLandingPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="5;url=/admin/">
<title>TunnelMesh</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{background:#0d1117;color:#c9d1d9;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;overflow:hidden}
.container{text-align:center;z-index:2;position:relative}
h1{font-size:2.4rem;font-weight:600;color:#58a6ff;margin-bottom:.5rem;letter-spacing:-.02em}
p{font-size:1.1rem;color:#8b949e;margin-bottom:2rem}
a{color:#58a6ff;text-decoration:none;border:1px solid #30363d;padding:.6rem 1.6rem;font-size:.95rem;transition:all .2s}
a:hover{background:#58a6ff;color:#0d1117;border-color:#58a6ff}
.meta{margin-top:2rem;font-size:.8rem;color:#484f58}
.grid{position:fixed;top:0;left:0;width:100%;height:100%;z-index:1;opacity:.08}
.grid svg{width:100%;height:100%}
</style>
</head>
<body>
<div class="grid"><svg xmlns="http://www.w3.org/2000/svg" width="100%" height="100%"><defs><pattern id="g" width="40" height="40" patternUnits="userSpaceOnUse"><path d="M40 0H0v40" fill="none" stroke="#58a6ff" stroke-width=".5"/></pattern></defs><rect width="100%" height="100%" fill="url(#g)"/></svg></div>
<div class="container">
<h1>TunnelMesh</h1>
<p>Welcome to your secured mesh network</p>
<a href="/admin/">Enter Dashboard</a>
<div class="meta">Redirecting in 5 seconds&hellip;</div>
</div>
</body>
</html>`

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
func (s *Server) handleWGClientsList(w http.ResponseWriter, r *http.Request) {
	// Proxy to concentrator via relay
	respBody, err := s.relay.SendAPIRequest(r.Context(), "GET /clients", nil, 10*time.Second)
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
	respBody, err := s.relay.SendAPIRequest(r.Context(), "POST /clients", body, 10*time.Second)
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
	respBody, err := s.relay.SendAPIRequest(r.Context(), method, body, 10*time.Second)
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

// MonitoringProxyConfig holds configuration for reverse proxying to monitoring services.
type MonitoringProxyConfig struct {
	PrometheusURL string // e.g., "http://localhost:9090"
	GrafanaURL    string // e.g., "http://localhost:3000"
}

// SetupMonitoringProxies registers reverse proxy handlers for Prometheus and Grafana.
// These are registered on the adminMux for access via https://this.tunnelmesh/
// If a service URL is configured locally, traffic is proxied to localhost.
// Otherwise, traffic is forwarded to a coordinator that has monitoring configured.
// Prometheus should be configured with --web.route-prefix=/prometheus/
// Grafana should be configured with GF_SERVER_SERVE_FROM_SUB_PATH=true
func (s *Server) SetupMonitoringProxies(cfg MonitoringProxyConfig) {
	if s.adminMux == nil {
		return
	}

	// Prometheus handler
	if cfg.PrometheusURL != "" {
		promURL, err := url.Parse(cfg.PrometheusURL)
		if err == nil {
			proxy := &httputil.ReverseProxy{
				Director: func(req *http.Request) {
					req.URL.Scheme = promURL.Scheme
					req.URL.Host = promURL.Host
					req.Host = promURL.Host
				},
			}
			s.adminMux.HandleFunc("/prometheus/", func(w http.ResponseWriter, r *http.Request) {
				proxy.ServeHTTP(w, r)
			})
		}
	} else {
		s.adminMux.HandleFunc("/prometheus/", func(w http.ResponseWriter, r *http.Request) {
			s.forwardToMonitoringCoordinator(w, r)
		})
	}

	// Grafana handler
	if cfg.GrafanaURL != "" {
		grafanaURL, err := url.Parse(cfg.GrafanaURL)
		if err == nil {
			proxy := &httputil.ReverseProxy{
				Director: func(req *http.Request) {
					req.URL.Scheme = grafanaURL.Scheme
					req.URL.Host = grafanaURL.Host
					req.Host = grafanaURL.Host
				},
			}
			s.adminMux.HandleFunc("/grafana/", func(w http.ResponseWriter, r *http.Request) {
				proxy.ServeHTTP(w, r)
			})
		}
	} else {
		s.adminMux.HandleFunc("/grafana/", func(w http.ResponseWriter, r *http.Request) {
			s.forwardToMonitoringCoordinator(w, r)
		})
	}
}

// forwardToMonitoringCoordinator proxies the request to a coordinator that has monitoring configured.
func (s *Server) forwardToMonitoringCoordinator(w http.ResponseWriter, r *http.Request) {
	// Detect forwarding loops — if another coordinator already forwarded this request, stop.
	if r.Header.Get("X-TunnelMesh-Forwarded") != "" {
		http.Error(w, "monitoring proxy forwarding loop detected", http.StatusLoopDetected)
		return
	}

	s.peersMu.RLock()
	var targetIP string
	for _, ci := range s.coordinators {
		if ci.hasMonitoring {
			targetIP = ci.peer.MeshIP
			break
		}
	}
	s.peersMu.RUnlock()

	if targetIP == "" {
		http.Error(w, "no monitoring coordinator available", http.StatusServiceUnavailable)
		return
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "https"
			req.URL.Host = targetIP
			req.Host = targetIP
			req.Header.Set("X-TunnelMesh-Forwarded", "true")
		},
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // mesh-internal traffic
		},
	}
	proxy.ServeHTTP(w, r)
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
	S3Enabled   bool             `json:"s3_enabled"` // Whether coordinator has S3 persistence enabled
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
	rulesJSON, err := s.relay.QueryFilterRules(r.Context(), peerName, 10*time.Second)
	if err != nil {
		// Peer not connected or timeout - return empty rules with error message
		log.Debug().Err(err).Str("peer", peerName).Msg("failed to query peer filter rules")
		resp := FilterRulesResponse{
			PeerName:    peerName,
			DefaultDeny: s.cfg.Filter.IsDefaultDeny(),
			S3Enabled:   s.s3SystemStore != nil,
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
		Expires    int64  `json:"expires"`
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
			Expires:    r.Expires,
			SourcePeer: r.SourcePeer,
		})
	}

	resp := FilterRulesResponse{
		PeerName:    peerName,
		DefaultDeny: s.cfg.Filter.IsDefaultDeny(),
		S3Enabled:   s.s3SystemStore != nil,
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
		if err := s.s3SystemStore.SaveGroups(r.Context(), s.s3Authorizer.Groups.List()); err != nil {
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
			if err := s.s3SystemStore.SaveGroups(r.Context(), s.s3Authorizer.Groups.List()); err != nil {
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
			if err := s.s3SystemStore.SaveGroups(r.Context(), s.s3Authorizer.Groups.List()); err != nil {
				log.Warn().Err(err).Msg("failed to persist groups")
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "added"})

	case http.MethodDelete:
		// Remove member - user ID in path
		if len(pathParts) == 0 || pathParts[0] == "" {
			s.jsonError(w, "peer_id required in path", http.StatusBadRequest)
			return
		}
		peerID := pathParts[0]

		if err := s.s3Authorizer.Groups.RemoveMember(groupName, peerID); err != nil {
			s.jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Persist
		if s.s3SystemStore != nil {
			if err := s.s3SystemStore.SaveGroups(r.Context(), s.s3Authorizer.Groups.List()); err != nil {
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
			if err := s.s3SystemStore.SaveGroupBindings(r.Context(), s.s3Authorizer.GroupBindings.List()); err != nil {
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

// FileShareResponse extends FileShare with additional computed fields.
type FileShareResponse struct {
	*s3.FileShare
	OwnerName string `json:"owner_name,omitempty"` // Human-readable owner name (looked up from peer ID)
	SizeBytes int64  `json:"size_bytes"`           // Actual size of all objects in the share bucket
}

// handleSharesList returns all file shares.
func (s *Server) handleSharesList(w http.ResponseWriter, r *http.Request) {
	if s.fileShareMgr == nil {
		s.jsonError(w, "file shares not enabled", http.StatusServiceUnavailable)
		return
	}

	shares := s.fileShareMgr.List()

	// Convert shares to response format with owner names and calculated sizes
	response := make([]FileShareResponse, len(shares))
	for i, share := range shares {
		// Calculate bucket size (returns 0 on error, which is fine for display)
		bucketName := s.fileShareMgr.BucketName(share.Name)
		sizeBytes, _ := s.s3Store.CalculateBucketSize(r.Context(), bucketName)

		response[i] = FileShareResponse{
			FileShare: share,
			OwnerName: s.getPeerName(share.Owner),
			SizeBytes: sizeBytes,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// ShareCreateRequest is the request body for creating a file share.
type ShareCreateRequest struct {
	Name              string `json:"name"`
	Description       string `json:"description,omitempty"`
	QuotaBytes        int64  `json:"quota_bytes,omitempty"`        // Per-share quota in bytes (0 = unlimited within global quota)
	ExpiresAt         string `json:"expires_at,omitempty"`         // ISO 8601 date when share expires (empty = use default)
	GuestRead         *bool  `json:"guest_read,omitempty"`         // Allow all mesh users to read (nil = true, false = owner only)
	ReplicationFactor int    `json:"replication_factor,omitempty"` // Number of replicas (1-3), defaults to 2
}

// validateShareName checks that a share name is DNS-safe (alphanumeric, hyphens, underscores, 1-63 chars).
func validateShareName(name string) error {
	if len(name) > 63 {
		return fmt.Errorf("name too long (max 63 characters)")
	}
	for i, c := range name {
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		isValidHyphen := c == '-' && i > 0 && i < len(name)-1
		isUnderscore := c == '_'
		if !isAlphaNum && !isValidHyphen && !isUnderscore {
			return fmt.Errorf("name must be alphanumeric with optional hyphens (not at start/end)")
		}
	}
	return nil
}

// validateShareQuota checks that share quota is valid.
func validateShareQuota(quotaBytes int64) error {
	const maxQuotaBytes = 1024 * 1024 * 1024 * 1024 // 1TB
	if quotaBytes < 0 {
		return fmt.Errorf("quota_bytes must be non-negative")
	}
	if quotaBytes > maxQuotaBytes {
		return fmt.Errorf("quota_bytes exceeds maximum (1TB)")
	}
	return nil
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

	// Get owner from TLS client certificate - required for share creation
	ownerID := s.getRequestOwner(r)
	if ownerID == "" {
		s.jsonError(w, "client certificate required for share creation", http.StatusUnauthorized)
		return
	}

	// Auto-prefix share name with peer name to prevent name squatting
	peerName := s.getPeerName(ownerID)
	if peerName != "" && peerName != ownerID {
		req.Name = peerName + "_" + req.Name
	}

	// Validate share name and quota
	if err := validateShareName(req.Name); err != nil {
		s.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateShareQuota(req.QuotaBytes); err != nil {
		s.jsonError(w, err.Error(), http.StatusBadRequest)
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
	// Set replication factor (default to 2)
	if req.ReplicationFactor == 0 {
		opts.ReplicationFactor = 2
	} else {
		opts.ReplicationFactor = req.ReplicationFactor
	}

	share, err := s.fileShareMgr.Create(r.Context(), req.Name, req.Description, ownerID, req.QuotaBytes, opts)
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
		if err := s.s3SystemStore.SaveGroupBindings(r.Context(), s.s3Authorizer.GroupBindings.List()); err != nil {
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

		if err := s.fileShareMgr.Delete(r.Context(), shareName); err != nil {
			s.jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Persist group bindings (file share removes them)
		if s.s3SystemStore != nil {
			if err := s.s3SystemStore.SaveGroupBindings(r.Context(), s.s3Authorizer.GroupBindings.List()); err != nil {
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

// --- Role Binding API Handlers ---

// RoleBindingRequest is the request body for creating a peer role binding.
type RoleBindingRequest struct {
	PeerID       string `json:"peer_id"`
	RoleName     string `json:"role_name"`
	BucketScope  string `json:"bucket_scope,omitempty"`
	ObjectPrefix string `json:"object_prefix,omitempty"`
}

// handleBindings handles GET (list) and POST (create) for peer role bindings.
// BindingInfo represents a role binding (peer or group) for the UI.
type BindingInfo struct {
	Name         string    `json:"name"`
	PeerID       string    `json:"peer_id,omitempty"`
	GroupName    string    `json:"group_name,omitempty"`
	RoleName     string    `json:"role_name"`
	BucketScope  string    `json:"bucket_scope,omitempty"`
	ObjectPrefix string    `json:"object_prefix,omitempty"`
	PanelScope   string    `json:"panel_scope,omitempty"`
	Protected    bool      `json:"protected"`
	CreatedAt    time.Time `json:"created_at"`
}

func (s *Server) handleBindings(w http.ResponseWriter, r *http.Request) {
	if s.s3Authorizer == nil {
		s.jsonError(w, "authorization not enabled", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		// Collect both peer and group bindings into a unified format
		var result []BindingInfo

		// Add peer bindings
		for _, b := range s.s3Authorizer.Bindings.List() {
			protected := s.fileShareMgr != nil && s.fileShareMgr.IsProtectedBinding(b)
			result = append(result, BindingInfo{
				Name:         b.Name,
				PeerID:       b.PeerID,
				RoleName:     b.RoleName,
				BucketScope:  b.BucketScope,
				ObjectPrefix: b.ObjectPrefix,
				PanelScope:   b.PanelScope,
				Protected:    protected,
				CreatedAt:    b.CreatedAt,
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
				PanelScope:   gb.PanelScope,
				Protected:    protected,
				CreatedAt:    gb.CreatedAt,
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
		if req.PeerID == "" {
			s.jsonError(w, "peer_id is required", http.StatusBadRequest)
			return
		}
		if req.RoleName == "" {
			s.jsonError(w, "role_name is required", http.StatusBadRequest)
			return
		}

		binding := auth.NewRoleBindingWithPrefix(req.PeerID, req.RoleName, req.BucketScope, req.ObjectPrefix)
		s.s3Authorizer.Bindings.Add(binding)

		// Persist
		if s.s3SystemStore != nil {
			if err := s.s3SystemStore.SaveBindings(r.Context(), s.s3Authorizer.Bindings.List()); err != nil {
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
				if err := s.s3SystemStore.SaveBindings(r.Context(), s.s3Authorizer.Bindings.List()); err != nil {
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
				if err := s.s3SystemStore.SaveGroupBindings(r.Context(), s.s3Authorizer.GroupBindings.List()); err != nil {
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
	Owner        string `json:"owner,omitempty"`         // Owner peer name (derived from bucket owner)
	Expires      string `json:"expires,omitempty"`       // Optional expiration date
	TombstonedAt string `json:"tombstoned_at,omitempty"` // When the object was deleted (tombstoned)
	ContentType  string `json:"content_type,omitempty"`
	IsPrefix     bool   `json:"is_prefix,omitempty"` // True for "folder" prefixes
}

// S3BucketInfo represents an S3 bucket for the explorer API.
type S3BucketInfo struct {
	Name              string `json:"name"`
	CreatedAt         string `json:"created_at"`
	Writable          bool   `json:"writable"`
	UsedBytes         int64  `json:"used_bytes"`
	QuotaBytes        int64  `json:"quota_bytes,omitempty"`        // Per-bucket quota (from file share, 0 = unlimited)
	ReplicationFactor int    `json:"replication_factor,omitempty"` // Number of replicas (1-3)
}

// S3QuotaInfo represents overall S3 storage quota for the explorer API.
type S3QuotaInfo struct {
	MaxBytes   int64 `json:"max_bytes"`
	UsedBytes  int64 `json:"used_bytes"`
	AvailBytes int64 `json:"avail_bytes"`
}

// S3VolumeInfo represents filesystem volume information for the S3 storage.
type S3VolumeInfo struct {
	TotalBytes     int64 `json:"total_bytes"`
	UsedBytes      int64 `json:"used_bytes"`
	AvailableBytes int64 `json:"available_bytes"`
}

// S3BucketsResponse is the response for the buckets list endpoint.
type S3BucketsResponse struct {
	Buckets []S3BucketInfo `json:"buckets"`
	Quota   S3QuotaInfo    `json:"quota"`
	Volume  *S3VolumeInfo  `json:"volume,omitempty"`
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
// handleS3GC triggers on-demand S3 garbage collection.
func (s *Server) handleS3GC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.s3Store == nil {
		s.jsonError(w, "S3 storage not enabled", http.StatusServiceUnavailable)
		return
	}

	// Serialize GC operations — only one can run at a time
	if !s.gcMu.TryLock() {
		s.jsonError(w, "garbage collection already in progress", http.StatusTooManyRequests)
		return
	}
	defer s.gcMu.Unlock()

	var req struct {
		PurgeAllTombstoned bool `json:"purge_all_tombstoned"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	// Phase 1: Purge tombstoned objects
	var tombstonedPurged int
	if req.PurgeAllTombstoned {
		tombstonedPurged = s.s3Store.PurgeAllTombstonedObjects(r.Context())
	} else {
		tombstonedPurged = s.s3Store.PurgeTombstonedObjects(r.Context())
	}

	// Phase 2: Run full GC (version pruning + orphan chunk cleanup)
	gcStats := s.s3Store.RunGarbageCollection(r.Context())

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"tombstoned_purged": tombstonedPurged,
		"versions_pruned":   gcStats.VersionsPruned,
		"chunks_deleted":    gcStats.ChunksDeleted,
		"bytes_reclaimed":   gcStats.BytesReclaimed,
	}); err != nil {
		log.Error().Err(err).Msg("failed to encode GC stats response")
	}
}

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

			// Check for version subresources (e.g., objects/{key}/versions, objects/{key}/restore, objects/{key}/undelete)
			// Split on /versions, /restore, or /undelete to extract key and subresource
			var key, subresource string
			if idx := strings.Index(objPath, "/versions"); idx >= 0 {
				key = objPath[:idx]
				subresource = "versions"
			} else if idx := strings.Index(objPath, "/restore"); idx >= 0 {
				key = objPath[:idx]
				subresource = "restore"
			} else if idx := strings.Index(objPath, "/undelete"); idx >= 0 {
				key = objPath[:idx]
				subresource = "undelete"
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
			case "undelete":
				s.handleS3UndeleteObject(w, r, bucket, key)
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

	buckets, err := s.s3Store.ListBuckets(r.Context())
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
			Name:              b.Name,
			CreatedAt:         b.CreatedAt.Format(time.RFC3339),
			Writable:          writable,
			UsedBytes:         usedBytes,
			QuotaBytes:        quotaBytes,
			ReplicationFactor: b.ReplicationFactor,
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

	// Add filesystem volume info
	if total, used, avail, err := s.s3Store.VolumeStats(); err == nil {
		resp.Volume = &S3VolumeInfo{TotalBytes: total, UsedBytes: used, AvailableBytes: avail}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleListBuckets wraps handleS3ListBuckets for the new API route
func (s *Server) handleListBuckets(w http.ResponseWriter, r *http.Request) {
	s.handleS3ListBuckets(w, r)
}

// handleCreateBucket creates a new S3 bucket with specified replication factor
func (s *Server) handleCreateBucket(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name              string `json:"name"`
		ReplicationFactor int    `json:"replication_factor"` // Optional, defaults to 2
		QuotaBytes        int64  `json:"quota_bytes"`        // Optional (not implemented yet)
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Validate bucket name
	if err := validateS3Name(req.Name); err != nil {
		s.jsonError(w, "invalid bucket name: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Default replication factor
	if req.ReplicationFactor == 0 {
		req.ReplicationFactor = 2
	}

	// Validate range
	if req.ReplicationFactor < 1 || req.ReplicationFactor > 3 {
		s.jsonError(w, "replication factor must be 1-3", http.StatusBadRequest)
		return
	}

	// Validate quota (if specified)
	if req.QuotaBytes < 0 {
		s.jsonError(w, "quota bytes cannot be negative", http.StatusBadRequest)
		return
	}

	// Check authorization
	userID := s.getRequestOwner(r)
	if userID == "" {
		s.jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}

	// Check if user has permission to create buckets
	if !s.s3Authorizer.Authorize(userID, "create", "buckets", req.Name, "") {
		s.jsonError(w, "permission denied", http.StatusForbidden)
		return
	}

	// Create bucket via S3 store
	if err := s.s3Store.CreateBucket(r.Context(), req.Name, userID, req.ReplicationFactor, nil); err != nil {
		if errors.Is(err, s3.ErrBucketExists) {
			s.jsonError(w, "bucket already exists", http.StatusConflict)
			return
		}
		s.jsonError(w, "failed to create bucket: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"name": req.Name})
}

// handleGetBucket returns metadata for a specific bucket
func (s *Server) handleGetBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	// Validate bucket name
	if err := validateS3Name(bucket); err != nil {
		s.jsonError(w, "invalid bucket name: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Check authorization
	userID := s.getRequestOwner(r)
	if userID == "" {
		s.jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}

	// User must have read access to the bucket
	if !s.s3Authorizer.Authorize(userID, "get", "buckets", bucket, "") {
		s.jsonError(w, "access denied", http.StatusForbidden)
		return
	}

	// Get bucket metadata from store
	buckets, err := s.s3Store.ListBuckets(r.Context())
	if err != nil {
		s.jsonError(w, "failed to get bucket", http.StatusInternalServerError)
		return
	}

	// Find the requested bucket
	var bucketMeta *s3.BucketMeta
	for i := range buckets {
		if buckets[i].Name == bucket {
			bucketMeta = &buckets[i]
			break
		}
	}

	if bucketMeta == nil {
		s.jsonError(w, "bucket not found", http.StatusNotFound)
		return
	}

	// Return bucket metadata
	resp := map[string]interface{}{
		"name":               bucketMeta.Name,
		"created_at":         bucketMeta.CreatedAt.Format(time.RFC3339),
		"owner":              bucketMeta.Owner,
		"replication_factor": bucketMeta.ReplicationFactor,
		"size_bytes":         bucketMeta.SizeBytes,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleUpdateBucket updates bucket metadata (admin-only)
func (s *Server) handleUpdateBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	var req struct {
		ReplicationFactor *int `json:"replication_factor,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Validate bucket name
	if err := validateS3Name(bucket); err != nil {
		s.jsonError(w, "invalid bucket name: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Block system bucket modifications (check before auth to match S3 proxy pattern)
	if bucket == auth.SystemBucket {
		s.jsonError(w, "cannot modify system bucket", http.StatusForbidden)
		return
	}

	// Check admin permission (bucket_scope=* or admin role)
	userID := s.getRequestOwner(r)
	if userID == "" {
		s.jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}

	// Admin check: user must be an admin to update bucket metadata
	if !s.s3Authorizer.IsAdmin(userID) {
		s.jsonError(w, "admin permission required", http.StatusForbidden)
		return
	}

	// Update bucket metadata
	updates := s3.BucketMetadataUpdate{
		ReplicationFactor: req.ReplicationFactor,
	}

	if err := s.s3Store.UpdateBucketMetadata(r.Context(), bucket, updates); err != nil {
		if errors.Is(err, s3.ErrBucketNotFound) {
			s.jsonError(w, "bucket not found", http.StatusNotFound)
			return
		}
		s.jsonError(w, "failed to update bucket: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// handleS3ListObjects returns objects in a bucket with optional prefix/delimiter.
func (s *Server) handleS3ListObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	prefix := r.URL.Query().Get("prefix")
	delimiter := r.URL.Query().Get("delimiter")

	objects, _, _, err := s.s3Store.ListObjects(r.Context(), bucket, prefix, "", 1000)
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

	// Get bucket metadata to find owner
	bucketMeta, err := s.s3Store.HeadBucket(r.Context(), bucket)
	var ownerName string
	if err == nil && bucketMeta.Owner != "" {
		// Look up peer name from cached map (falls back to ID if not found)
		ownerName = s.getPeerName(bucketMeta.Owner)
	}

	result := make([]S3ObjectInfo, 0)
	prefixSet := make(map[string]bool)
	prefixSizes := make(map[string]int64) // Track folder sizes for batch calculation

	for _, obj := range objects {
		// Handle delimiter (folder grouping)
		if delimiter != "" {
			keyAfterPrefix := strings.TrimPrefix(obj.Key, prefix)
			if idx := strings.Index(keyAfterPrefix, delimiter); idx >= 0 {
				// This is a "folder" - add common prefix
				commonPrefix := prefix + keyAfterPrefix[:idx+1]
				if !prefixSet[commonPrefix] {
					prefixSet[commonPrefix] = true
					// Size will be calculated below
					prefixSizes[commonPrefix] = 0
				}
				continue
			}
		}

		info := S3ObjectInfo{
			Key:          obj.Key,
			Size:         obj.Size,
			LastModified: obj.LastModified.Format(time.RFC3339),
			Owner:        ownerName,
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

	// Calculate sizes for all folders (prefixes) found
	for commonPrefix := range prefixSizes {
		size, err := s.s3Store.CalculatePrefixSize(r.Context(), bucket, commonPrefix)
		if err != nil {
			size = 0 // Gracefully handle errors - show 0 size rather than failing
		}
		prefixSizes[commonPrefix] = size

		// Add folder to result with calculated size
		result = append(result, S3ObjectInfo{
			Key:      commonPrefix,
			Size:     size,
			IsPrefix: true,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// handleS3Object handles GET/PUT/DELETE for a specific object.
func (s *Server) handleS3Object(w http.ResponseWriter, r *http.Request, bucket, key string) {
	// Forward writes and deletes proactively to primary coordinator.
	// Reads try local first, then forward on miss (see handleS3GetObject/handleS3HeadObject).
	if (r.Method == http.MethodPut || r.Method == http.MethodDelete) &&
		r.Header.Get("X-TunnelMesh-Forwarded") == "" {
		if target := s.objectPrimaryCoordinator(bucket, key); target != "" {
			s.forwardS3Request(w, r, target, bucket)
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		// Check for versionId query param
		versionID := r.URL.Query().Get("versionId")
		if versionID != "" {
			s.handleS3GetObjectVersion(r.Context(), w, bucket, key, versionID)
		} else {
			s.handleS3GetObject(w, r, bucket, key)
		}
	case http.MethodPut:
		s.handleS3PutObject(w, r, bucket, key)
	case http.MethodDelete:
		s.handleS3DeleteObject(w, r, bucket, key)
	case http.MethodHead:
		s.handleS3HeadObject(w, r, bucket, key)
	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleS3GetObject returns the object content.
// Tries local storage first; forwards to primary coordinator on miss.
func (s *Server) handleS3GetObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	reader, meta, err := s.s3Store.GetObject(r.Context(), bucket, key)
	if err != nil {
		// Try forwarding to primary if not found locally
		if (errors.Is(err, s3.ErrObjectNotFound) || errors.Is(err, s3.ErrBucketNotFound)) &&
			r.Header.Get("X-TunnelMesh-Forwarded") == "" {
			if target := s.objectPrimaryCoordinator(bucket, key); target != "" {
				s.forwardS3Request(w, r, target, bucket)
				return
			}
		}
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
func (s *Server) handleS3GetObjectVersion(ctx context.Context, w http.ResponseWriter, bucket, key, versionID string) {
	reader, meta, err := s.s3Store.GetObjectVersion(ctx, bucket, key, versionID)
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
	if existingMeta, err := s.s3Store.HeadObject(r.Context(), bucket, key); err == nil && existingMeta.IsTombstoned() {
		s.jsonError(w, "object is deleted and read-only", http.StatusForbidden)
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Limit request body size to prevent DoS
	r.Body = http.MaxBytesReader(w, r.Body, MaxS3ObjectSize)

	// Attempt to recover missing bucket for share (no-op if bucket exists).
	// Recovery failure is logged but not returned: the subsequent PutObject will fail
	// with ErrBucketNotFound, giving the caller the correct error for their operation.
	if s.fileShareMgr != nil {
		if err := s.fileShareMgr.EnsureBucketForShare(r.Context(), bucket); err != nil {
			log.Warn().Err(err).Str("bucket", bucket).Msg("bucket recovery attempt failed")
		}
	}

	// If this is a forwarded request and the bucket still doesn't exist, create it
	// using the owner from the forwarding coordinator. This handles the race condition
	// where share metadata hasn't been replicated yet.
	// Only trust the bucket owner header on forwarded requests (X-TunnelMesh-Forwarded
	// is set by our own forwardS3Request). External clients cannot spoof this because
	// the forwarded header is checked at the dispatch level before reaching here.
	if r.Header.Get("X-TunnelMesh-Forwarded") != "" {
		if bucketOwner := r.Header.Get("X-TunnelMesh-Bucket-Owner"); bucketOwner != "" {
			if _, err := s.s3Store.HeadBucket(r.Context(), bucket); err != nil {
				if createErr := s.s3Store.CreateBucket(r.Context(), bucket, bucketOwner, 2, nil); createErr != nil {
					if !errors.Is(createErr, s3.ErrBucketExists) {
						log.Warn().Err(createErr).Str("bucket", bucket).Msg("auto-create bucket from forwarded header failed")
					}
				} else {
					log.Info().Str("bucket", bucket).Str("owner", bucketOwner).Msg("auto-created bucket from forwarded write")
				}
			}
		}
	}

	// Stream body directly to PutObject — avoids buffering the entire object in memory.
	// PutObject uses StreamingChunker internally (~64KB peak memory per upload).
	// r.ContentLength is used for early quota checks; -1 if chunked (quota still enforced post-write).
	meta, err := s.s3Store.PutObject(r.Context(), bucket, key, r.Body, r.ContentLength, contentType, nil)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxBytesErr):
			s.jsonError(w, "object too large (max 10MB)", http.StatusRequestEntityTooLarge)
		case errors.Is(err, s3.ErrBucketNotFound):
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		default:
			s.jsonError(w, "failed to store object: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Replicate to other coordinators asynchronously using chunk-level replication.
	// Chunks are already stored by PutObject above — ReplicateObject reads them from CAS
	// and only sends chunks the remote peer doesn't already have.
	//
	// Uses WithoutCancel so replication completes even after the HTTP response is sent,
	// while preserving trace context from the original request.
	// 60s timeout accounts for chunk transfer + metadata send + cleanup across all peers.
	if s.replicator != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
			defer cancel()

			peers := s.replicator.GetPeers()
			if len(peers) == 0 {
				return
			}

			// Replicate to all peers in parallel with bounded concurrency.
			// Semaphore acquisition and result collection both respect context
			// cancellation to prevent goroutine leaks on timeout.
			type result struct {
				peerID string
				err    error
			}
			results := make(chan result, len(peers))
			sem := make(chan struct{}, 3)

			for _, peerID := range peers {
				go func(pid string) {
					// Acquire semaphore, bail out if context expires while waiting
					select {
					case sem <- struct{}{}:
						defer func() { <-sem }()
					case <-ctx.Done():
						results <- result{pid, ctx.Err()}
						return
					}
					results <- result{pid, s.replicator.ReplicateObject(ctx, bucket, key, pid)}
				}(peerID)
			}

			allSucceeded := true
			for range peers {
				select {
				case res := <-results:
					if res.err != nil {
						log.Error().Err(res.err).
							Str("peer", res.peerID).
							Str("bucket", bucket).
							Str("key", key).
							Msg("Failed to replicate S3 PUT operation")
						allSucceeded = false
					}
				case <-ctx.Done():
					log.Warn().
						Str("bucket", bucket).
						Str("key", key).
						Msg("Replication timed out, skipping cleanup")
					allSucceeded = false
				}
			}

			// Only cleanup non-assigned chunks if ALL peers received their chunks.
			// If any peer failed, keep all chunks locally so a retry can succeed.
			if allSucceeded {
				if err := s.replicator.CleanupNonAssignedChunks(ctx, bucket, key); err != nil {
					log.Error().Err(err).
						Str("bucket", bucket).
						Str("key", key).
						Msg("Failed to cleanup non-assigned chunks")
				}
			}
		}()
	}

	w.Header().Set("ETag", meta.ETag)
	w.WriteHeader(http.StatusOK)
}

// handleS3DeleteObject deletes an object.
func (s *Server) handleS3DeleteObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	// Check bucket write permission (could be extended to full RBAC)
	if bucket == auth.SystemBucket {
		s.jsonError(w, "bucket is read-only", http.StatusForbidden)
		return
	}

	err := s.s3Store.DeleteObject(r.Context(), bucket, key)
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

	// Replicate delete to other coordinators asynchronously.
	// Uses WithoutCancel so replication completes after the HTTP response is sent,
	// while preserving trace context from the original request.
	if s.replicator != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
			defer cancel()
			if err := s.replicator.ReplicateDelete(ctx, bucket, key); err != nil {
				log.Error().Err(err).
					Str("bucket", bucket).
					Str("key", key).
					Msg("Failed to replicate S3 DELETE operation")
			}
		}()
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleS3HeadObject returns object metadata.
// Tries local storage first; forwards to primary coordinator on miss.
func (s *Server) handleS3HeadObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	meta, err := s.s3Store.HeadObject(r.Context(), bucket, key)
	if err != nil {
		// Try forwarding to primary if not found locally
		if (errors.Is(err, s3.ErrBucketNotFound) || errors.Is(err, s3.ErrObjectNotFound)) &&
			r.Header.Get("X-TunnelMesh-Forwarded") == "" {
			if target := s.objectPrimaryCoordinator(bucket, key); target != "" {
				s.forwardS3Request(w, r, target, bucket)
				return
			}
		}
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

	versions, err := s.s3Store.ListVersions(r.Context(), bucket, key)
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

	meta, err := s.s3Store.RestoreVersion(r.Context(), bucket, key, req.VersionID)
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

// handleS3UndeleteObject restores a tombstoned (deleted) object.
func (s *Server) handleS3UndeleteObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check bucket write permission
	if bucket == auth.SystemBucket {
		s.jsonError(w, "bucket is read-only", http.StatusForbidden)
		return
	}

	// Untombstone the object
	if err := s.s3Store.UntombstoneObject(r.Context(), bucket, key); err != nil {
		switch {
		case errors.Is(err, s3.ErrBucketNotFound):
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrObjectNotFound):
			s.jsonError(w, "object not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrAccessDenied):
			s.jsonError(w, "access denied", http.StatusForbidden)
		default:
			s.jsonError(w, "failed to restore object: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "restored",
	})
}

// --- Panel Management API ---

// PeerPermissions is the response for the peer permissions endpoint.
type PeerPermissions struct {
	PeerID  string   `json:"peer_id"`
	IsAdmin bool     `json:"is_admin"`
	Panels  []string `json:"panels"`
}

// handlePeerPermissions returns the current peer's accessible panels.
func (s *Server) handlePeerPermissions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.s3Authorizer == nil {
		s.jsonError(w, "authorizer not configured", http.StatusServiceUnavailable)
		return
	}

	peerID := s.getRequestOwner(r)
	if peerID == "" {
		peerID = "guest"
	}

	isAdmin := s.s3Authorizer.IsAdmin(peerID)
	panels := s.s3Authorizer.GetAccessiblePanels(peerID)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(PeerPermissions{
		PeerID:  peerID,
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

	return s.s3SystemStore.SavePanels(context.Background(), panelPtrs)
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

	peerID := s.getRequestOwner(r)
	if !s.s3Authorizer.IsAdmin(peerID) {
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
	panel.CreatedBy = peerID

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

	peerID := s.getRequestOwner(r)
	if !s.s3Authorizer.IsAdmin(peerID) {
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

	peerID := s.getRequestOwner(r)
	if !s.s3Authorizer.IsAdmin(peerID) {
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
		s3.PeersPath,
		s3.BindingsPath,
		s3.GroupsPath,
		s3.GroupBindingsPath,
		s3.WGConcentratorPath,
		s3.DNSCachePath,
		s3.DNSAliasPath,
	}

	for _, path := range files {
		versions, err := store.ListVersions(context.Background(), s3.SystemBucket, path)

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

// objectPrimaryCoordinator returns the mesh IP of the coordinator that should own
// the given bucket/key combination. Returns "" if this coordinator is the primary
// or if there's only one coordinator (no forwarding needed).
func (s *Server) objectPrimaryCoordinator(bucket, key string) string {
	ips := s.GetCoordMeshIPs()
	if len(ips) <= 1 {
		return ""
	}

	selfIP := ips[0] // Self is always first (invariant of GetCoordMeshIPs)

	// Use cached sorted list (updated when coordinator list changes)
	sorted := s.getSortedCoordIPs()
	if len(sorted) <= 1 {
		return ""
	}

	// FNV-1a hash of bucket+key (null separator prevents ambiguity between
	// bucket="a", key="b/c" and bucket="a/b", key="c")
	h := fnv.New32a()
	h.Write([]byte(bucket))
	h.Write([]byte{0})
	h.Write([]byte(key))
	primaryIdx := int(h.Sum32()) % len(sorted)

	primary := sorted[primaryIdx]
	if primary == selfIP {
		return ""
	}
	return primary
}

// forwardS3Request proxies an S3 request to the target coordinator.
// bucket is used to include the bucket owner in the forwarded request so the
// target can create the bucket on-the-fly (avoids race with share replication).
func (s *Server) forwardS3Request(w http.ResponseWriter, r *http.Request, targetIP, bucket string) {
	// Look up bucket owner to include in forwarded request
	var bucketOwner string
	if meta, err := s.s3Store.HeadBucket(r.Context(), bucket); err == nil {
		bucketOwner = meta.Owner
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "https"
			req.URL.Host = targetIP
			req.Host = targetIP
			req.Header.Set("X-TunnelMesh-Forwarded", "true")
			if bucketOwner != "" {
				req.Header.Set("X-TunnelMesh-Bucket-Owner", bucketOwner)
			}
		},
		Transport: s.s3ForwardTransport,
	}
	proxy.ServeHTTP(w, r)
}

// ForwardS3Request implements s3.RequestForwarder. It checks if the given bucket/key
// should be handled by a different coordinator and forwards the request if so.
// The port parameter specifies the target port (e.g. "9000" for S3 API, "" for default 443).
func (s *Server) ForwardS3Request(w http.ResponseWriter, r *http.Request, bucket, key, port string) bool {
	if r.Header.Get("X-TunnelMesh-Forwarded") != "" {
		return false
	}
	target := s.objectPrimaryCoordinator(bucket, key)
	if target == "" {
		return false
	}
	if port != "" {
		target = net.JoinHostPort(target, port)
	}
	s.forwardS3Request(w, r, target, bucket)
	return true
}
