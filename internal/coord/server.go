// Package coord implements the coordination server for tunnelmesh.
package coord

import (
	"context"
	"crypto/rand"
	"crypto/tls"
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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/nfs"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/wireguard"
	"github.com/tunnelmesh/tunnelmesh/internal/mesh"
	"github.com/tunnelmesh/tunnelmesh/internal/tracing"
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
	aliases        []string // DNS aliases registered by this peer

	// Latency metrics reported by peer
	coordinatorRTT int64            // Peer's reported RTT to coordinator (ms)
	peerLatencies  map[string]int64 // Peer's reported latencies to other peers (ms)
}

// serverStats tracks server-level statistics.
type serverStats struct {
	startTime       time.Time
	totalHeartbeats uint64
}

// Server is the coordination server that manages peer registration and discovery.
//
// The server uses two separate HTTP muxes for security isolation:
//   - mux (port 8080): Public API for peer registration, exposed to internet
//   - adminMux (port 443 on mesh IP): Private admin interface, only accessible from within mesh
//
// This separation ensures the admin interface (dashboards, monitoring, config) is never
// exposed to the public internet, while the coordination API remains accessible for peers.
type Server struct {
	cfg          *config.ServerConfig
	mux          *http.ServeMux // Public coordination API (peer registration, heartbeats)
	adminMux     *http.ServeMux // Private admin interface (dashboards, monitoring) - mesh-only
	adminServer  *http.Server   // HTTPS server for adminMux, bound to mesh IP only
	peers        map[string]*peerInfo
	peersMu      sync.RWMutex
	ipAlloc      *ipAllocator
	dnsCache     map[string]string // hostname -> mesh IP
	aliasOwner   map[string]string // alias -> peer name (reverse lookup for ownership)
	serverStats  serverStats
	statsHistory *StatsHistory // Per-peer stats time series
	relay        *relayManager
	holePunch    *holePunchManager
	wgStore      *wireguard.Store      // WireGuard client storage
	ca           *CertificateAuthority // Internal CA for mesh TLS certs
	version      string                // Server version for admin display
	sseHub       *sseHub               // SSE hub for real-time dashboard updates
	ipGeoCache   *IPGeoCache           // IP geolocation cache for location fallback
	coordMeshIP  string                // Coordinator's mesh IP for "this.tunnelmesh" resolution
	coordMetrics *CoordMetrics         // Prometheus metrics for coordinator
	// S3 storage
	s3Store       *s3.Store            // S3 file-based storage
	s3Server      *s3.Server           // S3 HTTP server
	s3Authorizer  *auth.Authorizer     // RBAC authorizer for S3
	s3Credentials *s3.CredentialStore  // S3 credential store
	s3SystemStore *s3.SystemStore      // System bucket accessor
	fileShareMgr  *s3.FileShareManager // File share manager
	// NFS server
	nfsServer *nfs.Server // NFS server for file shares
}

// ipAllocator manages IP address allocation from the mesh CIDR.
// It uses deterministic allocation based on peer name hash for consistency.
type ipAllocator struct {
	network  *net.IPNet
	used     map[string]bool
	peerToIP map[string]string // peer name -> allocated IP (for consistency)
	next     uint32
	mu       sync.Mutex
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
	ipAlloc, err := newIPAllocator(mesh.CIDR)
	if err != nil {
		return nil, fmt.Errorf("create IP allocator: %w", err)
	}

	srv := &Server{
		cfg:          cfg,
		mux:          http.NewServeMux(),
		peers:        make(map[string]*peerInfo),
		ipAlloc:      ipAlloc,
		dnsCache:     make(map[string]string),
		aliasOwner:   make(map[string]string),
		statsHistory: NewStatsHistory(),
		serverStats: serverStats{
			startTime: time.Now(),
		},
		sseHub:       newSSEHub(),
		coordMetrics: nil, // Initialized lazily when SetMetricsRegistry is called
	}

	// Initialize IP geolocation cache if locations feature is enabled
	if cfg.Locations {
		srv.ipGeoCache = NewIPGeoCache("") // Use default ip-api.com URL
		log.Info().Msg("node location tracking enabled (uses external IP geolocation API)")
	}

	// Set user expiration days from config
	if cfg.UserExpirationDays > 0 {
		auth.SetUserExpirationDays(cfg.UserExpirationDays)
	}

	// Initialize WireGuard client store if enabled
	if cfg.WireGuard.Enabled {
		srv.wgStore = wireguard.NewStore(mesh.CIDR)
		log.Info().Msg("WireGuard client management enabled")
	}

	// Initialize S3 storage if enabled
	if cfg.S3.Enabled {
		if err := srv.initS3Storage(cfg); err != nil {
			return nil, fmt.Errorf("initialize S3 storage: %w", err)
		}
	}

	// Initialize Certificate Authority for mesh TLS
	ca, err := NewCertificateAuthority(cfg.DataDir, mesh.DomainSuffix)
	if err != nil {
		return nil, fmt.Errorf("initialize CA: %w", err)
	}
	srv.ca = ca

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

// StartPeriodicCleanup starts the background cleanup goroutine for S3 storage.
// It periodically:
//   - Purges tombstoned objects past their retention period
//   - Tombstones content in expired file shares
//   - Runs garbage collection on versions and orphaned chunks
//   - Updates CAS metrics (dedup ratio, chunk count, etc.)
func (s *Server) StartPeriodicCleanup(ctx context.Context) {
	if s.s3Store == nil {
		return
	}

	// Run cleanup every hour
	ticker := time.NewTicker(1 * time.Hour)
	go func() {
		defer ticker.Stop()

		// Update metrics on startup
		s.updateCASMetrics()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Purge tombstoned objects past retention period
				if purged := s.s3Store.PurgeTombstonedObjects(); purged > 0 {
					log.Info().Int("count", purged).Msg("purged tombstoned S3 objects")
				}

				// Tombstone content in expired file shares
				if s.fileShareMgr != nil {
					if tombstoned := s.fileShareMgr.TombstoneExpiredShareContents(); tombstoned > 0 {
						log.Info().Int("count", tombstoned).Msg("tombstoned expired file share content")
					}
				}

				// Run garbage collection on versions and orphaned chunks
				gcStart := time.Now()
				gcStats := s.s3Store.RunGarbageCollection()
				gcDuration := time.Since(gcStart).Seconds()

				if gcStats.VersionsPruned > 0 || gcStats.ChunksDeleted > 0 {
					log.Info().
						Int("versions_pruned", gcStats.VersionsPruned).
						Int("chunks_deleted", gcStats.ChunksDeleted).
						Int64("bytes_reclaimed", gcStats.BytesReclaimed).
						Int("objects_scanned", gcStats.ObjectsScanned).
						Msg("S3 garbage collection completed")
				}

				// Record GC metrics
				if metrics := s3.GetS3Metrics(); metrics != nil {
					metrics.RecordGCRun(gcStats.VersionsPruned, gcStats.ChunksDeleted, gcStats.BytesReclaimed, gcDuration)
				}

				// Update CAS metrics after GC
				s.updateCASMetrics()
			}
		}
	}()
}

// updateCASMetrics collects and updates CAS/chunking metrics.
func (s *Server) updateCASMetrics() {
	if s.s3Store == nil {
		return
	}

	casStats := s.s3Store.GetCASStats()
	if metrics := s3.GetS3Metrics(); metrics != nil {
		metrics.UpdateCASMetrics(
			casStats.ChunkCount,
			casStats.ChunkBytes,
			casStats.LogicalBytes,
			casStats.VersionCount,
		)
	}
}

// SetVersion sets the server version for admin display.
func (s *Server) SetVersion(version string) {
	s.version = version
}

func (s *Server) setupRoutes() {
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/debug/trace", s.handleTrace)
	s.mux.HandleFunc("/ca.crt", s.handleCACert) // CA cert for mesh TLS (no auth)
	s.mux.Handle("/metrics", promhttp.Handler())
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

		// Setup monitoring reverse proxies if configured
		if s.cfg.Admin.Monitoring.PrometheusURL != "" || s.cfg.Admin.Monitoring.GrafanaURL != "" {
			s.SetupMonitoringProxies(MonitoringProxyConfig{
				PrometheusURL: s.cfg.Admin.Monitoring.PrometheusURL,
				GrafanaURL:    s.cfg.Admin.Monitoring.GrafanaURL,
			})
		}
	}

	// Setup UDP hole-punch coordination routes
	s.setupHolePunchRoutes()

	// Setup WireGuard concentrator sync endpoint (JWT auth)
	if s.cfg.WireGuard.Enabled {
		s.setupWireGuardRoutes()
	}

	// Setup user registration endpoint (if S3 is enabled for user management)
	if s.cfg.S3.Enabled {
		s.mux.HandleFunc("/api/v1/user/register", s.handleUserRegister)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// NFSPort is the standard NFS port used for file share access.
const NFSPort = 2049

// getServicePorts returns the list of service ports the coordinator exposes.
// These ports are pushed to peers so they can auto-allow access through the packet filter.
func (s *Server) getServicePorts() []uint16 {
	var ports []uint16

	// Admin dashboard port (if enabled)
	if s.cfg.Admin.Enabled && s.cfg.Admin.Port > 0 {
		ports = append(ports, uint16(s.cfg.Admin.Port))
	}

	// S3 port (if enabled)
	if s.cfg.S3.Enabled && s.cfg.S3.Port > 0 {
		ports = append(ports, uint16(s.cfg.S3.Port))
	}

	// NFS port (only when there are active file shares)
	if s.fileShareMgr != nil && len(s.fileShareMgr.List()) > 0 {
		ports = append(ports, NFSPort)
	}

	// Configured service ports (includes metrics port 9443 by default)
	ports = append(ports, s.cfg.ServicePorts...)

	return ports
}

// loadOrCreateCASKey loads or creates the master key for CAS encryption.
// The key is stored in the S3 data directory as cas.key.
func (s *Server) loadOrCreateCASKey(dataDir string) ([32]byte, error) {
	keyPath := filepath.Join(dataDir, "cas.key")
	var masterKey [32]byte

	// Try to load existing key
	data, err := os.ReadFile(keyPath)
	if err == nil && len(data) == 32 {
		copy(masterKey[:], data)
		return masterKey, nil
	}

	// Create new key
	if _, err := rand.Read(masterKey[:]); err != nil {
		return masterKey, fmt.Errorf("generate CAS key: %w", err)
	}

	// Ensure data directory exists
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return masterKey, fmt.Errorf("create data directory: %w", err)
	}

	// Save key to disk
	if err := os.WriteFile(keyPath, masterKey[:], 0o600); err != nil {
		return masterKey, fmt.Errorf("save CAS key: %w", err)
	}

	log.Info().Str("path", keyPath).Msg("generated new CAS encryption key")
	return masterKey, nil
}

// initS3Storage initializes the S3 storage subsystem.
func (s *Server) initS3Storage(cfg *config.ServerConfig) error {
	// Require max_size to be configured for quota enforcement
	if cfg.S3.MaxSize.Bytes() <= 0 {
		return fmt.Errorf("s3.max_size must be configured (e.g., 10Gi) for quota enforcement")
	}
	quota := s3.NewQuotaManager(cfg.S3.MaxSize.Bytes())

	// Load or create master key for CAS encryption
	masterKey, err := s.loadOrCreateCASKey(cfg.S3.DataDir)
	if err != nil {
		return fmt.Errorf("initialize CAS key: %w", err)
	}

	// Create store with CAS for content-addressed storage
	store, err := s3.NewStoreWithCAS(cfg.S3.DataDir, quota, masterKey)
	if err != nil {
		return fmt.Errorf("create S3 store: %w", err)
	}
	s.s3Store = store

	// Set expiry defaults from config
	store.SetDefaultObjectExpiryDays(cfg.S3.ObjectExpiryDays)
	store.SetDefaultShareExpiryDays(cfg.S3.ShareExpiryDays)

	// Set version retention config
	store.SetVersionRetentionDays(cfg.S3.VersionRetentionDays)
	store.SetMaxVersionsPerObject(cfg.S3.MaxVersionsPerObject)
	store.SetVersionRetentionPolicy(s3.VersionRetentionPolicy{
		RecentDays:    cfg.S3.VersionRetention.RecentDays,
		WeeklyWeeks:   cfg.S3.VersionRetention.WeeklyWeeks,
		MonthlyMonths: cfg.S3.VersionRetention.MonthlyMonths,
	})

	// Create authorizer with group support
	s.s3Authorizer = auth.NewAuthorizerWithGroups()

	// Create credential store
	s.s3Credentials = s3.NewCredentialStore()

	// Create RBAC authorizer for S3
	rbacAuth := s3.NewRBACAuthorizer(s.s3Credentials, s.s3Authorizer)

	// Initialize S3 metrics
	s3Metrics := s3.InitS3Metrics(nil)

	// Create S3 server
	s.s3Server = s3.NewServer(store, rbacAuth, s3Metrics)

	// Create system store for internal coordinator data
	// Use a service user ID for the coordinator
	serviceUserID := auth.ServiceUserPrefix + "coordinator"
	systemStore, err := s3.NewSystemStore(store, serviceUserID)
	if err != nil {
		return fmt.Errorf("create system store: %w", err)
	}
	s.s3SystemStore = systemStore

	// Recover users and credentials from previous runs
	if users, err := systemStore.LoadUsers(); err == nil && len(users) > 0 {
		log.Info().Int("count", len(users)).Msg("recovering registered users")
		for _, user := range users {
			if _, _, err := s.s3Credentials.RegisterUser(user.ID, user.PublicKey); err != nil {
				log.Warn().Err(err).Str("user", user.ID).Msg("failed to recover user credentials")
			}
		}
	}

	// Recover role bindings
	if bindings, err := systemStore.LoadBindings(); err == nil && len(bindings) > 0 {
		log.Info().Int("count", len(bindings)).Msg("recovering role bindings")
		for _, binding := range bindings {
			s.s3Authorizer.Bindings.Add(binding)
		}
	}

	// Recover groups
	if groups, err := systemStore.LoadGroups(); err == nil && len(groups) > 0 {
		log.Info().Int("count", len(groups)).Msg("recovering groups")
		s.s3Authorizer.Groups.LoadGroups(groups)
	}

	// Recover group bindings
	if groupBindings, err := systemStore.LoadGroupBindings(); err == nil && len(groupBindings) > 0 {
		log.Info().Int("count", len(groupBindings)).Msg("recovering group bindings")
		s.s3Authorizer.GroupBindings.LoadBindings(groupBindings)
	}

	// Set up built-in group bindings if not already present
	s.ensureBuiltinGroupBindings()

	// Initialize file share manager
	s.fileShareMgr = s3.NewFileShareManager(store, systemStore, s.s3Authorizer)
	log.Info().Int("shares", len(s.fileShareMgr.List())).Msg("file share manager initialized")

	// Add coordinator service user to all_service_users group
	_ = s.s3Authorizer.Groups.AddMember(auth.GroupAllServiceUsers, serviceUserID)

	// Register service user credentials (derived from a fixed key for now)
	// In production, this would be derived from the CA private key
	if _, _, err := s.s3Credentials.RegisterUser(serviceUserID, serviceUserID); err != nil {
		return fmt.Errorf("register service user credentials: %w", err)
	}

	log.Info().
		Str("data_dir", cfg.S3.DataDir).
		Int("port", cfg.S3.Port).
		Str("max_size", cfg.S3.MaxSize.String()).
		Msg("S3 storage initialized")

	return nil
}

// ensureBuiltinGroupBindings sets up the built-in group bindings if not already present.
// - all_service_users group gets system role on _tunnelmesh bucket
// - all_admin_users group gets admin role (unscoped)
func (s *Server) ensureBuiltinGroupBindings() {
	modified := false

	// Check if bindings already exist
	serviceBindings := s.s3Authorizer.GroupBindings.GetForGroup(auth.GroupAllServiceUsers)
	if len(serviceBindings) == 0 {
		s.s3Authorizer.GroupBindings.Add(auth.NewGroupBinding(
			auth.GroupAllServiceUsers,
			auth.RoleSystem,
			"", // Unscoped, but RoleSystem only applies to _tunnelmesh
		))
		modified = true
	}

	adminBindings := s.s3Authorizer.GroupBindings.GetForGroup(auth.GroupAllAdminUsers)
	if len(adminBindings) == 0 {
		s.s3Authorizer.GroupBindings.Add(auth.NewGroupBinding(
			auth.GroupAllAdminUsers,
			auth.RoleAdmin,
			"", // Unscoped - admin has access to all buckets
		))
		modified = true
	}

	// Persist if we added any bindings
	if modified && s.s3SystemStore != nil {
		if err := s.s3SystemStore.SaveGroupBindings(s.s3Authorizer.GroupBindings.List()); err != nil {
			log.Warn().Err(err).Msg("failed to persist builtin group bindings")
		}
	}
}

// StartS3Server starts the S3 API server on the specified address.
// This should be called after join_mesh completes to bind to the mesh IP.
func (s *Server) StartS3Server(addr string, tlsCert *tls.Certificate) error {
	if s.s3Server == nil {
		return fmt.Errorf("S3 storage not initialized")
	}

	// Create listener with SO_REUSEADDR
	lc := net.ListenConfig{
		Control: setReuseAddr,
	}

	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	server := &http.Server{
		Handler: s.s3Server.Handler(),
	}

	if tlsCert != nil {
		server.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{*tlsCert},
			MinVersion:   tls.VersionTLS12,
		}
		log.Info().Str("addr", addr).Msg("starting S3 server (HTTPS)")
		go func() {
			if err := server.ServeTLS(ln, "", ""); err != nil && err != http.ErrServerClosed {
				log.Error().Err(err).Msg("S3 server error")
			}
		}()
	} else {
		log.Info().Str("addr", addr).Msg("starting S3 server (HTTP)")
		go func() {
			if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Error().Err(err).Msg("S3 server error")
			}
		}()
	}

	return nil
}

// StartNFSServer starts the NFS server on the specified address.
// This should be called after join_mesh completes to bind to the mesh IP.
func (s *Server) StartNFSServer(addr string, tlsCert *tls.Certificate) error {
	if s.s3Store == nil || s.fileShareMgr == nil {
		return fmt.Errorf("S3 storage not initialized")
	}

	// Create NFS password store
	passwords := nfs.NewPasswordStore()

	// Create TLS config if certificate provided
	var tlsConfig *tls.Config
	if tlsCert != nil {
		tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{*tlsCert},
			MinVersion:   tls.VersionTLS12,
		}
	}

	// Create and start NFS server
	s.nfsServer = nfs.NewServer(
		s.s3Store,
		s.fileShareMgr,
		s.s3Authorizer,
		passwords,
		nfs.Config{
			Address:   addr,
			TLSConfig: tlsConfig,
		},
	)

	if err := s.nfsServer.Start(); err != nil {
		return fmt.Errorf("start NFS server: %w", err)
	}

	return nil
}

// RegisterS3User registers a user for S3 access.
// Returns the user's S3 access key and secret key.
func (s *Server) RegisterS3User(userID, publicKey string, roles []string) (accessKey, secretKey string, err error) {
	if s.s3Credentials == nil {
		return "", "", fmt.Errorf("S3 storage not initialized")
	}

	// Register credentials
	accessKey, secretKey, err = s.s3Credentials.RegisterUser(userID, publicKey)
	if err != nil {
		return "", "", fmt.Errorf("register S3 credentials: %w", err)
	}

	// Bind roles
	for _, role := range roles {
		s.s3Authorizer.Bindings.Add(&auth.RoleBinding{
			Name:     fmt.Sprintf("%s-%s", userID, role),
			UserID:   userID,
			RoleName: role,
		})
	}

	return accessKey, secretKey, nil
}

// S3SystemStore returns the system store for internal coordinator data.
// Returns nil if S3 is not enabled.
func (s *Server) S3SystemStore() *s3.SystemStore {
	return s.s3SystemStore
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

// handleTrace returns a runtime trace snapshot.
// The output is compatible with `go tool trace`.
func (s *Server) handleTrace(w http.ResponseWriter, _ *http.Request) {
	if !tracing.Enabled() {
		http.Error(w, "tracing not enabled (use --enable-tracing flag)", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=trace.out")

	if err := tracing.Snapshot(w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// validateAliases checks if all aliases are valid and available for the requesting peer.
// Returns an error if any alias conflicts. Must be called with peersMu held.
func (s *Server) validateAliases(aliases []string, requestingPeer string) error {
	for _, alias := range aliases {
		// Can't use another peer's name
		if _, exists := s.peers[alias]; exists && alias != requestingPeer {
			return fmt.Errorf("alias %q conflicts with existing peer name", alias)
		}

		// Can't use another peer's alias
		if owner, exists := s.aliasOwner[alias]; exists && owner != requestingPeer {
			return fmt.Errorf("alias %q is already registered by peer %q", alias, owner)
		}
	}
	return nil
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

	// Validate aliases first (before any state changes)
	if len(req.Aliases) > 0 {
		if err := s.validateAliases(req.Aliases, req.Name); err != nil {
			s.jsonError(w, err.Error(), http.StatusConflict)
			return
		}
	}

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
		Name:              req.Name,
		PublicKey:         req.PublicKey,
		PublicIPs:         req.PublicIPs,
		PrivateIPs:        req.PrivateIPs,
		SSHPort:           req.SSHPort,
		UDPPort:           req.UDPPort,
		MeshIP:            meshIP,
		LastSeen:          time.Now(),
		Connectable:       len(req.PublicIPs) > 0 && !req.BehindNAT,
		BehindNAT:         req.BehindNAT,
		Version:           req.Version,
		Location:          location,
		AllowsExitTraffic: req.AllowsExitTraffic,
		ExitNode:          req.ExitNode,
	}

	// Preserve registeredAt for existing peers
	registeredAt := time.Now()
	if isExisting {
		registeredAt = existing.registeredAt
		// Clean up old aliases before registering new ones
		for _, oldAlias := range existing.aliases {
			delete(s.aliasOwner, oldAlias)
			delete(s.dnsCache, oldAlias)
		}
	}

	// Register new aliases
	for _, alias := range req.Aliases {
		s.aliasOwner[alias] = req.Name
		s.dnsCache[alias] = meshIP
	}

	s.peers[req.Name] = &peerInfo{
		peer:         peer,
		registeredAt: registeredAt,
		aliases:      req.Aliases,
	}
	s.dnsCache[req.Name] = meshIP

	// Generate JWT token for relay authentication
	token, err := s.GenerateToken(req.Name, meshIP)
	if err != nil {
		s.jsonError(w, "failed to generate token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	logEvent := log.Info().
		Str("name", req.Name).
		Str("mesh_ip", meshIP)
	if len(req.Aliases) > 0 {
		logEvent = logEvent.Strs("aliases", req.Aliases)
	}
	logEvent.Msg("peer registered")

	// Trigger IP geolocation only for new peers or when IP has changed (if locations enabled)
	if needsGeoLookup && s.cfg.Locations && s.ipGeoCache != nil {
		go s.lookupPeerLocation(req.Name, geoLookupIP)
	}

	resp := proto.RegisterResponse{
		MeshIP:        meshIP,
		MeshCIDR:      mesh.CIDR,
		Domain:        mesh.DomainSuffix,
		Token:         token,
		CoordMeshIP:   s.coordMeshIP, // For "this.tunnelmesh" resolution
		ServerVersion: s.version,
	}

	// Generate TLS certificate for the peer
	if s.ca != nil {
		certPEM, keyPEM, err := s.ca.GeneratePeerCert(req.Name, mesh.DomainSuffix, meshIP)
		if err != nil {
			log.Warn().Err(err).Str("peer", req.Name).Msg("failed to generate TLS cert")
		} else {
			resp.TLSCert = string(certPEM)
			resp.TLSKey = string(keyPEM)
		}
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
			// Clean up aliases
			for _, alias := range info.aliases {
				delete(s.aliasOwner, alias)
				delete(s.dnsCache, alias)
			}
			delete(s.peers, name)
			delete(s.dnsCache, name)
		}
		s.peersMu.Unlock()

		// Also remove UDP endpoint
		if s.holePunch != nil {
			s.holePunch.RemoveEndpoint(name)
		}

		// Clean up stats history for the peer
		if s.statsHistory != nil {
			s.statsHistory.CleanupPeer(name)
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

// SetCoordMeshIP sets the coordinator's mesh IP for "this.tunnelmesh" resolution.
// This is called after join_mesh completes so other peers can resolve "this" to the coordinator.
func (s *Server) SetCoordMeshIP(ip string) {
	s.coordMeshIP = ip
	log.Info().Str("ip", ip).Msg("coordinator mesh IP set for 'this.tunnelmesh' resolution")
}

// SetMetricsRegistry initializes coordinator metrics with the given registry.
// This should be called after the server is created if you want coordinator
// metrics to be exposed on a specific registry (e.g., metrics.Registry for
// the peer /metrics endpoint).
func (s *Server) SetMetricsRegistry(registry prometheus.Registerer) {
	s.coordMetrics = InitCoordMetrics(registry)
	log.Debug().Msg("coordinator metrics initialized")
}

// StartAdminServer starts the admin interface on the specified address.
// If tlsCert is provided, the server uses HTTPS; otherwise HTTP.
// This is called after join_mesh completes to bind admin to the mesh IP.
// Uses SO_REUSEADDR to allow binding to mesh IP even when main server uses wildcard.
func (s *Server) StartAdminServer(addr string, tlsCert *tls.Certificate) error {
	if s.adminMux == nil {
		return fmt.Errorf("admin routes not initialized")
	}

	// Create listener with SO_REUSEADDR to allow binding to specific IP
	// even when main server is bound to 0.0.0.0 on the same port
	lc := net.ListenConfig{
		Control: setReuseAddr,
	}

	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	s.adminServer = &http.Server{
		Handler: redirectToCanonicalDomain(s.adminMux),
	}

	if tlsCert != nil {
		s.adminServer.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{*tlsCert},
			MinVersion:   tls.VersionTLS12,
		}
		log.Info().Str("addr", addr).Msg("starting admin server (HTTPS)")
		go func() {
			if err := s.adminServer.ServeTLS(ln, "", ""); err != nil && err != http.ErrServerClosed {
				log.Error().Err(err).Msg("admin server error")
			}
		}()
	} else {
		log.Info().Str("addr", addr).Msg("starting admin server (HTTP)")
		go func() {
			if err := s.adminServer.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Error().Err(err).Msg("admin server error")
			}
		}()
	}

	return nil
}

// GetCA returns the server's certificate authority.
func (s *Server) GetCA() *CertificateAuthority {
	return s.ca
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

// handleUserRegister handles user registration requests.
// This endpoint allows users to register their identity with the mesh.
// The first human user to register becomes an admin.
func (s *Server) handleUserRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req proto.UserRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if req.UserID == "" || req.PublicKey == "" {
		s.jsonError(w, "user_id and public_key are required", http.StatusBadRequest)
		return
	}

	// Verify the signature to prove ownership of the private key
	if !auth.VerifyUserSignature(req.PublicKey, req.UserID, req.Signature) {
		s.jsonError(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Load existing users to check for expired users
	var existingUsers []*auth.User
	var existingUser *auth.User
	if s.s3SystemStore != nil {
		existingUsers, _ = s.s3SystemStore.LoadUsers()
		for _, u := range existingUsers {
			if u.ID == req.UserID {
				existingUser = u
				break
			}
		}
	}

	// Check if user is already registered and not expired
	existingBindings := s.s3Authorizer.Bindings.GetForUser(req.UserID)
	existingGroups := s.s3Authorizer.Groups.GetGroupsForUser(req.UserID)
	isActiveUser := (len(existingBindings) > 0 || len(existingGroups) > 0) &&
		(existingUser == nil || !existingUser.IsExpired())

	if isActiveUser {
		// User already registered and active - return existing info
		roles := make([]string, 0, len(existingBindings))
		for _, b := range existingBindings {
			roles = append(roles, b.RoleName)
		}

		// Update LastSeen
		if existingUser != nil {
			existingUser.LastSeen = time.Now().UTC()
			if s.s3SystemStore != nil {
				_ = s.s3SystemStore.SaveUsers(existingUsers)
			}
		}

		// Get existing S3 credentials
		accessKey, secretKey, _ := s.s3Credentials.RegisterUser(req.UserID, req.PublicKey)

		resp := proto.UserRegisterResponse{
			UserID:      req.UserID,
			Roles:       roles,
			Groups:      existingGroups,
			S3AccessKey: accessKey,
			S3SecretKey: secretKey,
			IsFirstUser: false,
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	// Determine if this is the first user
	isFirstUser := !s.s3Authorizer.HasHumanAdmin()

	// Register S3 credentials
	accessKey, secretKey, err := s.s3Credentials.RegisterUser(req.UserID, req.PublicKey)
	if err != nil {
		s.jsonError(w, "failed to register S3 credentials", http.StatusInternalServerError)
		return
	}

	// Add user to appropriate groups
	var groups []string
	if strings.HasPrefix(req.UserID, auth.ServiceUserPrefix) {
		// Service users go to all_service_users group
		_ = s.s3Authorizer.Groups.AddMember(auth.GroupAllServiceUsers, req.UserID)
		groups = append(groups, auth.GroupAllServiceUsers)
	} else {
		// Human users go to everyone group
		_ = s.s3Authorizer.Groups.AddMember(auth.GroupEveryone, req.UserID)
		groups = append(groups, auth.GroupEveryone)

		if isFirstUser {
			// First human user also becomes admin
			_ = s.s3Authorizer.Groups.AddMember(auth.GroupAllAdminUsers, req.UserID)
			groups = append(groups, auth.GroupAllAdminUsers)
			log.Info().Str("user_id", req.UserID).Msg("first user registered as admin")
		}
	}

	// Create or update user record
	now := time.Now().UTC()
	isReactivation := existingUser != nil && existingUser.IsExpired()

	if existingUser != nil {
		// Update existing user (reactivation case)
		existingUser.Name = req.Name
		existingUser.LastSeen = now
		existingUser.Expired = false
		existingUser.ExpiredAt = time.Time{}
	} else {
		// Create new user record
		user := &auth.User{
			ID:        req.UserID,
			Name:      req.Name,
			PublicKey: req.PublicKey,
			CreatedAt: now,
			LastSeen:  now,
		}
		existingUsers = append(existingUsers, user)
	}

	// Save user and groups to system store if available
	if s.s3SystemStore != nil {
		if err := s.s3SystemStore.SaveUsers(existingUsers); err != nil {
			log.Warn().Err(err).Str("user_id", req.UserID).Msg("failed to persist user")
		}

		// Save groups
		if err := s.s3SystemStore.SaveGroups(s.s3Authorizer.Groups.List()); err != nil {
			log.Warn().Err(err).Msg("failed to persist groups")
		}
	}

	if isReactivation {
		log.Info().
			Str("user_id", req.UserID).
			Str("name", req.Name).
			Strs("groups", groups).
			Msg("expired user reactivated")
	} else {
		log.Info().
			Str("user_id", req.UserID).
			Str("name", req.Name).
			Strs("groups", groups).
			Bool("is_first_user", isFirstUser).
			Msg("user registered")
	}

	resp := proto.UserRegisterResponse{
		UserID:      req.UserID,
		Groups:      groups,
		S3AccessKey: accessKey,
		S3SecretKey: secretKey,
		IsFirstUser: isFirstUser,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
