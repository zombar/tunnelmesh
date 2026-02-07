package main

import (
	"fmt"
	"os"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/context"
	"github.com/tunnelmesh/tunnelmesh/internal/svc"
)

var (
	serviceMode       string
	serviceConfigPath string
	serviceContext    string
	serviceUser       string
	forceInstall      bool
	logsFollow        bool
	logsLines         int
)

func newServiceCmd() *cobra.Command {
	serviceCmd := &cobra.Command{
		Use:   "service",
		Short: "Manage TunnelMesh system service",
		Long: `Install, control, and manage TunnelMesh as a system service.

Supported platforms:
  - Linux (systemd)
  - macOS (launchd)
  - Windows (Service Control Manager)

Service names are derived from context names:
  - Context "work" → Service "tunnelmesh-work"
  - Context "default" → Service "tunnelmesh"

Examples:
  # Install service for active context
  sudo tunnelmesh service install

  # Install service for a specific context
  sudo tunnelmesh service install --context work

  # Install as server service (uses --mode serve)
  sudo tunnelmesh service install --mode serve --config /etc/tunnelmesh/server.yaml

  # Control the service
  sudo tunnelmesh service start
  sudo tunnelmesh service stop
  sudo tunnelmesh service status

  # View logs
  sudo tunnelmesh service logs --follow`,
	}

	// Install subcommand
	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Install TunnelMesh as a system service",
		Long: `Install TunnelMesh as a system service that starts automatically at boot.

For peer mode (--mode join), uses the active context or --context flag to determine
the service name and configuration. For server mode (--mode serve), requires --config.

Requires administrator/root privileges.`,
		RunE: runServiceInstall,
	}
	installCmd.Flags().StringVar(&serviceMode, "mode", "join", "Service mode: 'serve' (coordination server) or 'join' (peer)")
	installCmd.Flags().StringVarP(&serviceConfigPath, "config", "c", "", "Path to configuration file (required for serve mode)")
	installCmd.Flags().StringVar(&serviceContext, "context", "", "Context name (uses active context if not specified)")
	installCmd.Flags().StringVar(&serviceUser, "user", "", "Run service as this user (Linux/macOS only)")
	installCmd.Flags().BoolVarP(&forceInstall, "force", "f", false, "Force reinstall if service already exists")
	serviceCmd.AddCommand(installCmd)

	// Uninstall subcommand
	uninstallCmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the TunnelMesh system service",
		RunE:  runServiceUninstall,
	}
	uninstallCmd.Flags().StringVar(&serviceContext, "context", "", "Context name (uses active context if not specified)")
	serviceCmd.AddCommand(uninstallCmd)

	// Start subcommand
	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start the TunnelMesh service",
		RunE:  runServiceStart,
	}
	startCmd.Flags().StringVar(&serviceContext, "context", "", "Context name (uses active context if not specified)")
	serviceCmd.AddCommand(startCmd)

	// Stop subcommand
	stopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the TunnelMesh service",
		RunE:  runServiceStop,
	}
	stopCmd.Flags().StringVar(&serviceContext, "context", "", "Context name (uses active context if not specified)")
	serviceCmd.AddCommand(stopCmd)

	// Restart subcommand
	restartCmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart the TunnelMesh service",
		RunE:  runServiceRestart,
	}
	restartCmd.Flags().StringVar(&serviceContext, "context", "", "Context name (uses active context if not specified)")
	serviceCmd.AddCommand(restartCmd)

	// Status subcommand
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show TunnelMesh service status",
		RunE:  runServiceStatus,
	}
	statusCmd.Flags().StringVar(&serviceContext, "context", "", "Context name (uses active context if not specified)")
	serviceCmd.AddCommand(statusCmd)

	// Logs subcommand
	logsCmd := &cobra.Command{
		Use:   "logs",
		Short: "View TunnelMesh service logs",
		Long: `View logs from the TunnelMesh service.

Log locations by platform:
  - Linux:   journalctl -u tunnelmesh
  - macOS:   log show/stream with subsystem filter
  - Windows: Event Viewer > Application log`,
		RunE: runServiceLogs,
	}
	logsCmd.Flags().StringVar(&serviceContext, "context", "", "Context name (uses active context if not specified)")
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "Follow log output (like tail -f)")
	logsCmd.Flags().IntVar(&logsLines, "lines", 50, "Number of log lines to show")
	serviceCmd.AddCommand(logsCmd)

	return serviceCmd
}

// getServiceConfig returns service configuration based on context or explicit flags.
// For serve mode, it uses traditional config-based approach.
// For join mode, it uses the context system.
func getServiceConfig() (*svc.ServiceConfig, error) {
	mode := serviceMode
	if mode == "" {
		mode = "join"
	}

	// For serve mode, use traditional config-based approach
	if mode == "serve" {
		configPath := serviceConfigPath
		if configPath == "" {
			configPath = svc.DefaultConfigPath(mode)
		}
		return &svc.ServiceConfig{
			Name:        svc.DefaultServiceName(mode),
			DisplayName: svc.DefaultDisplayName(mode),
			Description: svc.DefaultDescription(mode),
			Mode:        mode,
			ConfigPath:  configPath,
			UserName:    serviceUser,
		}, nil
	}

	// For join mode, use context system
	store, err := context.Load()
	if err != nil {
		return nil, fmt.Errorf("load context store: %w", err)
	}

	// Use specified context or active context
	ctxName := serviceContext
	if ctxName == "" {
		ctxName = store.Active
	}
	if ctxName == "" {
		return nil, fmt.Errorf("no active context; use --context or 'tunnelmesh context use <name>'")
	}

	ctx := store.Get(ctxName)
	if ctx == nil {
		return nil, fmt.Errorf("context %q not found", ctxName)
	}

	// If explicit config path provided, use it
	configPath := serviceConfigPath
	if configPath == "" {
		configPath = ctx.ConfigPath
	}
	if configPath == "" {
		return nil, fmt.Errorf("context %q has no config path; use --config to specify one", ctxName)
	}

	return &svc.ServiceConfig{
		Name:        ctx.ServiceName(),
		DisplayName: fmt.Sprintf("TunnelMesh Peer (%s)", ctxName),
		Description: fmt.Sprintf("TunnelMesh P2P mesh network peer daemon for context %q", ctxName),
		Mode:        mode,
		ConfigPath:  configPath,
		UserName:    serviceUser,
	}, nil
}

func runServiceInstall(cmd *cobra.Command, args []string) error {
	setupLogging()

	// Check privileges
	if err := svc.CheckPrivileges(); err != nil {
		return err
	}

	// Validate mode
	if serviceMode != "serve" && serviceMode != "join" {
		return fmt.Errorf("invalid mode %q: must be 'serve' or 'join'", serviceMode)
	}

	cfg, err := getServiceConfig()
	if err != nil {
		return err
	}

	// Validate config file exists
	if _, err := os.Stat(cfg.ConfigPath); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s\nCreate the config file first or specify a different path with --config", cfg.ConfigPath)
	}

	log.Info().
		Str("name", cfg.Name).
		Str("mode", cfg.Mode).
		Str("config", cfg.ConfigPath).
		Msg("installing service")

	if err := svc.Install(cfg, forceInstall); err != nil {
		return err
	}

	fmt.Printf("Service %q installed successfully.\n", cfg.Name)
	fmt.Printf("\nTo start the service:\n")
	fmt.Printf("  tunnelmesh service start\n")
	fmt.Printf("\nTo view logs:\n")
	fmt.Printf("  tunnelmesh service logs\n")

	return nil
}

func runServiceUninstall(cmd *cobra.Command, args []string) error {
	setupLogging()

	if err := svc.CheckPrivileges(); err != nil {
		return err
	}

	cfg, err := getServiceConfig()
	if err != nil {
		return err
	}

	log.Info().Str("name", cfg.Name).Msg("uninstalling service")

	if err := svc.Uninstall(cfg); err != nil {
		return err
	}

	fmt.Printf("Service %q uninstalled successfully.\n", cfg.Name)
	return nil
}

func runServiceStart(cmd *cobra.Command, args []string) error {
	setupLogging()

	if err := svc.CheckPrivileges(); err != nil {
		return err
	}

	cfg, err := getServiceConfig()
	if err != nil {
		return err
	}

	log.Info().Str("name", cfg.Name).Msg("starting service")

	if err := svc.Start(cfg); err != nil {
		return err
	}

	fmt.Printf("Service %q started.\n", cfg.Name)
	return nil
}

func runServiceStop(cmd *cobra.Command, args []string) error {
	setupLogging()

	if err := svc.CheckPrivileges(); err != nil {
		return err
	}

	cfg, err := getServiceConfig()
	if err != nil {
		return err
	}

	log.Info().Str("name", cfg.Name).Msg("stopping service")

	if err := svc.Stop(cfg); err != nil {
		return err
	}

	fmt.Printf("Service %q stopped.\n", cfg.Name)
	return nil
}

func runServiceRestart(cmd *cobra.Command, args []string) error {
	setupLogging()

	if err := svc.CheckPrivileges(); err != nil {
		return err
	}

	cfg, err := getServiceConfig()
	if err != nil {
		return err
	}

	log.Info().Str("name", cfg.Name).Msg("restarting service")

	if err := svc.Restart(cfg); err != nil {
		return err
	}

	fmt.Printf("Service %q restarted.\n", cfg.Name)
	return nil
}

func runServiceStatus(cmd *cobra.Command, args []string) error {
	setupLogging()

	cfg, err := getServiceConfig()
	if err != nil {
		return err
	}

	status, err := svc.Status(cfg)
	if err != nil {
		// Service might not be installed
		fmt.Printf("Service: %s\n", cfg.Name)
		fmt.Printf("Status:  not installed or unknown\n")
		fmt.Printf("Error:   %v\n", err)
		return nil
	}

	fmt.Printf("Service: %s\n", cfg.Name)
	fmt.Printf("Status:  %s\n", svc.StatusString(status))
	fmt.Printf("Mode:    %s\n", cfg.Mode)
	fmt.Printf("Config:  %s\n", cfg.ConfigPath)

	return nil
}

func runServiceLogs(cmd *cobra.Command, args []string) error {
	cfg, err := getServiceConfig()
	if err != nil {
		return err
	}

	return svc.ViewLogs(svc.LogOptions{
		ServiceName: cfg.Name,
		Follow:      logsFollow,
		Lines:       logsLines,
	})
}
