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
)

// FetchCA fetches the CA certificate from the given server URL and returns the PEM bytes.
// This is used by the join command to store the CA locally.
func FetchCA(serverURL string) ([]byte, error) {
	// Normalize server URL
	server := strings.TrimSuffix(serverURL, "/")

	log.Debug().Str("server", server).Msg("fetching CA certificate")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(server + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("fetch CA cert: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned status %d", resp.StatusCode)
	}

	caPEM, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if len(caPEM) == 0 {
		return nil, fmt.Errorf("empty CA certificate")
	}

	return caPEM, nil
}

// InstallCA installs the given CA certificate PEM into the system trust store.
func InstallCA(caPEM []byte, caName string) error {
	if caName == "" {
		caName = "TunnelMesh CA"
	}

	// Save to temp file
	tmpFile, err := os.CreateTemp("", "tunnelmesh-ca-*.crt")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmpFile.Write(caPEM); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	_ = tmpFile.Close()

	return installCAWithName(tmpPath, caName)
}

// RemoveCA removes the TunnelMesh CA certificate from the system trust store.
func RemoveCA() error {
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

// runCmd executes a command with stdout/stderr/stdin attached.
func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// macOS implementation
func installCAMacOS(certPath, caName string) error {
	log.Info().Msg("installing CA certificate (requires sudo)")

	// Remove any existing TunnelMesh CA with same name to avoid duplicates
	_ = removeCAMacOSByName(caName, false) // Ignore error if not found

	// Add to system keychain
	if err := runCmd("sudo", "security", "add-trusted-cert",
		"-d", "-r", "trustRoot",
		"-k", "/Library/Keychains/System.keychain",
		certPath); err != nil {
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

// Linux distro configuration
type linuxDistro struct {
	name       string
	certPath   string
	updateCmd  []string
	detectFile string
}

var linuxDistros = []linuxDistro{
	{"Debian/Ubuntu", "/usr/local/share/ca-certificates/tunnelmesh-ca.crt", []string{"update-ca-certificates"}, "/etc/debian_version"},
	{"RHEL/Fedora", "/etc/pki/ca-trust/source/anchors/tunnelmesh-ca.crt", []string{"update-ca-trust"}, "/etc/redhat-release"},
	{"Arch Linux", "/etc/ca-certificates/trust-source/anchors/tunnelmesh-ca.crt", []string{"trust", "extract-compat"}, "/etc/arch-release"},
}

func detectLinuxDistro() *linuxDistro {
	for i := range linuxDistros {
		if fileExists(linuxDistros[i].detectFile) {
			return &linuxDistros[i]
		}
	}
	// Also check for Fedora specifically
	if fileExists("/etc/fedora-release") {
		return &linuxDistros[1] // RHEL/Fedora
	}
	return nil
}

// Linux implementation
func installCALinux(certPath string) error {
	distro := detectLinuxDistro()
	if distro == nil {
		log.Warn().Msg("unknown Linux distribution, trying Debian/Ubuntu method")
		distro = &linuxDistros[0]
	}

	log.Info().Str("distro", distro.name).Msg("installing CA certificate (requires sudo)")

	if err := runCmd("sudo", "cp", certPath, distro.certPath); err != nil {
		return fmt.Errorf("copy cert failed: %w", err)
	}

	updateArgs := append([]string{distro.updateCmd[0]}, distro.updateCmd[1:]...)
	if err := runCmd("sudo", updateArgs...); err != nil {
		return fmt.Errorf("%s failed: %w", distro.updateCmd[0], err)
	}

	log.Info().Msg("CA certificate installed successfully")
	return nil
}

func removeCALinux() error {
	log.Info().Msg("removing CA certificate (requires sudo)")

	// Try to remove from all known locations
	var removed bool
	for _, distro := range linuxDistros {
		if fileExists(distro.certPath) {
			if err := runCmd("sudo", "rm", distro.certPath); err != nil {
				log.Warn().Str("path", distro.certPath).Err(err).Msg("failed to remove")
			} else {
				removed = true
			}
		}
	}

	if !removed {
		log.Warn().Msg("no CA certificate found to remove")
		return nil
	}

	// Update CA store
	if distro := detectLinuxDistro(); distro != nil {
		updateArgs := append([]string{distro.updateCmd[0]}, distro.updateCmd[1:]...)
		_ = runCmd("sudo", updateArgs...)
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
	if err := runCmd("certutil", "-addstore", "-f", "ROOT", absPath); err != nil {
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

// IsCATrusted checks if a TunnelMesh CA certificate is installed in the system trust store.
// It returns true if a CA with "TunnelMesh" in the name is found.
func IsCATrusted() (bool, error) {
	switch runtime.GOOS {
	case "darwin":
		return isCATrustedMacOS()
	case "linux":
		return isCATrustedLinux()
	case "windows":
		return isCATrustedWindows()
	default:
		return false, fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

func isCATrustedMacOS() (bool, error) {
	// Use security find-certificate to search for TunnelMesh CA
	cmd := exec.Command("security", "find-certificate", "-c", "TunnelMesh CA", "/Library/Keychains/System.keychain")
	output, err := cmd.Output()
	if err != nil {
		// Command fails if cert not found
		return false, nil
	}
	// If we got output, the cert exists
	return len(output) > 0, nil
}

func isCATrustedLinux() (bool, error) {
	// Check if cert file exists in any known location
	for _, distro := range linuxDistros {
		if fileExists(distro.certPath) {
			return true, nil
		}
	}
	return false, nil
}

func isCATrustedWindows() (bool, error) {
	// Use certutil to check if TunnelMesh CA exists in root store
	cmd := exec.Command("certutil", "-store", "ROOT")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("certutil failed: %w", err)
	}
	// Check if output contains TunnelMesh CA
	return strings.Contains(string(output), "TunnelMesh CA"), nil
}
