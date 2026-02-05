// Package coord implements the coordination server for tunnelmesh.
package coord

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/wireguard"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

// peerInfo wraps a peer with stats and metadata for admin UI.
type peerInfo struct {
	peer           *proto.Peer
	stats          *proto.PeerStats
	prevStats      *proto.PeerStats
	heartbeatCount uint64
	registeredAt   time.Time
	lastStatsTime  time.Time
	prevStatsTime  time.Time
}

// serverStats tracks server-level statistics.
type serverStats struct {
	startTime       time.Time
	totalHeartbeats uint64
}

// Server is the coordination server that manages peer registration and discovery.
type Server struct {
	cfg          *config.ServerConfig
	mux          *http.ServeMux
	peers        map[string]*peerInfo
	peersMu      sync.RWMutex
	ipAlloc      *ipAllocator
	dnsCache     map[string]string // hostname -> mesh IP
	serverStats  serverStats
	statsHistory *StatsHistory // Per-peer stats time series
	relay        *relayManager
	holePunch    *holePunchManager
	wgStore      *wireguard.Store // WireGuard client storage
	version      string           // Server version for admin display
	sseHub       *sseHub          // SSE hub for real-time dashboard updates
	ipGeoCache   *IPGeoCache      // IP geolocation cache for location fallback
}

// ipAllocator manages IP address allocation from the mesh CIDR.
// It uses deterministic allocation based on peer name hash for consistency.
type ipAllocator struct {
	network   *net.IPNet
	used      map[string]bool
	peerToIP  map[string]string // peer name -> allocated IP (for consistency)
	next      uint32
	mu        sync.Mutex
}

func newIPAllocator(cidr string) (*ipAllocator, error) {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("parse CIDR: %w", err)
	}

	return &ipAllocator{
		network:  network,
		used:     make(map[string]bool),
		peerToIP: make(map[string]string),
		next:     1, // Start from .1, skip .0 (network address)
	}, nil
}

// allocateForPeer allocates an IP for a specific peer, using deterministic
// hashing to ensure the same peer always gets the same IP.
func (a *ipAllocator) allocateForPeer(peerName string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Check if we already allocated an IP for this peer
	if ip, exists := a.peerToIP[peerName]; exists {
		return ip, nil
	}

	// Get the network address as uint32
	ip := a.network.IP.To4()
	if ip == nil {
		return "", fmt.Errorf("only IPv4 supported")
	}

	base := uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])

	ones, bits := a.network.Mask.Size()
	maxHosts := uint32(1<<(bits-ones)) - 2 // Subtract network and broadcast

	// Use hash of peer name to get a deterministic starting point
	// This ensures the same peer always gets the same IP (if available)
	h := fnv.New32a()
	h.Write([]byte(peerName))
	hashOffset := h.Sum32() % maxHosts
	if hashOffset == 0 {
		hashOffset = 1 // Skip .0
	}

	// Try the hash-based IP first, then fall back to sequential search
	for i := uint32(0); i < maxHosts; i++ {
		offset := (hashOffset + i) % maxHosts
		if offset == 0 {
			offset = 1 // Skip .0
		}
		candidate := base + offset

		candidateIP := net.IPv4(
			byte(candidate>>24),
			byte(candidate>>16),
			byte(candidate>>8),
			byte(candidate),
		)

		ipStr := candidateIP.String()
		if !a.used[ipStr] {
			a.used[ipStr] = true
			a.peerToIP[peerName] = ipStr
			return ipStr, nil
		}
	}

	return "", fmt.Errorf("no available IP addresses")
}

func (a *ipAllocator) release(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.used, ip)
}

// NewServer creates a new coordination server.
func NewServer(cfg *config.ServerConfig) (*Server, error) {
	ipAlloc, err := newIPAllocator(cfg.MeshCIDR)
	if err != nil {
		return nil, fmt.Errorf("create IP allocator: %w", err)
	}

	srv := &Server{
		cfg:          cfg,
		mux:          http.NewServeMux(),
		peers:        make(map[string]*peerInfo),
		ipAlloc:      ipAlloc,
		dnsCache:     make(map[string]string),
		statsHistory: NewStatsHistory(),
		serverStats: serverStats{
			startTime: time.Now(),
		},
		sseHub: newSSEHub(),
	}

	// Initialize IP geolocation cache if locations feature is enabled
	if cfg.Locations {
		srv.ipGeoCache = NewIPGeoCache("") // Use default ip-api.com URL
		log.Info().Msg("node location tracking enabled (uses external IP geolocation API)")
	}

	// Initialize WireGuard client store if enabled
	if cfg.WireGuard.Enabled {
		srv.wgStore = wireguard.NewStore(cfg.MeshCIDR)
		log.Info().Msg("WireGuard client management enabled")
	}

	// Load persisted stats history
	if err := srv.LoadStatsHistory(); err != nil {
		log.Warn().Err(err).Msg("failed to load stats history, starting fresh")
	}

	srv.setupRoutes()
	return srv, nil
}

// statsHistoryPath returns the file path for stats history persistence.
func (s *Server) statsHistoryPath() string {
	return filepath.Join(s.cfg.DataDir, "stats_history.json")
}

// LoadStatsHistory loads stats history from disk.
func (s *Server) LoadStatsHistory() error {
	// Ensure data directory exists
	if err := os.MkdirAll(s.cfg.DataDir, 0755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	path := s.statsHistoryPath()
	if err := s.statsHistory.Load(path); err != nil {
		return err
	}

	peerCount := s.statsHistory.PeerCount()
	if peerCount > 0 {
		log.Info().Int("peers", peerCount).Str("path", path).Msg("loaded stats history")
	}
	return nil
}

// SaveStatsHistory persists stats history to disk.
func (s *Server) SaveStatsHistory() error {
	path := s.statsHistoryPath()
	if err := s.statsHistory.Save(path); err != nil {
		return fmt.Errorf("save stats history: %w", err)
	}
	log.Debug().Str("path", path).Msg("saved stats history")
	return nil
}

// Shutdown gracefully shuts down the server, persisting state.
func (s *Server) Shutdown() error {
	log.Info().Msg("saving stats history before shutdown")
	return s.SaveStatsHistory()
}

// StartPeriodicSave starts a goroutine that periodically saves stats history.
// The goroutine stops when the context is cancelled.
func (s *Server) StartPeriodicSave(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.SaveStatsHistory(); err != nil {
					log.Warn().Err(err).Msg("failed to save stats history")
				}
			}
		}
	}()
}

// SetVersion sets the server version for admin display.
func (s *Server) SetVersion(version string) {
	s.version = version
}

func (s *Server) setupRoutes() {
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/api/v1/register", s.withAuth(s.handleRegister))
	s.mux.HandleFunc("/api/v1/peers", s.withAuth(s.handlePeers))
	s.mux.HandleFunc("/api/v1/peers/", s.withAuth(s.handlePeerByName))
	// Note: HTTP heartbeat endpoint removed - heartbeats now sent via WebSocket in relay.go
	s.mux.HandleFunc("/api/v1/dns", s.withAuth(s.handleDNS))

	// Setup relay routes (JWT auth handled internally)
	// Always initialize relay manager if relay or WireGuard is enabled (WG uses relay for API proxying)
	if s.cfg.Relay.Enabled || s.cfg.WireGuard.Enabled {
		s.setupRelayRoutes()
	}

	// Setup admin routes if enabled
	if s.cfg.Admin.Enabled {
		s.setupAdminRoutes()
	}

	// Setup UDP hole-punch coordination routes
	s.setupHolePunchRoutes()

	// Setup WireGuard concentrator sync endpoint (JWT auth)
	if s.cfg.WireGuard.Enabled {
		s.setupWireGuardRoutes()
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			s.jsonError(w, "missing authorization header", http.StatusUnauthorized)
			return
		}

		// Expect "Bearer <token>"
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			s.jsonError(w, "invalid authorization header", http.StatusUnauthorized)
			return
		}

		if parts[1] != s.cfg.AuthToken {
			s.jsonError(w, "invalid token", http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req proto.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	s.peersMu.Lock()
	defer s.peersMu.Unlock()

	// Allocate IP deterministically based on peer name
	// This ensures the same peer always gets the same IP
	meshIP, err := s.ipAlloc.allocateForPeer(req.Name)
	if err != nil {
		s.jsonError(w, "failed to allocate IP: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Preserve existing location if re-registering without new location
	// Only process locations if the feature is enabled
	var location *proto.GeoLocation
	var needsGeoLookup bool
	var geoLookupIP string

	existing, isExisting := s.peers[req.Name]

	if s.cfg.Locations {
		if req.Location != nil {
			// Manual location provided - use it
			location = req.Location
		} else if isExisting && existing.peer.Location != nil {
			// Existing peer has location - check if we should keep it
			existingLoc := existing.peer.Location
			if existingLoc.Source == "manual" {
				// Always preserve manual locations
				location = existingLoc
			} else if existingLoc.Source == "ip" && len(req.PublicIPs) > 0 && len(existing.peer.PublicIPs) > 0 {
				// IP-based location - keep if IP hasn't changed
				if req.PublicIPs[0] == existing.peer.PublicIPs[0] {
					location = existingLoc
				} else {
					// IP changed - need new lookup
					needsGeoLookup = true
					geoLookupIP = req.PublicIPs[0]
				}
			} else {
				// Keep existing location as fallback
				location = existingLoc
			}
		} else if len(req.PublicIPs) > 0 {
			// New peer with public IPs - need geolocation lookup
			needsGeoLookup = true
			geoLookupIP = req.PublicIPs[0]
		}
	}

	peer := &proto.Peer{
		Name:        req.Name,
		PublicKey:   req.PublicKey,
		PublicIPs:   req.PublicIPs,
		PrivateIPs:  req.PrivateIPs,
		SSHPort:     req.SSHPort,
		UDPPort:     req.UDPPort,
		MeshIP:      meshIP,
		LastSeen:    time.Now(),
		Connectable: len(req.PublicIPs) > 0 && !req.BehindNAT,
		BehindNAT:   req.BehindNAT,
		Version:     req.Version,
		Location:    location,
	}

	// Preserve registeredAt for existing peers
	registeredAt := time.Now()
	if isExisting {
		registeredAt = existing.registeredAt
	}

	s.peers[req.Name] = &peerInfo{
		peer:         peer,
		registeredAt: registeredAt,
	}
	s.dnsCache[req.Name] = meshIP

	// Generate JWT token for relay authentication
	token, err := s.GenerateToken(req.Name, meshIP)
	if err != nil {
		s.jsonError(w, "failed to generate token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Info().
		Str("name", req.Name).
		Str("mesh_ip", meshIP).
		Msg("peer registered")

	// Trigger IP geolocation only for new peers or when IP has changed (if locations enabled)
	if needsGeoLookup && s.cfg.Locations && s.ipGeoCache != nil {
		go s.lookupPeerLocation(req.Name, geoLookupIP)
	}

	resp := proto.RegisterResponse{
		MeshIP:   meshIP,
		MeshCIDR: s.cfg.MeshCIDR,
		Domain:   s.cfg.DomainSuffix,
		Token:    token,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.peersMu.RLock()
	defer s.peersMu.RUnlock()

	peers := make([]proto.Peer, 0, len(s.peers))
	for _, info := range s.peers {
		peers = append(peers, *info.peer)
	}

	resp := proto.PeerListResponse{Peers: peers}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handlePeerByName(w http.ResponseWriter, r *http.Request) {
	// Extract peer name from path
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/peers/")
	if name == "" {
		s.jsonError(w, "peer name required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.peersMu.RLock()
		info, exists := s.peers[name]
		s.peersMu.RUnlock()

		if !exists {
			s.jsonError(w, "peer not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(info.peer)

	case http.MethodDelete:
		s.peersMu.Lock()
		info, exists := s.peers[name]
		if exists {
			s.ipAlloc.release(info.peer.MeshIP)
			delete(s.peers, name)
			delete(s.dnsCache, name)
		}
		s.peersMu.Unlock()

		// Also remove UDP endpoint
		if s.holePunch != nil {
			s.holePunch.RemoveEndpoint(name)
		}

		if !exists {
			s.jsonError(w, "peer not found", http.StatusNotFound)
			return
		}

		log.Info().Str("name", name).Msg("peer deregistered")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleDNS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.peersMu.RLock()
	defer s.peersMu.RUnlock()

	records := make([]proto.DNSRecord, 0, len(s.dnsCache))
	for hostname, ip := range s.dnsCache {
		records = append(records, proto.DNSRecord{
			Hostname: hostname,
			MeshIP:   ip,
		})
	}

	resp := proto.DNSUpdateNotification{Records: records}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) jsonError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(proto.ErrorResponse{
		Error:   http.StatusText(code),
		Code:    code,
		Message: message,
	})
}

// lookupPeerLocation performs IP geolocation lookup for a peer and updates their location.
// This is called in a background goroutine to avoid blocking registration.
func (s *Server) lookupPeerLocation(peerName, ip string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	location, err := s.ipGeoCache.Lookup(ctx, ip)
	if err != nil {
		log.Debug().Err(err).Str("peer", peerName).Str("ip", ip).Msg("IP geolocation lookup failed")
		return
	}

	if location == nil {
		// IP could not be geolocated (e.g., private IP)
		return
	}

	// Update peer's location
	s.peersMu.Lock()
	if info, ok := s.peers[peerName]; ok && info.peer.Location == nil {
		info.peer.Location = location
		log.Info().
			Str("peer", peerName).
			Str("city", location.City).
			Str("country", location.Country).
			Msg("peer location updated via IP geolocation")
	}
	s.peersMu.Unlock()
}

// ListenAndServe starts the coordination server.
func (s *Server) ListenAndServe() error {
	log.Info().Str("listen", s.cfg.Listen).Msg("starting coordination server")
	return http.ListenAndServe(s.cfg.Listen, s)
}

// setupWireGuardRoutes registers the WireGuard API routes for concentrator sync.
func (s *Server) setupWireGuardRoutes() {
	// Concentrator fetches client list (JWT auth)
	s.mux.HandleFunc("/api/v1/wireguard/clients", s.handleWireGuardClients)
	// Concentrator reports handshakes (JWT auth)
	s.mux.HandleFunc("/api/v1/wireguard/handshake", s.handleWireGuardHandshake)
}

// handleWireGuardClients returns the list of WireGuard clients for the concentrator.
// Uses JWT authentication.
func (s *Server) handleWireGuardClients(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Validate JWT token
	auth := r.Header.Get("Authorization")
	if auth == "" {
		s.jsonError(w, "missing authorization header", http.StatusUnauthorized)
		return
	}

	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		s.jsonError(w, "invalid authorization header", http.StatusUnauthorized)
		return
	}

	_, err := s.ValidateToken(parts[1])
	if err != nil {
		s.jsonError(w, "invalid token", http.StatusUnauthorized)
		return
	}

	// Return only enabled clients
	allClients := s.wgStore.List()
	enabledClients := make([]wireguard.Client, 0)
	for _, c := range allClients {
		if c.Enabled {
			enabledClients = append(enabledClients, c)
		}
	}

	resp := wireguard.ClientListResponse{
		Clients: enabledClients,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleWireGuardHandshake updates the last seen time for a WireGuard client.
// Called by the concentrator when it detects a client handshake.
func (s *Server) handleWireGuardHandshake(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Validate JWT token
	auth := r.Header.Get("Authorization")
	if auth == "" {
		s.jsonError(w, "missing authorization header", http.StatusUnauthorized)
		return
	}

	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		s.jsonError(w, "invalid authorization header", http.StatusUnauthorized)
		return
	}

	_, err := s.ValidateToken(parts[1])
	if err != nil {
		s.jsonError(w, "invalid token", http.StatusUnauthorized)
		return
	}

	var req wireguard.HandshakeReport
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := s.wgStore.UpdateLastSeen(req.ClientID, req.HandshakeAt); err != nil {
		s.jsonError(w, "client not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
