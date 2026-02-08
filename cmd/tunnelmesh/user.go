package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/context"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

var (
	userName string
)

func newUserCmd() *cobra.Command {
	userCmd := &cobra.Command{
		Use:   "user",
		Short: "Manage user identity",
		Long: `Manage your TunnelMesh user identity.

Your identity is based on a 3-word recovery phrase that derives an ED25519 keypair.
This identity is portable - you can use it across multiple TunnelMesh networks.

Examples:
  # Create a new identity
  tunnelmesh user setup

  # Recover an existing identity from your recovery phrase
  tunnelmesh user recover

  # Show your current identity
  tunnelmesh user info`,
	}

	// Setup subcommand
	setupCmd := &cobra.Command{
		Use:   "setup",
		Short: "Create a new user identity",
		Long: `Generate a new 3-word recovery phrase and derive your identity.

IMPORTANT: Store your recovery phrase safely! You will need it to
recover your identity on other devices or if your local data is lost.`,
		RunE: runUserSetup,
	}
	setupCmd.Flags().StringVar(&userName, "name", "", "Optional display name for your identity")
	userCmd.AddCommand(setupCmd)

	// Recover subcommand
	recoverCmd := &cobra.Command{
		Use:   "recover",
		Short: "Recover identity from recovery phrase",
		Long:  `Recover your identity by entering your 3-word recovery phrase.`,
		RunE:  runUserRecover,
	}
	recoverCmd.Flags().StringVar(&userName, "name", "", "Optional display name for your identity")
	userCmd.AddCommand(recoverCmd)

	// Info subcommand
	infoCmd := &cobra.Command{
		Use:   "info",
		Short: "Show current identity",
		Long:  `Display information about your current identity and registration status.`,
		RunE:  runUserInfo,
	}
	userCmd.AddCommand(infoCmd)

	// Register subcommand
	registerCmd := &cobra.Command{
		Use:   "register",
		Short: "Register with current mesh",
		Long: `Register your identity with a mesh coordinator.

This connects to the coordinator and registers your public key.
The first user to register becomes an admin.

You can either:
1. Use an active context (run 'tunnelmesh context use <name>')
2. Specify the server directly with --server (for coordinator admins)

Examples:
  # Register using active context
  tunnelmesh user register

  # Register directly with a server (coordinator admin)
  tunnelmesh user register --server https://localhost:8443`,
		RunE: runUserRegister,
	}
	registerCmd.Flags().String("server", "", "Server URL to register with (bypasses context)")
	userCmd.AddCommand(registerCmd)

	return userCmd
}

func runUserSetup(cmd *cobra.Command, args []string) error {
	identityPath, err := defaultIdentityPath()
	if err != nil {
		return fmt.Errorf("get identity path: %w", err)
	}

	// Check if identity already exists
	if _, err := os.Stat(identityPath); err == nil {
		fmt.Println("An identity already exists at", identityPath)
		fmt.Print("Overwrite? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		response, _ := reader.ReadString('\n')
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	// Use OS username if not provided via flag
	if userName == "" {
		if u, err := user.Current(); err == nil {
			userName = u.Username
		}
	}

	// Generate mnemonic
	mnemonic, err := auth.GenerateMnemonic()
	if err != nil {
		return fmt.Errorf("generate mnemonic: %w", err)
	}

	// Create identity
	identity, err := auth.NewUserIdentity(mnemonic, userName)
	if err != nil {
		return fmt.Errorf("create identity: %w", err)
	}

	// Save identity
	if err := identity.Save(identityPath); err != nil {
		return fmt.Errorf("save identity: %w", err)
	}

	fmt.Println()
	fmt.Println("Your recovery phrase is:")
	fmt.Println()
	fmt.Printf("    %s\n", mnemonic)
	fmt.Println()
	fmt.Println("IMPORTANT: Store this phrase safely! You will need it to recover your identity.")
	fmt.Println()
	fmt.Printf("User ID: %s\n", identity.User.ID)
	if identity.User.Name != "" {
		fmt.Printf("Name:    %s\n", identity.User.Name)
	}
	fmt.Printf("Saved:   %s\n", identityPath)

	return nil
}

func runUserRecover(cmd *cobra.Command, args []string) error {
	identityPath, err := defaultIdentityPath()
	if err != nil {
		return fmt.Errorf("get identity path: %w", err)
	}

	// Check if identity already exists
	if _, err := os.Stat(identityPath); err == nil {
		fmt.Println("An identity already exists at", identityPath)
		fmt.Print("Overwrite? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		response, _ := reader.ReadString('\n')
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	// Prompt for mnemonic
	fmt.Print("Enter your 3-word recovery phrase: ")
	reader := bufio.NewReader(os.Stdin)
	mnemonic, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	mnemonic = strings.TrimSpace(mnemonic)

	// Validate mnemonic
	if err := auth.ValidateMnemonic(mnemonic); err != nil {
		return fmt.Errorf("invalid recovery phrase: %w", err)
	}

	// Prompt for name if not provided via flag
	if userName == "" {
		fmt.Print("Enter your display name: ")
		name, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read name: %w", err)
		}
		userName = strings.TrimSpace(name)
	}

	// Create identity
	identity, err := auth.NewUserIdentity(mnemonic, userName)
	if err != nil {
		return fmt.Errorf("create identity: %w", err)
	}

	// Save identity
	if err := identity.Save(identityPath); err != nil {
		return fmt.Errorf("save identity: %w", err)
	}

	fmt.Println()
	fmt.Println("Identity recovered successfully!")
	fmt.Printf("User ID: %s\n", identity.User.ID)
	if identity.User.Name != "" {
		fmt.Printf("Name:    %s\n", identity.User.Name)
	}
	fmt.Printf("Saved:   %s\n", identityPath)

	return nil
}

func runUserInfo(cmd *cobra.Command, args []string) error {
	identityPath, err := defaultIdentityPath()
	if err != nil {
		return fmt.Errorf("get identity path: %w", err)
	}

	identity, err := auth.LoadUserIdentity(identityPath)
	if os.IsNotExist(err) {
		fmt.Println("No identity found. Run 'tunnelmesh user setup' to create one.")
		return nil
	}
	if err != nil {
		return fmt.Errorf("load identity: %w", err)
	}

	fmt.Printf("User ID:    %s\n", identity.User.ID)
	if identity.User.Name != "" {
		fmt.Printf("Name:       %s\n", identity.User.Name)
	}
	fmt.Printf("Public Key: %s\n", identity.User.PublicKey)
	fmt.Printf("Created:    %s\n", identity.User.CreatedAt.Format("2006-01-02 15:04:05 UTC"))

	// Show registration status for active context
	store, err := context.Load()
	if err != nil {
		return nil // Ignore context errors
	}

	activeCtx := store.GetActive()
	if activeCtx != nil {
		fmt.Println()
		fmt.Printf("Active Context: %s\n", activeCtx.Name)
		if activeCtx.IsRegistered() {
			fmt.Printf("Registered:     Yes (ID: %s)\n", activeCtx.UserID)
			// Try to load registration for more details
			if activeCtx.RegistrationPath != "" {
				if reg, err := auth.LoadRegistration(activeCtx.RegistrationPath); err == nil {
					if len(reg.Roles) > 0 {
						fmt.Printf("Roles:          %s\n", strings.Join(reg.Roles, ", "))
					}
				}
			}
		} else {
			fmt.Println("Registered:     No")
			fmt.Println("Run 'tunnelmesh user register' to register with this mesh.")
		}
	}

	return nil
}

func runUserRegister(cmd *cobra.Command, args []string) error {
	// Load identity
	identityPath, err := defaultIdentityPath()
	if err != nil {
		return fmt.Errorf("get identity path: %w", err)
	}

	identity, err := auth.LoadUserIdentity(identityPath)
	if os.IsNotExist(err) {
		// No identity exists - run setup first
		fmt.Println("No user identity found. Setting up...")
		fmt.Println()

		// Use OS username as display name
		var userName string
		if u, err := user.Current(); err == nil {
			userName = u.Username
		}

		// Generate mnemonic
		mnemonic, err := auth.GenerateMnemonic()
		if err != nil {
			return fmt.Errorf("generate mnemonic: %w", err)
		}

		// Create identity
		identity, err = auth.NewUserIdentity(mnemonic, userName)
		if err != nil {
			return fmt.Errorf("create identity: %w", err)
		}

		// Save identity
		if err := identity.Save(identityPath); err != nil {
			return fmt.Errorf("save identity: %w", err)
		}

		fmt.Println()
		fmt.Println("Identity created successfully!")
		fmt.Printf("User ID: %s\n", identity.User.ID)
		fmt.Println()
		fmt.Println("IMPORTANT: Save your recovery phrase in a safe place:")
		fmt.Println()
		fmt.Printf("  %s\n", mnemonic)
		fmt.Println()
	} else if err != nil {
		return fmt.Errorf("load identity: %w", err)
	}

	// Get server URL - either from --server flag or from active context
	serverFlag, _ := cmd.Flags().GetString("server")
	var serverURL string
	var activeCtx *context.Context
	var contextName string

	if serverFlag != "" {
		// Direct server registration (for coordinator admins)
		serverURL = serverFlag
		contextName = "direct"
	} else {
		// Load context store
		store, err := context.Load()
		if err != nil {
			return fmt.Errorf("load context: %w", err)
		}

		activeCtx = store.GetActive()
		if activeCtx == nil {
			return fmt.Errorf("no active context. Run 'tunnelmesh context use <name>' or use --server flag")
		}

		// Get server address from context or config file
		if activeCtx.Server != "" {
			// Context has server URL directly (created via join with --server flag)
			serverURL = activeCtx.Server
		} else if activeCtx.ConfigPath != "" {
			// Load from config file
			peerCfg, err := config.LoadPeerConfig(activeCtx.ConfigPath)
			if err != nil {
				return fmt.Errorf("load peer config: %w", err)
			}
			serverURL = peerCfg.Server
		} else {
			return fmt.Errorf("context %q has no server URL or config path", activeCtx.Name)
		}
		contextName = activeCtx.Name
	}

	if !strings.HasPrefix(serverURL, "http") {
		serverURL = "https://" + serverURL
	}

	// Sign the user ID to prove ownership
	signature, err := identity.SignMessage(identity.User.ID)
	if err != nil {
		return fmt.Errorf("sign user ID: %w", err)
	}

	// Create registration request
	req := proto.UserRegisterRequest{
		UserID:    identity.User.ID,
		PublicKey: identity.User.PublicKey,
		Name:      identity.User.Name,
		Signature: signature,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	// Build the registration URL
	registerURL := strings.TrimSuffix(serverURL, "/") + "/api/v1/user/register"

	// Create HTTP client with TLS config that skips verification for self-signed certs
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // Allow self-signed mesh CA certs
			},
		},
	}

	httpReq, err := http.NewRequest(http.MethodPost, registerURL, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	fmt.Printf("Registering with %s...\n", contextName)

	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp proto.ErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Message != "" {
			return fmt.Errorf("registration failed: %s", errResp.Message)
		}
		return fmt.Errorf("registration failed: %s", resp.Status)
	}

	var regResp proto.UserRegisterResponse
	if err := json.Unmarshal(body, &regResp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	// Save registration
	regPath := auth.DefaultRegistrationPath(contextName)
	meshDomain := ""
	if activeCtx != nil {
		meshDomain = activeCtx.Domain
	}
	reg := &auth.Registration{
		UserID:       regResp.UserID,
		MeshDomain:   meshDomain,
		RegisteredAt: time.Now().UTC(),
		Roles:        regResp.Roles,
		S3AccessKey:  regResp.S3AccessKey,
		S3SecretKey:  regResp.S3SecretKey,
	}

	if err := reg.Save(regPath); err != nil {
		return fmt.Errorf("save registration: %w", err)
	}

	// Update context with registration info (if using a context)
	if activeCtx != nil {
		store, _ := context.Load()
		activeCtx.UserID = regResp.UserID
		activeCtx.RegistrationPath = regPath
		store.Add(*activeCtx)
		if err := store.Save(); err != nil {
			fmt.Printf("Warning: failed to update context store: %v\n", err)
		}
	}

	// Print result
	fmt.Println()
	fmt.Println("Registration successful!")
	fmt.Printf("User ID: %s\n", regResp.UserID)
	fmt.Printf("Roles:   %s\n", strings.Join(regResp.Roles, ", "))

	if regResp.IsFirstUser {
		fmt.Println()
		fmt.Println("You are the first user - you have been granted admin role.")
	}

	if regResp.S3AccessKey != "" {
		fmt.Println()
		fmt.Println("S3 Credentials:")
		fmt.Printf("  Access Key: %s\n", regResp.S3AccessKey)
		fmt.Printf("  Secret Key: %s\n", regResp.S3SecretKey)
	}

	fmt.Printf("\nRegistration saved: %s\n", regPath)

	return nil
}

func defaultIdentityPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".tunnelmesh", "user.json"), nil
}
