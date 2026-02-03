// tunnelmesh is the P2P SSH tunnel mesh network tool.
package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/coord"
	meshdns "github.com/tunnelmesh/tunnelmesh/internal/dns"
	"github.com/tunnelmesh/tunnelmesh/internal/netmon"
	"github.com/tunnelmesh/tunnelmesh/internal/peer"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
	"github.com/tunnelmesh/tunnelmesh/internal/svc"
	"github.com/tunnelmesh/tunnelmesh/internal/transport"
	relaytransport "github.com/tunnelmesh/tunnelmesh/internal/transport/relay"
	sshtransport "github.com/tunnelmesh/tunnelmesh/internal/transport/ssh"
	udptransport "github.com/tunnelmesh/tunnelmesh/internal/transport/udp"
	"github.com/tunnelmesh/tunnelmesh/internal/tun"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
	"golang.org/x/crypto/ssh"
)

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// ballast is a large allocation that reduces GC frequency by increasing the
// target heap size. This is particularly effective for reducing latency spikes
// caused by GC pauses in packet forwarding paths. Package-level to ensure it
// stays alive for the lifetime of the process.
//
//nolint:gochecknoglobals
var ballast []byte

// setupGCTuning configures the Go garbage collector for lower latency.
// It sets GOGC to 200 and allocates a memory ballast.
func setupGCTuning() {
	// Set GOGC to 200 (default is 100) to trigger GC less frequently.
	// This trades memory for lower latency variance.
	debug.SetGCPercent(200)

	// Allocate a 10MB ballast. The GC triggers when live heap reaches
	// GOGC% of the previous heap size. With a ballast, the effective
	// trigger point is higher, reducing GC frequency for small allocations.
	ballast = make([]byte, 10<<20) // 10MB

	// Prevent the ballast from being optimized away
	runtime.KeepAlive(ballast)
}

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
	logStartupBanner()

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
	srv.SetVersion(Version)

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
	logStartupBanner()

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
	srv.SetVersion(Version)

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
	logStartupBanner()

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

	// Tune GC for lower latency in packet forwarding
	setupGCTuning()

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

	// UDP port is SSH port + 1
	udpPort := cfg.SSHPort + 1
	resp, err := client.Register(cfg.Name, pubKeyEncoded, publicIPs, privateIPs, cfg.SSHPort, udpPort, behindNAT, Version)
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

	// Create peer identity and mesh node
	identity := peer.NewPeerIdentity(cfg, pubKeyEncoded, udpPort, Version, resp)
	node := peer.NewMeshNode(identity, client)

	// Set up forwarder with node's tunnel manager and router
	forwarder := routing.NewForwarder(node.Router(), node.TunnelMgr())
	if tunDev != nil {
		forwarder.SetTUN(tunDev)
		forwarder.SetLocalIP(net.ParseIP(resp.MeshIP))
	}
	node.Forwarder = forwarder

	// When forwarder detects a dead tunnel (write fails), disconnect the peer
	// This removes the tunnel and triggers discovery for reconnection
	forwarder.SetOnDeadTunnel(func(peerName string) {
		if pc := node.Connections.Get(peerName); pc != nil {
			log.Debug().Str("peer", peerName).Msg("forwarder detected dead tunnel, disconnecting peer")
			_ = pc.Disconnect("tunnel write failed", nil)
		}
	})

	// Create transport registry with default order: UDP -> SSH -> Relay
	// UDP is first for better performance (lower latency, no head-of-line blocking)
	// Falls back to SSH when UDP hole-punching fails or times out
	transportRegistry := transport.NewRegistry(transport.RegistryConfig{
		DefaultOrder: []transport.TransportType{
			transport.TransportUDP,
			transport.TransportSSH,
			transport.TransportRelay,
		},
	})
	node.TransportRegistry = transportRegistry

	// Create and register SSH transport
	sshTransport, err := sshtransport.New(sshtransport.Config{
		HostKey:      signer,
		ClientSigner: signer,
		ListenPort:   cfg.SSHPort,
	})
	if err != nil {
		return fmt.Errorf("create SSH transport: %w", err)
	}
	if err := transportRegistry.Register(sshTransport); err != nil {
		return fmt.Errorf("register SSH transport: %w", err)
	}
	node.SSHTransport = sshTransport

	// Start SSH listener via transport layer
	sshListener, err := sshTransport.Listen(ctx, transport.ListenOptions{
		Port: cfg.SSHPort,
	})
	if err != nil {
		return fmt.Errorf("create SSH listener: %w", err)
	}
	go node.HandleIncomingSSH(ctx, sshListener)
	log.Info().Int("port", cfg.SSHPort).Msg("SSH transport listening")

	// Create and register UDP transport (for lower latency when direct connection possible)
	edPrivKey, err := config.LoadED25519PrivateKey(cfg.PrivateKey)
	if err != nil {
		log.Warn().Err(err).Msg("failed to load ED25519 key for UDP transport, UDP disabled")
	} else {
		x25519Priv, x25519Pub, err := config.DeriveX25519KeyPair(edPrivKey)
		if err != nil {
			log.Warn().Err(err).Msg("failed to derive X25519 keys for UDP transport, UDP disabled")
		} else {
			var privKey, pubKey [32]byte
			copy(privKey[:], x25519Priv)
			copy(pubKey[:], x25519Pub)

			// Debug: Also convert ED25519 public key directly to verify matching
			ed25519PubBytes := edPrivKey.Public().(ed25519.PublicKey)
			x25519FromEd, _ := config.ED25519PublicToX25519(ed25519PubBytes)
			log.Debug().
				Hex("x25519_from_priv", x25519Pub).
				Hex("x25519_from_ed_pub", x25519FromEd).
				Bool("match", string(x25519Pub) == string(x25519FromEd)).
				Msg("X25519 key derivation check")

			// Create peer resolver for incoming UDP connections
			peerResolver := func(x25519PubKey [32]byte) string {
				peers, err := client.ListPeers()
				if err != nil {
					log.Debug().Err(err).Msg("failed to list peers for resolver")
					return ""
				}
				for _, p := range peers {
					if p.PublicKey == "" {
						continue
					}
					// Parse the peer's SSH public key
					sshPubKey, err := config.DecodePublicKey(p.PublicKey)
					if err != nil {
						continue
					}
					// Extract the crypto.PublicKey from the SSH public key
					cryptoPubKey, ok := sshPubKey.(ssh.CryptoPublicKey)
					if !ok {
						continue
					}
					edPubKey, ok := cryptoPubKey.CryptoPublicKey().(ed25519.PublicKey)
					if !ok {
						continue
					}
					peerX25519, err := config.ED25519PublicToX25519(edPubKey)
					if err != nil {
						continue
					}
					if len(peerX25519) == 32 {
						var peerKey [32]byte
						copy(peerKey[:], peerX25519)
						if peerKey == x25519PubKey {
							return p.Name
						}
					}
				}
				return ""
			}

			udpTransport, err := udptransport.New(udptransport.Config{
				Port:           cfg.SSHPort + 1, // Use SSH port + 1 for UDP
				LocalPeerName:  cfg.Name,
				StaticPrivate:  privKey,
				StaticPublic:   pubKey,
				CoordServerURL: cfg.Server,
				AuthToken:      cfg.AuthToken,
				PeerResolver:   peerResolver,
			})
			if err != nil {
				log.Warn().Err(err).Msg("failed to create UDP transport, UDP disabled")
			} else {
				// Wire up session invalidation callback for rekey-required handling
				node.SetupUDPSessionInvalidCallback(udpTransport)

				if err := transportRegistry.Register(udpTransport); err != nil {
					log.Warn().Err(err).Msg("failed to register UDP transport")
				} else {
					log.Info().Int("port", cfg.SSHPort+1).Msg("UDP transport registered")
					// Start UDP transport to accept incoming connections
					if err := udpTransport.Start(); err != nil {
						log.Warn().Err(err).Msg("failed to start UDP transport")
					} else {
						log.Info().Int("port", cfg.SSHPort+1).Msg("UDP transport started")
						// Start listening for incoming UDP connections
						udpListener, err := udpTransport.Listen(ctx, transport.ListenOptions{})
						if err != nil {
							log.Warn().Err(err).Msg("failed to create UDP listener")
						} else {
							go node.HandleIncomingUDP(ctx, udpListener)
							log.Info().Int("port", cfg.SSHPort+1).Msg("UDP listener started")
						}
					}
					// Register UDP endpoint with coordination server for hole-punching
					if err := udpTransport.RegisterUDPEndpoint(ctx, cfg.Name); err != nil {
						log.Warn().Err(err).Msg("failed to register UDP endpoint")
					}

					// Start periodic UDP endpoint refresh (endpoints expire after 5 minutes)
					go func() {
						ticker := time.NewTicker(2 * time.Minute)
						defer ticker.Stop()
						for {
							select {
							case <-ctx.Done():
								return
							case <-ticker.C:
								if err := udpTransport.RegisterUDPEndpoint(ctx, cfg.Name); err != nil {
									log.Debug().Err(err).Msg("failed to refresh UDP endpoint registration")
								} else {
									log.Debug().Msg("UDP endpoint registration refreshed")
								}
							}
						}
					}()
				}
			}
		}
	}

	// Create and register relay transport
	relayTransport, err := relaytransport.New(relaytransport.Config{
		ServerURL: cfg.Server,
		JWTToken:  client.JWTToken(), // Will be updated after registration
	})
	if err != nil {
		return fmt.Errorf("create relay transport: %w", err)
	}
	if err := transportRegistry.Register(relayTransport); err != nil {
		return fmt.Errorf("register relay transport: %w", err)
	}
	log.Info().Msg("relay transport registered")

	// Create transport negotiator
	transportNegotiator := transport.NewNegotiator(transportRegistry, transport.NegotiatorConfig{
		ProbeTimeout:      5 * time.Second,
		ConnectionTimeout: 30 * time.Second,
		MaxRetries:        3,
		RetryDelay:        1 * time.Second,
		ParallelProbes:    3,
		EnableFallback:    true,
	})
	node.TransportNegotiator = transportNegotiator

	// Connect persistent relay for DERP-like instant connectivity
	// This provides immediate relay routing while direct connections are established in parallel
	if err := node.ConnectPersistentRelay(ctx); err != nil {
		log.Warn().Err(err).Msg("persistent relay not available, will use sequential fallback")
	} else {
		// Set the relay on the forwarder for fallback routing
		forwarder.SetRelay(node.PersistentRelay)
		log.Info().Msg("persistent relay enabled for instant connectivity")
	}

	// Start DNS resolver if enabled
	var dnsConfigured bool
	if cfg.DNS.Enabled {
		resolver := meshdns.NewResolver(resp.Domain, cfg.DNS.CacheTTL)
		node.Resolver = resolver

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

	// Set up callback to trigger discovery when a tunnel is removed
	node.TunnelMgr().SetOnRemove(func() {
		if node.TriggerDiscovery() {
			log.Debug().Msg("triggered discovery due to tunnel removal")
		}
	})

	// Create network monitor for detecting network changes
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
			go node.RunNetworkMonitor(ctx, networkChanges)
		}
	}

	// Start peer discovery and tunnel establishment loop
	go node.RunPeerDiscovery(ctx)

	// Start heartbeat loop
	go node.RunHeartbeat(ctx)

	// Wait for context cancellation (shutdown signal)
	<-ctx.Done()

	// Clean up system resolver
	if dnsConfigured {
		if err := removeSystemResolver(resp.Domain); err != nil {
			log.Warn().Err(err).Msg("failed to remove system resolver")
		}
	}

	// Close all connections via FSM (properly transitions states and triggers observers)
	node.Connections.CloseAll()

	// Don't deregister on shutdown - keep the peer record so we get the same
	// mesh IP when we reconnect. Use 'tunnelmesh leave' for intentional removal.
	log.Info().Msg("disconnected from mesh (peer record retained for sticky IP)")

	if node.Resolver != nil {
		_ = node.Resolver.Shutdown()
	}

	return nil
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

// logStartupBanner logs the application startup banner with version information.
func logStartupBanner() {
	banner := `
╔════════════════════════════════════════════════════════════════╗
║                                                                ║
║   ████████╗██╗   ██╗███╗   ██╗███╗   ██╗███████╗██╗            ║
║   ╚══██╔══╝██║   ██║████╗  ██║████╗  ██║██╔════╝██║            ║
║      ██║   ██║   ██║██╔██╗ ██║██╔██╗ ██║█████╗  ██║            ║
║      ██║   ██║   ██║██║╚██╗██║██║╚██╗██║██╔══╝  ██║            ║
║      ██║   ╚██████╔╝██║ ╚████║██║ ╚████║███████╗███████╗       ║
║      ╚═╝    ╚═════╝ ╚═╝  ╚═══╝╚═╝  ╚═══╝╚══════╝╚══════╝       ║
║                                                                ║
║            ███╗   ███╗███████╗███████╗██╗  ██╗                 ║
║            ████╗ ████║██╔════╝██╔════╝██║  ██║                 ║
║            ██╔████╔██║█████╗  ███████╗███████║                 ║
║            ██║╚██╔╝██║██╔══╝  ╚════██║██╔══██║                 ║
║            ██║ ╚═╝ ██║███████╗███████║██║  ██║                 ║
║            ╚═╝     ╚═╝╚══════╝╚══════╝╚═╝  ╚═╝                 ║
║                                                                ║
║            P2P Encrypted Mesh Network                          ║
║                   written by zombar                            ║
║                                                                ║
╚════════════════════════════════════════════════════════════════╝`

	fmt.Fprintln(os.Stderr, banner)
	fmt.Fprintf(os.Stderr, "\n  Version:    %s\n", Version)
	fmt.Fprintf(os.Stderr, "  Commit:     %s\n", Commit)
	fmt.Fprintf(os.Stderr, "  Build Time: %s\n", BuildTime)
	fmt.Fprintf(os.Stderr, "  Go:         %s\n", runtime.Version())
	fmt.Fprintf(os.Stderr, "  OS/Arch:    %s/%s\n\n", runtime.GOOS, runtime.GOARCH)
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
