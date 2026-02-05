package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	trustCAServer string
	trustCARemove bool
)

func newTrustCACmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trust-ca",
		Short: "Install mesh CA certificate in system trust store",
		Long: `Downloads the TunnelMesh CA certificate from the coordination server and
installs it in the system trust store. This allows HTTPS connections to
mesh services (like https://this.tunnelmesh) to work without browser warnings.

Requires administrator/root privileges on most systems.

Examples:
  # Install CA cert from server
  tunnelmesh trust-ca --server http://coord.example.com:8080

  # Remove previously installed CA cert
  tunnelmesh trust-ca --server http://coord.example.com:8080 --remove`,
		RunE: runTrustCA,
	}
	cmd.Flags().StringVar(&trustCAServer, "server", "", "coordination server URL (required)")
	cmd.Flags().BoolVar(&trustCARemove, "remove", false, "remove the CA certificate instead of installing")
	_ = cmd.MarkFlagRequired("server")
	return cmd
}

func runTrustCA(cmd *cobra.Command, args []string) error {
	setupLogging()

	if trustCARemove {
		return removeCAFromSystem()
	}
	return InstallCAFromServer(trustCAServer)
}

// InstallCAFromServer fetches and installs the CA certificate from the given server URL.
// This is exported so it can be called from the join command with --trust-ca flag.
func InstallCAFromServer(serverURL string) error {
	// Normalize server URL
	server := strings.TrimSuffix(serverURL, "/")

	// Fetch CA cert
	log.Info().Str("server", server).Msg("fetching CA certificate")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(server + "/ca.crt")
	if err != nil {
		return fmt.Errorf("fetch CA cert: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned status %d", resp.StatusCode)
	}

	// Get CA name from header (used for removal)
	caName := resp.Header.Get("X-TunnelMesh-CA-Name")
	if caName == "" {
		caName = "TunnelMesh CA" // Fallback for older servers
	}

	caPEM, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if len(caPEM) == 0 {
		return fmt.Errorf("empty CA certificate")
	}

	// Save to temp file
	tmpFile, err := os.CreateTemp("", "tunnelmesh-ca-*.crt")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(caPEM); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	tmpFile.Close()

	return installCAWithName(tmpPath, caName)
}

func removeCAFromSystem() error {
	return removeCA("")
}

func installCAWithName(certPath, caName string) error {
	switch runtime.GOOS {
	case "darwin":
		return installCAMacOS(certPath, caName)
	case "linux":
		return installCALinux(certPath)
	case "windows":
		return installCAWindows(certPath, caName)
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

func removeCA(certPath string) error {
	switch runtime.GOOS {
	case "darwin":
		return removeCAMacOS()
	case "linux":
		return removeCALinux()
	case "windows":
		return removeCAWindows()
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// macOS implementation
func installCAMacOS(certPath, caName string) error {
	log.Info().Msg("installing CA certificate (requires sudo)")

	// Remove any existing TunnelMesh CA with same name to avoid duplicates
	_ = removeCAMacOSByName(caName, false) // Ignore error if not found

	// Add to system keychain
	cmd := exec.Command("sudo", "security", "add-trusted-cert",
		"-d", "-r", "trustRoot",
		"-k", "/Library/Keychains/System.keychain",
		certPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("security add-trusted-cert failed: %w", err)
	}

	log.Info().Msg("CA certificate installed successfully")
	log.Info().Msg("you may need to restart your browser for changes to take effect")
	return nil
}

func removeCAMacOS() error {
	// Remove all TunnelMesh CA certs (any domain suffix)
	return removeCAMacOSByName("TunnelMesh CA", true)
}

func removeCAMacOSByName(caName string, verbose bool) error {
	if verbose {
		log.Info().Str("name", caName).Msg("removing CA certificate (requires sudo)")
	}

	// Find and delete the cert by name (-c does substring match)
	cmd := exec.Command("sudo", "security", "delete-certificate",
		"-c", caName,
		"/Library/Keychains/System.keychain")
	if verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	cmd.Stdin = os.Stdin

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("security delete-certificate failed: %w", err)
	}

	if verbose {
		log.Info().Msg("CA certificate removed successfully")
	}
	return nil
}

// Linux implementation
func installCALinux(certPath string) error {
	// Detect distro and use appropriate method
	if fileExists("/etc/debian_version") {
		return installCADebian(certPath)
	}
	if fileExists("/etc/redhat-release") || fileExists("/etc/fedora-release") {
		return installCARedHat(certPath)
	}
	if fileExists("/etc/arch-release") {
		return installCAArch(certPath)
	}

	// Fallback: try Debian method
	log.Warn().Msg("unknown Linux distribution, trying Debian/Ubuntu method")
	return installCADebian(certPath)
}

func installCADebian(certPath string) error {
	log.Info().Msg("installing CA certificate (Debian/Ubuntu, requires sudo)")

	destPath := "/usr/local/share/ca-certificates/tunnelmesh-ca.crt"

	// Copy cert
	cmd := exec.Command("sudo", "cp", certPath, destPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("copy cert failed: %w", err)
	}

	// Update CA certificates
	cmd = exec.Command("sudo", "update-ca-certificates")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("update-ca-certificates failed: %w", err)
	}

	log.Info().Msg("CA certificate installed successfully")
	return nil
}

func installCARedHat(certPath string) error {
	log.Info().Msg("installing CA certificate (RHEL/Fedora, requires sudo)")

	destPath := "/etc/pki/ca-trust/source/anchors/tunnelmesh-ca.crt"

	// Copy cert
	cmd := exec.Command("sudo", "cp", certPath, destPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("copy cert failed: %w", err)
	}

	// Update CA trust
	cmd = exec.Command("sudo", "update-ca-trust")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("update-ca-trust failed: %w", err)
	}

	log.Info().Msg("CA certificate installed successfully")
	return nil
}

func installCAArch(certPath string) error {
	log.Info().Msg("installing CA certificate (Arch Linux, requires sudo)")

	destPath := "/etc/ca-certificates/trust-source/anchors/tunnelmesh-ca.crt"

	// Copy cert
	cmd := exec.Command("sudo", "cp", certPath, destPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("copy cert failed: %w", err)
	}

	// Update CA trust
	cmd = exec.Command("sudo", "trust", "extract-compat")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("trust extract-compat failed: %w", err)
	}

	log.Info().Msg("CA certificate installed successfully")
	return nil
}

func removeCALinux() error {
	// Try to remove from all known locations
	paths := []string{
		"/usr/local/share/ca-certificates/tunnelmesh-ca.crt",
		"/etc/pki/ca-trust/source/anchors/tunnelmesh-ca.crt",
		"/etc/ca-certificates/trust-source/anchors/tunnelmesh-ca.crt",
	}

	var removed bool
	for _, path := range paths {
		if fileExists(path) {
			cmd := exec.Command("sudo", "rm", path)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Stdin = os.Stdin
			if err := cmd.Run(); err != nil {
				log.Warn().Str("path", path).Err(err).Msg("failed to remove")
			} else {
				removed = true
			}
		}
	}

	if !removed {
		log.Warn().Msg("no CA certificate found to remove")
		return nil
	}

	// Update CA certificates
	if fileExists("/etc/debian_version") {
		cmd := exec.Command("sudo", "update-ca-certificates")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		_ = cmd.Run()
	} else if fileExists("/etc/redhat-release") || fileExists("/etc/fedora-release") {
		cmd := exec.Command("sudo", "update-ca-trust")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		_ = cmd.Run()
	} else if fileExists("/etc/arch-release") {
		cmd := exec.Command("sudo", "trust", "extract-compat")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		_ = cmd.Run()
	}

	log.Info().Msg("CA certificate removed successfully")
	return nil
}

// Windows implementation
func installCAWindows(certPath, caName string) error {
	log.Info().Msg("installing CA certificate (requires Administrator)")

	// Remove any existing TunnelMesh CA with same name to avoid duplicates
	_ = removeCAWindowsByName(caName, false) // Ignore error if not found

	// Convert path to Windows format
	absPath, err := filepath.Abs(certPath)
	if err != nil {
		return fmt.Errorf("get absolute path: %w", err)
	}

	// Use certutil to add to root store
	cmd := exec.Command("certutil", "-addstore", "-f", "ROOT", absPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("certutil failed (run as Administrator): %w", err)
	}

	log.Info().Msg("CA certificate installed successfully")
	return nil
}

func removeCAWindows() error {
	// Remove all TunnelMesh CA certs (any domain suffix)
	return removeCAWindowsByName("TunnelMesh CA", true)
}

func removeCAWindowsByName(caName string, verbose bool) error {
	if verbose {
		log.Info().Str("name", caName).Msg("removing CA certificate (requires Administrator)")
	}

	// Use certutil to delete from root store
	cmd := exec.Command("certutil", "-delstore", "ROOT", caName)
	if verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("certutil failed (run as Administrator): %w", err)
	}

	if verbose {
		log.Info().Msg("CA certificate removed successfully")
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
