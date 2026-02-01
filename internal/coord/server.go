// Package coord implements the coordination server for tunnelmesh.
package coord

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
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
}

// serverStats tracks server-level statistics.
type serverStats struct {
	startTime       time.Time
	totalHeartbeats uint64
}

// Server is the coordination server that manages peer registration and discovery.
type Server struct {
	cfg         *config.ServerConfig
	mux         *http.ServeMux
	peers       map[string]*peerInfo
	peersMu     sync.RWMutex
	ipAlloc     *ipAllocator
	dnsCache    map[string]string // hostname -> mesh IP
	serverStats serverStats
}

// ipAllocator manages IP address allocation from the mesh CIDR.
type ipAllocator struct {
	network *net.IPNet
	used    map[string]bool
	next    uint32
	mu      sync.Mutex
}

func newIPAllocator(cidr string) (*ipAllocator, error) {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("parse CIDR: %w", err)
	}

	return &ipAllocator{
		network: network,
		used:    make(map[string]bool),
		next:    1, // Start from .1, skip .0 (network address)
	}, nil
}

func (a *ipAllocator) allocate() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Get the network address as uint32
	ip := a.network.IP.To4()
	if ip == nil {
		return "", fmt.Errorf("only IPv4 supported")
	}

	base := uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])

	// Find next available IP
	ones, bits := a.network.Mask.Size()
	maxHosts := uint32(1<<(bits-ones)) - 2 // Subtract network and broadcast

	for i := uint32(0); i < maxHosts; i++ {
		candidate := base + a.next
		a.next++
		if a.next > maxHosts {
			a.next = 1
		}

		candidateIP := net.IPv4(
			byte(candidate>>24),
			byte(candidate>>16),
			byte(candidate>>8),
			byte(candidate),
		)

		ipStr := candidateIP.String()
		if !a.used[ipStr] {
			a.used[ipStr] = true
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
		cfg:      cfg,
		mux:      http.NewServeMux(),
		peers:    make(map[string]*peerInfo),
		ipAlloc:  ipAlloc,
		dnsCache: make(map[string]string),
		serverStats: serverStats{
			startTime: time.Now(),
		},
	}

	srv.setupRoutes()
	return srv, nil
}

func (s *Server) setupRoutes() {
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/api/v1/register", s.withAuth(s.handleRegister))
	s.mux.HandleFunc("/api/v1/peers", s.withAuth(s.handlePeers))
	s.mux.HandleFunc("/api/v1/peers/", s.withAuth(s.handlePeerByName))
	s.mux.HandleFunc("/api/v1/heartbeat", s.withAuth(s.handleHeartbeat))
	s.mux.HandleFunc("/api/v1/dns", s.withAuth(s.handleDNS))

	// Setup admin routes if enabled
	if s.cfg.Admin.Enabled {
		s.setupAdminRoutes()
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

	// Check if peer already exists
	existing, exists := s.peers[req.Name]
	var meshIP string

	if exists {
		// Re-use existing IP
		meshIP = existing.peer.MeshIP
	} else {
		// Allocate new IP
		var err error
		meshIP, err = s.ipAlloc.allocate()
		if err != nil {
			s.jsonError(w, "failed to allocate IP: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	peer := &proto.Peer{
		Name:        req.Name,
		PublicKey:   req.PublicKey,
		PublicIPs:   req.PublicIPs,
		PrivateIPs:  req.PrivateIPs,
		SSHPort:     req.SSHPort,
		MeshIP:      meshIP,
		LastSeen:    time.Now(),
		Connectable: len(req.PublicIPs) > 0,
	}

	s.peers[req.Name] = &peerInfo{
		peer:         peer,
		registeredAt: time.Now(),
	}
	s.dnsCache[req.Name] = meshIP

	log.Info().
		Str("name", req.Name).
		Str("mesh_ip", meshIP).
		Msg("peer registered")

	resp := proto.RegisterResponse{
		MeshIP:   meshIP,
		MeshCIDR: s.cfg.MeshCIDR,
		Domain:   s.cfg.DomainSuffix,
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

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req proto.HeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	s.peersMu.Lock()
	info, exists := s.peers[req.Name]
	if exists {
		info.peer.LastSeen = time.Now()
		info.heartbeatCount++
		if req.Stats != nil {
			info.prevStats = info.stats
			info.stats = req.Stats
			info.lastStatsTime = time.Now()
		}
	}
	s.serverStats.totalHeartbeats++
	s.peersMu.Unlock()

	if !exists {
		s.jsonError(w, "peer not found", http.StatusNotFound)
		return
	}

	resp := proto.HeartbeatResponse{OK: true}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
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

// ListenAndServe starts the coordination server.
func (s *Server) ListenAndServe() error {
	log.Info().Str("listen", s.cfg.Listen).Msg("starting coordination server")
	return http.ListenAndServe(s.cfg.Listen, s)
}
