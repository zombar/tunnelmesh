// tunnelmesh is the P2P SSH tunnel mesh network tool.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/coord"
	meshdns "github.com/tunnelmesh/tunnelmesh/internal/dns"
	"github.com/tunnelmesh/tunnelmesh/internal/negotiate"
	"github.com/tunnelmesh/tunnelmesh/internal/netmon"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
	"github.com/tunnelmesh/tunnelmesh/internal/svc"
	"github.com/tunnelmesh/tunnelmesh/internal/tun"
	"github.com/tunnelmesh/tunnelmesh/internal/tunnel"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
	"golang.org/x/crypto/ssh"
)

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

var (
	cfgFile   string
	logLevel  string
	serverURL string
	authToken string
	nodeName  string

	// Service mode flags (hidden, used when running as a service)
	serviceRun     bool
	serviceRunMode string
)

func main() {
	// Check if running as a service (invoked by service manager)
	if svc.IsServiceMode(os.Args) {
		runAsService()
		return
	}

	rootCmd := &cobra.Command{
		Use:   "tunnelmesh",
		Short: "TunnelMesh - P2P SSH tunnel mesh network",
		Long: `TunnelMesh creates encrypted P2P tunnels between peers using SSH.

Use 'tunnelmesh serve' to run a coordination server.
Use 'tunnelmesh join' to connect as a peer.
Use 'tunnelmesh service' to manage system service installation.`,
	}

	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file path")
	rootCmd.PersistentFlags().StringVarP(&logLevel, "log-level", "l", "info", "log level")

	// Hidden service mode flags (used when running as a service)
	rootCmd.PersistentFlags().BoolVar(&serviceRun, "service-run", false, "Run as a service (internal use)")
	rootCmd.PersistentFlags().StringVar(&serviceRunMode, "service-mode", "", "Service mode: serve or join (internal use)")
	_ = rootCmd.PersistentFlags().MarkHidden("service-run")
	_ = rootCmd.PersistentFlags().MarkHidden("service-mode")

	// Serve command - run coordination server
	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the coordination server",
		Long: `Start the TunnelMesh coordination server.

The server manages peer registration, discovery, and DNS records.
It does not route traffic - peers connect directly to each other.`,
		RunE: runServe,
	}
	rootCmd.AddCommand(serveCmd)

	// Join command
	joinCmd := &cobra.Command{
		Use:   "join",
		Short: "Join the mesh network",
		Long:  "Register with the coordination server and start the mesh daemon",
		RunE:  runJoin,
	}
	joinCmd.Flags().StringVarP(&serverURL, "server", "s", "", "coordination server URL")
	joinCmd.Flags().StringVarP(&authToken, "token", "t", "", "authentication token")
	joinCmd.Flags().StringVarP(&nodeName, "name", "n", "", "node name")
	rootCmd.AddCommand(joinCmd)

	// Status command
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show mesh status",
		RunE:  runStatus,
	}
	rootCmd.AddCommand(statusCmd)

	// Peers command
	peersCmd := &cobra.Command{
		Use:   "peers",
		Short: "List mesh peers",
		RunE:  runPeers,
	}
	rootCmd.AddCommand(peersCmd)

	// Resolve command
	resolveCmd := &cobra.Command{
		Use:   "resolve <hostname>",
		Short: "Resolve a mesh hostname",
		Args:  cobra.ExactArgs(1),
		RunE:  runResolve,
	}
	rootCmd.AddCommand(resolveCmd)

	// Leave command
	leaveCmd := &cobra.Command{
		Use:   "leave",
		Short: "Leave the mesh network",
		RunE:  runLeave,
	}
	rootCmd.AddCommand(leaveCmd)

	// Version command
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("tunnelmesh %s\n", Version)
			fmt.Printf("  Commit:     %s\n", Commit)
			fmt.Printf("  Build Time: %s\n", BuildTime)
		},
	}
	rootCmd.AddCommand(versionCmd)

	// Init command - generate keys
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize tunnelmesh (generate keys)",
		RunE:  runInit,
	}
	rootCmd.AddCommand(initCmd)

	// Service command - manage system service
	rootCmd.AddCommand(newServiceCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// runAsService runs the application as a system service.
// This is called when the service manager starts the application with --service-run flag.
func runAsService() {
	// Set up logging directly to a file since launchd/kardianos-service
	// may not properly redirect stderr
	setupServiceLogging()

	// Parse the service-specific flags manually
	var mode, configPath string
	for i, arg := range os.Args {
		if arg == "--service-mode" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
		}
		if (arg == "--config" || arg == "-c") && i+1 < len(os.Args) {
			configPath = os.Args[i+1]
		}
	}

	if mode == "" {
		log.Fatal().Msg("service mode not specified")
	}
	if configPath == "" {
		configPath = svc.DefaultConfigPath(mode)
	}

	log.Info().
		Str("mode", mode).
		Str("config", configPath).
		Msg("starting as service")

	// Create service configuration
	cfg := &svc.ServiceConfig{
		Name:        svc.DefaultServiceName(mode),
		DisplayName: svc.DefaultDisplayName(mode),
		Description: svc.DefaultDescription(mode),
		Mode:        mode,
		ConfigPath:  configPath,
	}

	// Create program with runner functions
	prg := &svc.Program{
		Mode:       mode,
		ConfigPath: configPath,
		RunServe:   runServeFromService,
		RunJoin:    runJoinFromService,
	}

	// Run as service
	if err := svc.Run(prg, cfg); err != nil {
		log.Fatal().Err(err).Msg("service error")
	}
}

// runServeFromService runs the server mode from within a service.
func runServeFromService(ctx context.Context, configPath string) error {
	var cfg *config.ServerConfig
	var err error

	if configPath != "" {
		cfg, err = config.LoadServerConfig(configPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
	} else {
		return fmt.Errorf("config file required")
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	srv, err := coord.NewServer(cfg)
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}

	log.Info().
		Str("listen", cfg.Listen).
		Str("mesh_cidr", cfg.MeshCIDR).
		Str("domain", cfg.DomainSuffix).
		Msg("starting coordination server")

	// Start HTTP server in goroutine
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			log.Error().Err(err).Msg("server error")
		}
	}()

	// If JoinMesh is configured, join the mesh as a client
	if cfg.JoinMesh != nil {
		cfg.JoinMesh.Server = "http://127.0.0.1" + cfg.Listen
		cfg.JoinMesh.AuthToken = cfg.AuthToken

		log.Info().
			Str("name", cfg.JoinMesh.Name).
			Msg("joining mesh as client")

		go func() {
			time.Sleep(500 * time.Millisecond)
			if err := runJoinWithConfig(ctx, cfg.JoinMesh); err != nil {
				log.Error().Err(err).Msg("failed to join mesh as client")
			}
		}()
	}

	// Wait for context cancellation (service stop)
	<-ctx.Done()
	return nil
}

// runJoinFromService runs the peer mode from within a service.
func runJoinFromService(ctx context.Context, configPath string) error {
	log.Info().Str("config_path", configPath).Msg("runJoinFromService starting")

	var cfg *config.PeerConfig
	var err error

	if configPath != "" {
		cfg, err = config.LoadPeerConfig(configPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
	} else {
		return fmt.Errorf("config file required")
	}

	log.Info().
		Str("server", cfg.Server).
		Str("name", cfg.Name).
		Int("ssh_port", cfg.SSHPort).
		Msg("config loaded")

	if cfg.Server == "" || cfg.AuthToken == "" || cfg.Name == "" {
		return fmt.Errorf("server, token, and name are required in config")
	}

	// Log network interface information for debugging
	interfaces, _ := net.Interfaces()
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0 {
			addrs, _ := iface.Addrs()
			for _, addr := range addrs {
				log.Debug().
					Str("interface", iface.Name).
					Str("address", addr.String()).
					Msg("network interface")
			}
		}
	}

	return runJoinWithConfig(ctx, cfg)
}

func runServe(cmd *cobra.Command, args []string) error {
	setupLogging()

	var cfg *config.ServerConfig
	var err error

	if cfgFile != "" {
		cfg, err = config.LoadServerConfig(cfgFile)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
	} else {
		// Try default locations
		defaults := []string{"server.yaml", "tunnelmesh-server.yaml"}
		for _, path := range defaults {
			if _, err := os.Stat(path); err == nil {
				cfg, err = config.LoadServerConfig(path)
				if err != nil {
					return fmt.Errorf("load config: %w", err)
				}
				break
			}
		}
		if cfg == nil {
			return fmt.Errorf("config file required (use --config or create server.yaml)")
		}
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	srv, err := coord.NewServer(cfg)
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}

	// Create context for shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Info().Msg("shutting down...")
		cancel()
	}()

	log.Info().
		Str("listen", cfg.Listen).
		Str("mesh_cidr", cfg.MeshCIDR).
		Str("domain", cfg.DomainSuffix).
		Msg("starting coordination server")

	// Start HTTP server in goroutine
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			log.Fatal().Err(err).Msg("server error")
		}
	}()

	// If JoinMesh is configured, join the mesh as a client
	if cfg.JoinMesh != nil {
		// Set server URL to localhost and copy auth token
		cfg.JoinMesh.Server = "http://127.0.0.1" + cfg.Listen
		cfg.JoinMesh.AuthToken = cfg.AuthToken

		log.Info().
			Str("name", cfg.JoinMesh.Name).
			Msg("joining mesh as client")

		// Run join in a goroutine so we can handle shutdown
		go func() {
			// Small delay to ensure server is ready
			time.Sleep(500 * time.Millisecond)
			if err := runJoinWithConfig(ctx, cfg.JoinMesh); err != nil {
				log.Error().Err(err).Msg("failed to join mesh as client")
			}
		}()
	}

	// Wait for shutdown
	<-ctx.Done()
	return nil
}

func runInit(cmd *cobra.Command, args []string) error {
	setupLogging()

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}

	keyDir := filepath.Join(homeDir, ".tunnelmesh")
	keyPath := filepath.Join(keyDir, "id_ed25519")

	// Check if key already exists
	if _, err := os.Stat(keyPath); err == nil {
		log.Info().Str("path", keyPath).Msg("keys already exist")
		return nil
	}

	if err := config.GenerateKeyPair(keyPath); err != nil {
		return fmt.Errorf("generate keys: %w", err)
	}

	log.Info().Str("path", keyPath).Msg("keys generated")
	log.Info().Str("path", keyPath+".pub").Msg("public key")
	return nil
}

func runJoin(cmd *cobra.Command, args []string) error {
	setupLogging()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	// Override with flags
	if serverURL != "" {
		cfg.Server = serverURL
	}
	if authToken != "" {
		cfg.AuthToken = authToken
	}
	if nodeName != "" {
		cfg.Name = nodeName
	}

	if cfg.Server == "" || cfg.AuthToken == "" || cfg.Name == "" {
		return fmt.Errorf("server, token, and name are required")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Info().Msg("shutting down...")
		cancel()
	}()

	return runJoinWithConfig(ctx, cfg)
}

func runJoinWithConfig(ctx context.Context, cfg *config.PeerConfig) error {
	if cfg.Server == "" || cfg.AuthToken == "" || cfg.Name == "" {
		return fmt.Errorf("server, token, and name are required")
	}

	// Ensure keys exist
	signer, err := config.EnsureKeyPairExists(cfg.PrivateKey)
	if err != nil {
		return fmt.Errorf("load keys: %w", err)
	}

	pubKeyEncoded := config.EncodePublicKey(signer.PublicKey())
	pubKeyFP := config.GetPublicKeyFingerprint(signer.PublicKey())
	log.Info().Str("fingerprint", pubKeyFP).Msg("using SSH key")

	// Get local IPs
	publicIPs, privateIPs, behindNAT := proto.GetLocalIPs()
	log.Debug().
		Strs("public", publicIPs).
		Strs("private", privateIPs).
		Bool("behind_nat", behindNAT).
		Msg("detected local IPs")

	// Connect to coordination server
	client := coord.NewClient(cfg.Server, cfg.AuthToken)

	resp, err := client.Register(cfg.Name, pubKeyEncoded, publicIPs, privateIPs, cfg.SSHPort, behindNAT)
	if err != nil {
		return fmt.Errorf("register with server: %w", err)
	}

	log.Info().
		Str("mesh_ip", resp.MeshIP).
		Str("mesh_cidr", resp.MeshCIDR).
		Str("domain", resp.Domain).
		Msg("joined mesh network")

	// Create TUN device
	tunCfg := tun.Config{
		Name:    cfg.TUN.Name,
		MTU:     cfg.TUN.MTU,
		Address: resp.MeshIP + "/" + strings.Split(resp.MeshCIDR, "/")[1],
	}

	log.Info().
		Str("name", tunCfg.Name).
		Int("mtu", tunCfg.MTU).
		Str("address", tunCfg.Address).
		Msg("creating TUN device")

	tunDev, err := tun.Create(tunCfg)
	if err != nil {
		log.Error().Err(err).Msg("failed to create TUN device (requires root)")
		// Continue without TUN for now
	} else {
		defer tunDev.Close()
		log.Info().
			Str("name", tunDev.Name()).
			Str("ip", resp.MeshIP).
			Msg("TUN device created")
	}

	// Initialize routing and tunnel management
	router := routing.NewRouter()
	tunnelMgr := NewTunnelAdapter()
	forwarder := routing.NewForwarder(router, tunnelMgr)
	if tunDev != nil {
		forwarder.SetTUN(tunDev)
		forwarder.SetLocalIP(net.ParseIP(resp.MeshIP))
	}

	// Create SSH server for incoming connections
	sshServer := tunnel.NewSSHServer(signer, nil) // Will add authorized keys dynamically

	// Start SSH listener
	sshListener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.SSHPort))
	if err != nil {
		return fmt.Errorf("start SSH listener: %w", err)
	}
	defer sshListener.Close()
	log.Info().Int("port", cfg.SSHPort).Msg("SSH server listening")

	// Handle incoming SSH connections
	go handleSSHConnections(ctx, sshListener, sshServer, tunnelMgr, forwarder, router, client)

	// Create negotiator for outbound connections
	negotiator := negotiate.NewNegotiator(negotiate.Config{
		ProbeTimeout: 5 * time.Second,
		MaxRetries:   3,
		RetryDelay:   1 * time.Second,
		AllowReverse: true,
		AllowRelay:   true, // Enable relay through coordination server
	})

	// Create SSH client for outbound connections
	sshClient := tunnel.NewSSHClient(signer, nil) // Accept any host key for now

	// Start DNS resolver if enabled
	var resolver *meshdns.Resolver
	var dnsConfigured bool
	if cfg.DNS.Enabled {
		resolver = meshdns.NewResolver(resp.Domain, cfg.DNS.CacheTTL)

		// Initial DNS sync
		if err := syncDNS(client, resolver); err != nil {
			log.Warn().Err(err).Msg("failed to sync DNS")
		}

		go func() {
			if err := resolver.ListenAndServe(cfg.DNS.Listen); err != nil {
				log.Error().Err(err).Msg("DNS server error")
			}
		}()
		log.Info().Str("listen", cfg.DNS.Listen).Msg("DNS server started")

		// Configure system resolver
		if err := configureSystemResolver(resp.Domain, cfg.DNS.Listen); err != nil {
			log.Warn().Err(err).Msg("failed to configure system resolver")
		} else {
			dnsConfigured = true
		}
	}

	// Start packet forwarder if TUN is available
	if tunDev != nil {
		go func() {
			if err := forwarder.Run(ctx); err != nil && ctx.Err() == nil {
				log.Error().Err(err).Msg("forwarder error")
			}
		}()
	}

	// Create network monitor for detecting network changes
	triggerDiscovery := make(chan struct{}, 1)
	netChangeState := newNetworkChangeState()

	// Set up callback to trigger discovery when a tunnel is removed
	tunnelMgr.SetOnRemove(func() {
		select {
		case triggerDiscovery <- struct{}{}:
			log.Debug().Msg("triggered discovery due to tunnel removal")
		default:
			// Discovery already pending
		}
	})

	netMonitor, err := netmon.New(netmon.DefaultConfig())
	if err != nil {
		log.Warn().Err(err).Msg("failed to create network monitor, changes won't be detected")
	} else {
		networkChanges, err := netMonitor.Start(ctx)
		if err != nil {
			log.Warn().Err(err).Msg("failed to start network monitor")
			netMonitor.Close()
		} else {
			defer netMonitor.Close()
			go networkChangeLoop(ctx, networkChanges, client, cfg, pubKeyEncoded, resp.MeshCIDR, tunnelMgr, triggerDiscovery, netChangeState)
		}
	}

	// Start peer discovery and tunnel establishment loop
	go peerDiscoveryLoop(ctx, client, cfg.Name, sshClient, negotiator, tunnelMgr, router, forwarder, sshServer, triggerDiscovery, netChangeState)

	// Start heartbeat loop
	go heartbeatLoop(ctx, client, cfg.Name, pubKeyEncoded, cfg.SSHPort, resp.MeshCIDR, resolver, forwarder, tunnelMgr)

	// Wait for context cancellation (shutdown signal)
	<-ctx.Done()

	// Clean up system resolver
	if dnsConfigured {
		if err := removeSystemResolver(resp.Domain); err != nil {
			log.Warn().Err(err).Msg("failed to remove system resolver")
		}
	}

	// Close all tunnels
	tunnelMgr.CloseAll()

	// Don't deregister on shutdown - keep the peer record so we get the same
	// mesh IP when we reconnect. Use 'tunnelmesh leave' for intentional removal.
	log.Info().Msg("disconnected from mesh (peer record retained for sticky IP)")

	if resolver != nil {
		_ = resolver.Shutdown()
	}

	return nil
}

// networkChangeState tracks when the last network change occurred.
// Used to temporarily bypass alpha ordering for faster reconnection after resume.
type networkChangeState struct {
	lastChange atomic.Value // stores time.Time
}

func newNetworkChangeState() *networkChangeState {
	state := &networkChangeState{}
	state.lastChange.Store(time.Time{})
	return state
}

func (s *networkChangeState) recordChange() {
	s.lastChange.Store(time.Now())
}

// inBypassWindow returns true if within the bypass window after a network change.
func (s *networkChangeState) inBypassWindow(window time.Duration) bool {
	lastChange := s.lastChange.Load().(time.Time)
	if lastChange.IsZero() {
		return false
	}
	return time.Since(lastChange) < window
}

// TunnelAdapter wraps tunnel.TunnelManager to implement routing.TunnelProvider
type TunnelAdapter struct {
	tunnels  map[string]io.ReadWriteCloser
	mu       sync.RWMutex
	onRemove func() // Called when a tunnel is removed (for triggering reconnection)
}

func NewTunnelAdapter() *TunnelAdapter {
	return &TunnelAdapter{
		tunnels: make(map[string]io.ReadWriteCloser),
	}
}

// SetOnRemove sets a callback that is called when a tunnel is removed.
func (t *TunnelAdapter) SetOnRemove(callback func()) {
	t.onRemove = callback
}

func (t *TunnelAdapter) Get(name string) (io.ReadWriteCloser, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	tunnel, ok := t.tunnels[name]
	return tunnel, ok
}

func (t *TunnelAdapter) Add(name string, tunnel io.ReadWriteCloser) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Close existing tunnel if present
	if existing, ok := t.tunnels[name]; ok {
		existing.Close()
	}
	t.tunnels[name] = tunnel
	log.Debug().Str("peer", name).Msg("tunnel added")
}

func (t *TunnelAdapter) Remove(name string) {
	t.mu.Lock()
	callback := t.onRemove
	if tunnel, ok := t.tunnels[name]; ok {
		tunnel.Close()
		delete(t.tunnels, name)
		log.Debug().Str("peer", name).Msg("tunnel removed")
	}
	t.mu.Unlock()

	// Trigger reconnection callback outside the lock
	if callback != nil {
		callback()
	}
}

// RemoveIfMatch removes a tunnel only if the current tunnel for this peer matches
// the provided tunnel. This prevents removing a replacement tunnel when the old
// tunnel's handler goroutine exits.
func (t *TunnelAdapter) RemoveIfMatch(name string, tun io.ReadWriteCloser) {
	t.mu.Lock()
	callback := t.onRemove
	removed := false
	if existing, ok := t.tunnels[name]; ok && existing == tun {
		// Don't close - it's already closed (that's why we're here)
		delete(t.tunnels, name)
		log.Debug().Str("peer", name).Msg("tunnel removed")
		removed = true
	}
	t.mu.Unlock()

	// Only trigger reconnection if we actually removed the tunnel
	if removed && callback != nil {
		callback()
	}
}

func (t *TunnelAdapter) CloseAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for name, tunnel := range t.tunnels {
		tunnel.Close()
		delete(t.tunnels, name)
	}
	log.Debug().Msg("all tunnels closed")
}

func (t *TunnelAdapter) List() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	names := make([]string, 0, len(t.tunnels))
	for name := range t.tunnels {
		names = append(names, name)
	}
	return names
}

// handleSSHConnections handles incoming SSH connections
func handleSSHConnections(ctx context.Context, listener net.Listener, sshServer *tunnel.SSHServer,
	tunnelMgr *TunnelAdapter, forwarder *routing.Forwarder, router *routing.Router,
	coordClient *coord.Client) {

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Error().Err(err).Msg("accept error")
			continue
		}

		go handleSSHConnection(ctx, conn, sshServer, tunnelMgr, forwarder, router, coordClient)
	}
}

func handleSSHConnection(ctx context.Context, conn net.Conn, sshServer *tunnel.SSHServer,
	tunnelMgr *TunnelAdapter, forwarder *routing.Forwarder, router *routing.Router,
	coordClient *coord.Client) {

	sshConn, err := sshServer.Accept(conn)
	if err != nil {
		// If handshake failed, try refreshing authorized keys and log for retry
		log.Warn().Err(err).Str("remote", conn.RemoteAddr().String()).Msg("SSH handshake failed, refreshing peer keys")
		refreshAuthorizedKeys(coordClient, sshServer, router)
		conn.Close()
		return
	}

	log.Info().
		Str("remote", conn.RemoteAddr().String()).
		Str("user", sshConn.Conn.User()).
		Msg("incoming SSH connection")

	// Handle channels
	for newChannel := range sshConn.Channels {
		if newChannel.ChannelType() != tunnel.ChannelType {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}

		channel, _, err := newChannel.Accept()
		if err != nil {
			log.Warn().Err(err).Msg("failed to accept channel")
			continue
		}

		// Get peer name from extra data (sent by client when opening channel)
		peerName := string(newChannel.ExtraData())
		if peerName == "" {
			peerName = sshConn.Conn.User() // Fallback to user name
		}

		tun := tunnel.NewTunnel(channel, peerName)

		// Add to tunnel manager
		tunnelMgr.Add(peerName, tun)

		log.Info().Str("peer", peerName).Msg("tunnel established from incoming connection")

		// Handle incoming packets from this tunnel
		go func(name string, t io.ReadWriteCloser) {
			forwarder.HandleTunnel(ctx, name, t)
			tunnelMgr.RemoveIfMatch(name, t)
		}(peerName, tun)
	}
}

// peerDiscoveryLoop periodically discovers peers and establishes tunnels
func peerDiscoveryLoop(ctx context.Context, client *coord.Client, myName string,
	sshClient *tunnel.SSHClient, negotiator *negotiate.Negotiator,
	tunnelMgr *TunnelAdapter, router *routing.Router, forwarder *routing.Forwarder,
	sshServer *tunnel.SSHServer, triggerDiscovery <-chan struct{},
	netChangeState *networkChangeState) {

	// Add random jitter (0-3 seconds) before initial discovery to stagger startup
	jitter := time.Duration(rand.Intn(3000)) * time.Millisecond
	log.Debug().Dur("jitter", jitter).Msg("waiting before initial peer discovery")
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}

	// Initial peer discovery
	discoverAndConnectPeers(ctx, client, myName, sshClient, negotiator, tunnelMgr, router, forwarder, sshServer, netChangeState)

	// Fast retry phase: discover every 5 seconds for the first 30 seconds
	// This helps establish connections quickly after startup
	fastTicker := time.NewTicker(5 * time.Second)
	fastPhaseEnd := time.After(30 * time.Second)

fastLoop:
	for {
		select {
		case <-ctx.Done():
			fastTicker.Stop()
			return
		case <-fastPhaseEnd:
			fastTicker.Stop()
			break fastLoop
		case <-fastTicker.C:
			discoverAndConnectPeers(ctx, client, myName, sshClient, negotiator, tunnelMgr, router, forwarder, sshServer, netChangeState)
		case <-triggerDiscovery:
			log.Debug().Msg("peer discovery triggered by network change")
			discoverAndConnectPeers(ctx, client, myName, sshClient, negotiator, tunnelMgr, router, forwarder, sshServer, netChangeState)
		}
	}

	// Normal phase: discover every 60 seconds
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			discoverAndConnectPeers(ctx, client, myName, sshClient, negotiator, tunnelMgr, router, forwarder, sshServer, netChangeState)
		case <-triggerDiscovery:
			log.Debug().Msg("peer discovery triggered by network change")
			discoverAndConnectPeers(ctx, client, myName, sshClient, negotiator, tunnelMgr, router, forwarder, sshServer, netChangeState)
		}
	}
}

func discoverAndConnectPeers(ctx context.Context, client *coord.Client, myName string,
	sshClient *tunnel.SSHClient, negotiator *negotiate.Negotiator,
	tunnelMgr *TunnelAdapter, router *routing.Router, forwarder *routing.Forwarder,
	sshServer *tunnel.SSHServer, netChangeState *networkChangeState) {

	peers, err := client.ListPeers()
	if err != nil {
		log.Warn().Err(err).Msg("failed to list peers")
		return
	}

	existingTunnels := tunnelMgr.List()
	existingSet := make(map[string]bool)
	for _, name := range existingTunnels {
		existingSet[name] = true
	}

	// Check if we're in a network change bypass window (10 seconds)
	// During this window, both peers attempt connection to speed up reconnection
	const bypassWindow = 10 * time.Second
	bypassAlphaOrdering := netChangeState.inBypassWindow(bypassWindow)

	for _, peer := range peers {
		if peer.Name == myName {
			continue // Skip self
		}

		// Add peer's public key to authorized keys for incoming connections
		if peer.PublicKey != "" {
			pubKey, err := config.DecodePublicKey(peer.PublicKey)
			if err != nil {
				log.Warn().Err(err).Str("peer", peer.Name).Msg("failed to decode peer public key")
			} else {
				sshServer.AddAuthorizedKey(pubKey)
			}
		}

		// Update routing table with peer's mesh IP
		router.AddRoute(peer.MeshIP, peer.Name)

		// Skip if tunnel already exists
		if existingSet[peer.Name] {
			continue
		}

		// Try to establish tunnel
		// Alpha ordering is checked inside establishTunnel and only applies to direct SSH connections.
		// For relay connections, both peers must connect to the relay server, so alpha ordering is bypassed.
		go establishTunnel(ctx, peer, myName, sshClient, negotiator, tunnelMgr, forwarder, client, bypassAlphaOrdering)
	}
}

// refreshAuthorizedKeys fetches peer keys from coordination server and adds them to SSH server.
// This is called when an SSH handshake fails to ensure we have the latest keys.
func refreshAuthorizedKeys(client *coord.Client, sshServer *tunnel.SSHServer, router *routing.Router) {
	peers, err := client.ListPeers()
	if err != nil {
		log.Warn().Err(err).Msg("failed to refresh peer keys")
		return
	}

	for _, peer := range peers {
		if peer.PublicKey != "" {
			pubKey, err := config.DecodePublicKey(peer.PublicKey)
			if err != nil {
				log.Warn().Err(err).Str("peer", peer.Name).Msg("failed to decode peer public key")
			} else {
				sshServer.AddAuthorizedKey(pubKey)
			}
		}
		// Also update routing table
		router.AddRoute(peer.MeshIP, peer.Name)
	}
	log.Debug().Int("peers", len(peers)).Msg("refreshed authorized keys")
}

// networkChangeLoop handles network interface change events
func networkChangeLoop(ctx context.Context, events <-chan netmon.Event,
	client *coord.Client, cfg *config.PeerConfig, pubKeyEncoded string,
	meshCIDR string, tunnelMgr *TunnelAdapter, triggerDiscovery chan<- struct{},
	netChangeState *networkChangeState) {

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			log.Info().
				Str("type", event.Type.String()).
				Str("interface", event.Interface).
				Msg("network change detected")

			// Get new IP addresses, excluding mesh network IPs
			publicIPs, privateIPs, behindNAT := proto.GetLocalIPsExcluding(meshCIDR)
			log.Debug().
				Strs("public", publicIPs).
				Strs("private", privateIPs).
				Bool("behind_nat", behindNAT).
				Msg("updated local IPs")

			// Re-register with coordination server
			resp, err := client.Register(cfg.Name, pubKeyEncoded, publicIPs, privateIPs, cfg.SSHPort, behindNAT)
			if err != nil {
				log.Error().Err(err).Msg("failed to re-register after network change")
				continue
			}

			log.Info().
				Str("mesh_ip", resp.MeshIP).
				Msg("re-registered with coordination server")

			// Close all existing tunnels (they may be using stale IPs)
			tunnelMgr.CloseAll()
			log.Debug().Msg("closed stale tunnels after network change")

			// Record network change time for bypass window
			netChangeState.recordChange()

			// Trigger immediate peer discovery
			select {
			case triggerDiscovery <- struct{}{}:
			default:
				// Discovery already pending
			}
		}
	}
}

func establishTunnel(ctx context.Context, peer proto.Peer, myName string, sshClient *tunnel.SSHClient,
	negotiator *negotiate.Negotiator, tunnelMgr *TunnelAdapter, forwarder *routing.Forwarder,
	client *coord.Client, bypassAlphaOrdering bool) {

	peerInfo := &negotiate.PeerInfo{
		ID:          peer.Name,
		PublicIP:    "",
		PrivateIPs:  peer.PrivateIPs,
		SSHPort:     peer.SSHPort,
		Connectable: peer.Connectable,
	}
	if len(peer.PublicIPs) > 0 {
		peerInfo.PublicIP = peer.PublicIPs[0]
	}

	// Log detailed peer info for debugging
	log.Debug().
		Str("peer", peer.Name).
		Str("public_ip", peerInfo.PublicIP).
		Strs("private_ips", peerInfo.PrivateIPs).
		Int("ssh_port", peerInfo.SSHPort).
		Bool("connectable", peerInfo.Connectable).
		Msg("attempting to establish tunnel")

	// Negotiate connection
	result, err := negotiator.Negotiate(ctx, peerInfo)
	if err != nil {
		log.Warn().Err(err).Str("peer", peer.Name).Msg("negotiation failed")
		return
	}

	switch result.Strategy {
	case negotiate.StrategyReverse:
		log.Info().Str("peer", peer.Name).Msg("peer requires reverse connection, waiting for incoming")
		return

	case negotiate.StrategyRelay:
		// Use relay through coordination server
		log.Info().Str("peer", peer.Name).Msg("using relay connection through coordination server")

		jwtToken := client.JWTToken()
		if jwtToken == "" {
			log.Warn().Str("peer", peer.Name).Msg("no JWT token available for relay")
			return
		}

		relayTunnel, err := tunnel.NewRelayTunnel(ctx, client.BaseURL(), peer.Name, jwtToken)
		if err != nil {
			log.Warn().Err(err).Str("peer", peer.Name).Msg("relay connection failed")
			return
		}

		tunnelMgr.Add(peer.Name, relayTunnel)
		log.Info().Str("peer", peer.Name).Msg("relay tunnel established")

		// Handle incoming packets from this tunnel
		go func(name string, t io.ReadWriteCloser) {
			forwarder.HandleTunnel(ctx, name, t)
			tunnelMgr.RemoveIfMatch(name, t)
		}(peer.Name, relayTunnel)
		return

	case negotiate.StrategyDirect:
		// For direct SSH connections, apply alpha ordering to prevent duplicate tunnels
		// (both peers trying to SSH to each other simultaneously)
		// Exception: bypass during network change recovery window
		if myName > peer.Name && !bypassAlphaOrdering {
			log.Debug().Str("peer", peer.Name).Msg("waiting for peer to initiate direct connection")
			return
		}

		if bypassAlphaOrdering && myName > peer.Name {
			log.Debug().Str("peer", peer.Name).Msg("bypassing alpha ordering due to recent network change")
		}

		// Direct SSH connection
		log.Info().
			Str("peer", peer.Name).
			Str("addr", result.Address).
			Msg("connecting to peer via direct SSH")

		sshConn, err := sshClient.Connect(result.Address)
		if err != nil {
			log.Warn().Err(err).Str("peer", peer.Name).Msg("SSH connection failed")
			return
		}

		// Open data channel with our name as extra data so the peer knows who we are
		channel, _, err := sshConn.OpenChannel(tunnel.ChannelType, []byte(myName))
		if err != nil {
			log.Warn().Err(err).Str("peer", peer.Name).Msg("failed to open channel")
			sshConn.Close()
			return
		}

		// Create tunnel and add to manager
		// Use NewTunnelWithClient to keep SSH connection alive (prevents GC from closing it)
		tun := tunnel.NewTunnelWithClient(channel, peer.Name, sshConn)
		tunnelMgr.Add(peer.Name, tun)

		log.Info().Str("peer", peer.Name).Msg("tunnel established")

		// Handle incoming packets from this tunnel
		go func(name string, t io.ReadWriteCloser) {
			forwarder.HandleTunnel(ctx, name, t)
			tunnelMgr.RemoveIfMatch(name, t)
		}(peer.Name, tun)
	}
}

func runStatus(cmd *cobra.Command, args []string) error {
	setupLogging()

	cfg, err := loadConfig()
	if err != nil {
		fmt.Println("Status: Not connected")
		fmt.Println("  Config: not found or invalid")
		fmt.Printf("  Error:  %v\n", err)
		return nil
	}

	// Display configuration
	fmt.Println("TunnelMesh Status")
	fmt.Println("=================")
	fmt.Println()

	// Node info
	fmt.Println("Node:")
	if cfg.Name != "" {
		fmt.Printf("  Name:        %s\n", cfg.Name)
	} else {
		fmt.Println("  Name:        (not configured)")
	}
	fmt.Printf("  SSH Port:    %d\n", cfg.SSHPort)
	fmt.Printf("  Private Key: %s\n", cfg.PrivateKey)

	// Check if keys exist
	if _, err := os.Stat(cfg.PrivateKey); err == nil {
		signer, err := config.LoadPrivateKey(cfg.PrivateKey)
		if err == nil {
			fingerprint := config.GetPublicKeyFingerprint(signer.PublicKey())
			fmt.Printf("  Key FP:      %s\n", fingerprint)
		}
	} else {
		fmt.Println("  Key FP:      (keys not initialized - run 'tunnelmesh init')")
	}

	fmt.Println()

	// Server connection
	fmt.Println("Server:")
	if cfg.Server == "" {
		fmt.Println("  URL:         (not configured)")
		fmt.Println("  Status:      disconnected")
		return nil
	}
	fmt.Printf("  URL:         %s\n", cfg.Server)

	// Try to connect and get peer list
	client := coord.NewClient(cfg.Server, cfg.AuthToken)
	peers, err := client.ListPeers()
	if err != nil {
		fmt.Println("  Status:      unreachable")
		fmt.Printf("  Error:       %v\n", err)
		return nil
	}
	fmt.Println("  Status:      connected")

	fmt.Println()

	// Find ourselves in the peer list
	fmt.Println("Mesh:")
	var myPeer *proto.Peer
	onlineCount := 0
	for i, p := range peers {
		if p.Name == cfg.Name {
			myPeer = &peers[i]
		}
		// Consider peers seen in last 2 minutes as online
		if time.Since(p.LastSeen) < 2*time.Minute {
			onlineCount++
		}
	}

	if myPeer != nil {
		fmt.Printf("  Mesh IP:     %s\n", myPeer.MeshIP)
		fmt.Printf("  Last Seen:   %s\n", myPeer.LastSeen.Format("2006-01-02 15:04:05"))
		if myPeer.Connectable {
			fmt.Println("  Connectable: yes")
		} else {
			fmt.Println("  Connectable: no (behind NAT)")
		}
	} else {
		fmt.Println("  Mesh IP:     (not registered)")
		fmt.Println("  Note:        run 'tunnelmesh join' to join the mesh")
	}

	fmt.Printf("  Total Peers: %d\n", len(peers))
	fmt.Printf("  Online:      %d\n", onlineCount)

	fmt.Println()

	// TUN device info
	fmt.Println("TUN Device:")
	fmt.Printf("  Name:        %s\n", cfg.TUN.Name)
	fmt.Printf("  MTU:         %d\n", cfg.TUN.MTU)

	fmt.Println()

	// DNS info
	fmt.Println("DNS:")
	if cfg.DNS.Enabled {
		fmt.Println("  Enabled:     yes")
		fmt.Printf("  Listen:      %s\n", cfg.DNS.Listen)
		fmt.Printf("  Cache TTL:   %ds\n", cfg.DNS.CacheTTL)
	} else {
		fmt.Println("  Enabled:     no")
	}

	return nil
}

func runPeers(cmd *cobra.Command, args []string) error {
	setupLogging()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	client := coord.NewClient(cfg.Server, cfg.AuthToken)
	peers, err := client.ListPeers()
	if err != nil {
		return fmt.Errorf("list peers: %w", err)
	}

	if len(peers) == 0 {
		fmt.Println("No peers in mesh")
		return nil
	}

	fmt.Printf("%-20s %-15s %-20s %s\n", "NAME", "MESH IP", "PUBLIC IP", "LAST SEEN")
	fmt.Println("-------------------- --------------- -------------------- --------------------")

	for _, p := range peers {
		publicIP := "-"
		if len(p.PublicIPs) > 0 {
			publicIP = p.PublicIPs[0]
		}
		lastSeen := p.LastSeen.Format("2006-01-02 15:04:05")
		fmt.Printf("%-20s %-15s %-20s %s\n", p.Name, p.MeshIP, publicIP, lastSeen)
	}

	return nil
}

func runResolve(cmd *cobra.Command, args []string) error {
	setupLogging()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	hostname := args[0]

	client := coord.NewClient(cfg.Server, cfg.AuthToken)
	records, err := client.GetDNSRecords()
	if err != nil {
		return fmt.Errorf("get DNS records: %w", err)
	}

	for _, r := range records {
		if r.Hostname == hostname {
			fmt.Printf("%s -> %s\n", hostname, r.MeshIP)
			return nil
		}
	}

	return fmt.Errorf("hostname not found: %s", hostname)
}

func runLeave(cmd *cobra.Command, args []string) error {
	setupLogging()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	client := coord.NewClient(cfg.Server, cfg.AuthToken)
	if err := client.Deregister(cfg.Name); err != nil {
		return fmt.Errorf("deregister: %w", err)
	}

	log.Info().Msg("left mesh network")
	return nil
}

func loadConfig() (*config.PeerConfig, error) {
	if cfgFile != "" {
		return config.LoadPeerConfig(cfgFile)
	}

	// Try default locations
	homeDir, _ := os.UserHomeDir()
	defaults := []string{
		filepath.Join(homeDir, ".tunnelmesh", "config.yaml"),
		"tunnelmesh.yaml",
		"peer.yaml",
	}

	for _, path := range defaults {
		if _, err := os.Stat(path); err == nil {
			return config.LoadPeerConfig(path)
		}
	}

	// Return empty config with defaults
	return &config.PeerConfig{
		SSHPort:    2222,
		PrivateKey: filepath.Join(homeDir, ".tunnelmesh", "id_ed25519"),
		TUN: config.TUNConfig{
			Name: "tun-mesh0",
			MTU:  1400,
		},
		DNS: config.DNSConfig{
			Enabled:  true,
			Listen:   "127.0.0.53:5353",
			CacheTTL: 300,
		},
	}, nil
}

func heartbeatLoop(ctx context.Context, client *coord.Client, name, pubKeyEncoded string,
	sshPort int, meshCIDR string, resolver *meshdns.Resolver, forwarder *routing.Forwarder, tunnelMgr *TunnelAdapter) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Track last known IPs to detect changes
	var lastPublicIPs, lastPrivateIPs []string
	var lastBehindNAT bool

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Check if IPs have changed (network switch, ISP change, etc.)
			publicIPs, privateIPs, behindNAT := proto.GetLocalIPsExcluding(meshCIDR)
			ipsChanged := !slicesEqual(publicIPs, lastPublicIPs) || !slicesEqual(privateIPs, lastPrivateIPs) || behindNAT != lastBehindNAT

			if ipsChanged && lastPublicIPs != nil {
				// IPs changed - re-register to update the coordination server
				log.Info().
					Strs("old_public", lastPublicIPs).
					Strs("new_public", publicIPs).
					Strs("old_private", lastPrivateIPs).
					Strs("new_private", privateIPs).
					Msg("IP addresses changed, re-registering...")

				// Close stale connections from the old network
				client.CloseIdleConnections()

				if _, regErr := client.Register(name, pubKeyEncoded, publicIPs, privateIPs, sshPort, behindNAT); regErr != nil {
					log.Error().Err(regErr).Msg("failed to re-register after IP change")
				} else {
					log.Info().Msg("re-registered with new IP addresses")
					lastPublicIPs = publicIPs
					lastPrivateIPs = privateIPs
					lastBehindNAT = behindNAT
				}
			} else if lastPublicIPs == nil {
				// First heartbeat - just record the IPs
				lastPublicIPs = publicIPs
				lastPrivateIPs = privateIPs
				lastBehindNAT = behindNAT
			}

			// Collect stats from forwarder
			var stats *proto.PeerStats
			if forwarder != nil {
				fwdStats := forwarder.Stats()
				stats = &proto.PeerStats{
					PacketsSent:     fwdStats.PacketsSent,
					PacketsReceived: fwdStats.PacketsReceived,
					BytesSent:       fwdStats.BytesSent,
					BytesReceived:   fwdStats.BytesReceived,
					DroppedNoRoute:  fwdStats.DroppedNoRoute,
					DroppedNoTunnel: fwdStats.DroppedNoTunnel,
					Errors:          fwdStats.Errors,
					ActiveTunnels:   len(tunnelMgr.List()),
				}
			}

			if err := client.HeartbeatWithStats(name, pubKeyEncoded, stats); err != nil {
				if errors.Is(err, coord.ErrPeerNotFound) {
					// Server restarted or peer was removed - re-register
					log.Info().Msg("peer not found on server, re-registering...")
					publicIPs, privateIPs, behindNAT := proto.GetLocalIPsExcluding(meshCIDR)
					if _, regErr := client.Register(name, pubKeyEncoded, publicIPs, privateIPs, sshPort, behindNAT); regErr != nil {
						log.Error().Err(regErr).Msg("failed to re-register")
					} else {
						log.Info().Msg("re-registered with coordination server")
						lastPublicIPs = publicIPs
						lastPrivateIPs = privateIPs
						lastBehindNAT = behindNAT
					}
				} else {
					log.Warn().Err(err).Msg("heartbeat failed")
				}
				continue
			}

			// Sync DNS records
			if resolver != nil {
				if err := syncDNS(client, resolver); err != nil {
					log.Warn().Err(err).Msg("DNS sync failed")
				}
			}
		}
	}
}

// slicesEqual compares two string slices for equality.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func syncDNS(client *coord.Client, resolver *meshdns.Resolver) error {
	records, err := client.GetDNSRecords()
	if err != nil {
		return err
	}

	recordMap := make(map[string]string, len(records))
	for _, r := range records {
		recordMap[r.Hostname] = r.MeshIP
	}

	resolver.UpdateRecords(recordMap)
	return nil
}

func setupLogging() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix

	level, err := zerolog.ParseLevel(logLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)

	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
}

// setupServiceLogging configures logging for service mode.
// This writes directly to a file because launchd/kardianos-service
// may not properly redirect stderr.
func setupServiceLogging() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	zerolog.SetGlobalLevel(zerolog.DebugLevel)

	// Try to open log file for direct writing
	logPath := "/var/log/tunnelmesh-service.log"
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		// Fall back to stderr if we can't open the log file
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
		return
	}

	// Write to both file and stderr
	multi := io.MultiWriter(logFile, os.Stderr)
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: multi, TimeFormat: time.RFC3339})
}

// configureSystemResolver sets up the system to resolve .mesh domains via our DNS server.
func configureSystemResolver(domain, dnsAddr string) error {
	// Extract port from address
	parts := strings.Split(dnsAddr, ":")
	if len(parts) != 2 {
		return fmt.Errorf("invalid DNS address: %s", dnsAddr)
	}
	port := parts[1]

	// Remove leading dot from domain
	domain = strings.TrimPrefix(domain, ".")

	switch runtime.GOOS {
	case "darwin":
		return configureDarwinResolver(domain, port)
	case "linux":
		return configureLinuxResolver(domain, dnsAddr)
	case "windows":
		return configureWindowsResolver(domain, dnsAddr)
	default:
		log.Warn().Str("os", runtime.GOOS).Msg("automatic DNS configuration not supported on this OS")
		return nil
	}
}

// removeSystemResolver removes the system resolver configuration.
func removeSystemResolver(domain string) error {
	domain = strings.TrimPrefix(domain, ".")

	switch runtime.GOOS {
	case "darwin":
		return removeDarwinResolver(domain)
	case "linux":
		return removeLinuxResolver(domain)
	case "windows":
		return removeWindowsResolver(domain)
	default:
		return nil
	}
}

func configureDarwinResolver(domain, port string) error {
	resolverDir := "/etc/resolver"
	resolverFile := filepath.Join(resolverDir, domain)

	// Check if we can write directly (running as root)
	content := fmt.Sprintf("nameserver 127.0.0.1\nport %s\n", port)

	// Try to create directory and file
	if err := os.MkdirAll(resolverDir, 0755); err != nil {
		// Need sudo - try with sudo
		log.Info().Msg("configuring system DNS resolver (requires sudo)...")

		// Create resolver directory
		cmd := exec.Command("sudo", "mkdir", "-p", resolverDir)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to create resolver directory: %w", err)
		}

		// Write resolver file using tee
		cmd = exec.Command("sudo", "tee", resolverFile)
		cmd.Stdin = strings.NewReader(content)
		cmd.Stdout = nil // Suppress tee output
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to write resolver file: %w", err)
		}

		log.Info().Str("file", resolverFile).Msg("system resolver configured")
		return nil
	}

	// We have permissions, write directly
	if err := os.WriteFile(resolverFile, []byte(content), 0644); err != nil {
		return fmt.Errorf("write resolver file: %w", err)
	}

	log.Info().Str("file", resolverFile).Msg("system resolver configured")
	return nil
}

func removeDarwinResolver(domain string) error {
	resolverFile := filepath.Join("/etc/resolver", domain)

	// Check if file exists
	if _, err := os.Stat(resolverFile); os.IsNotExist(err) {
		return nil
	}

	// Try to remove directly
	if err := os.Remove(resolverFile); err != nil {
		// Need sudo
		cmd := exec.Command("sudo", "rm", "-f", resolverFile)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to remove resolver file: %w", err)
		}
	}

	log.Info().Str("file", resolverFile).Msg("system resolver removed")
	return nil
}

func configureLinuxResolver(domain, dnsAddr string) error {
	parts := strings.Split(dnsAddr, ":")
	if len(parts) != 2 {
		return fmt.Errorf("invalid DNS address: %s", dnsAddr)
	}
	ip := parts[0]
	port := parts[1]

	// Try systemd-resolved first (most common on modern Linux)
	if _, err := exec.LookPath("resolvectl"); err == nil {
		return configureSystemdResolved(domain, ip, port)
	}

	// Fallback: create a config file for systemd-resolved
	confDir := "/etc/systemd/resolved.conf.d"
	if _, err := os.Stat("/etc/systemd/resolved.conf"); err == nil {
		return configureSystemdResolvedFile(domain, ip, port, confDir)
	}

	log.Warn().
		Str("domain", domain).
		Str("dns", dnsAddr).
		Msg("could not auto-configure DNS; manually add DNS server or use: dig @127.0.0.1 -p PORT hostname.mesh")
	return nil
}

func configureSystemdResolved(domain, ip, port string) error {
	log.Info().Msg("configuring systemd-resolved (requires sudo)...")

	// Use resolvectl to set DNS for the mesh domain
	// Note: This sets a global DNS that only handles .mesh
	cmd := exec.Command("sudo", "resolvectl", "dns", "lo", ip+":"+port)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("resolvectl dns failed: %w", err)
	}

	cmd = exec.Command("sudo", "resolvectl", "domain", "lo", "~"+domain)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("resolvectl domain failed: %w", err)
	}

	log.Info().Str("domain", domain).Msg("systemd-resolved configured")
	return nil
}

func configureSystemdResolvedFile(domain, ip, port, confDir string) error {
	log.Info().Msg("configuring systemd-resolved via config file (requires sudo)...")

	content := fmt.Sprintf(`# TunnelMesh DNS configuration
[Resolve]
DNS=%s:%s
Domains=~%s
`, ip, port, domain)

	confFile := filepath.Join(confDir, "tunnelmesh.conf")

	// Create directory
	cmd := exec.Command("sudo", "mkdir", "-p", confDir)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mkdir failed: %w", err)
	}

	// Write config
	cmd = exec.Command("sudo", "tee", confFile)
	cmd.Stdin = strings.NewReader(content)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write config failed: %w", err)
	}

	// Restart systemd-resolved
	cmd = exec.Command("sudo", "systemctl", "restart", "systemd-resolved")
	cmd.Stderr = os.Stderr
	_ = cmd.Run() // Ignore error, might not need restart

	log.Info().Str("file", confFile).Msg("systemd-resolved configured")
	return nil
}

func removeLinuxResolver(domain string) error {
	// Try resolvectl first
	if _, err := exec.LookPath("resolvectl"); err == nil {
		cmd := exec.Command("sudo", "resolvectl", "revert", "lo")
		cmd.Stdin = os.Stdin
		cmd.Stderr = os.Stderr
		_ = cmd.Run() // Ignore errors
	}

	// Remove config file if it exists
	confFile := "/etc/systemd/resolved.conf.d/tunnelmesh.conf"
	if _, err := os.Stat(confFile); err == nil {
		cmd := exec.Command("sudo", "rm", "-f", confFile)
		cmd.Stdin = os.Stdin
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("remove config failed: %w", err)
		}

		// Restart systemd-resolved
		cmd = exec.Command("sudo", "systemctl", "restart", "systemd-resolved")
		cmd.Stderr = os.Stderr
		_ = cmd.Run()

		log.Info().Msg("systemd-resolved configuration removed")
	}

	return nil
}

func configureWindowsResolver(domain, dnsAddr string) error {
	parts := strings.Split(dnsAddr, ":")
	if len(parts) != 2 {
		return fmt.Errorf("invalid DNS address: %s", dnsAddr)
	}
	ip := parts[0]
	port := parts[1]

	log.Info().Msg("configuring Windows DNS (requires Administrator)...")

	// Windows doesn't natively support per-domain DNS or custom ports easily
	// We'll use NRPT (Name Resolution Policy Table) via PowerShell
	// This requires running as Administrator

	// PowerShell command to add NRPT rule
	psCmd := fmt.Sprintf(`Add-DnsClientNrptRule -Namespace ".%s" -NameServers "%s"`, domain, ip)

	if port != "53" {
		// Windows NRPT doesn't support custom ports directly
		// We'd need to run a local DNS proxy on port 53
		log.Warn().
			Str("port", port).
			Msg("Windows requires DNS on port 53; consider changing dns.listen to 127.0.0.1:53")
		return fmt.Errorf("Windows requires DNS on port 53, got port %s", port)
	}

	cmd := exec.Command("powershell", "-Command", psCmd)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("PowerShell NRPT rule failed: %w", err)
	}

	log.Info().Str("domain", domain).Msg("Windows NRPT rule configured")
	return nil
}

func removeWindowsResolver(domain string) error {
	// Remove NRPT rule
	psCmd := fmt.Sprintf(`Get-DnsClientNrptRule | Where-Object {$_.Namespace -eq ".%s"} | Remove-DnsClientNrptRule -Force`, domain)

	cmd := exec.Command("powershell", "-Command", psCmd)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Warn().Err(err).Msg("failed to remove Windows NRPT rule")
		return err
	}

	log.Info().Msg("Windows NRPT rule removed")
	return nil
}
