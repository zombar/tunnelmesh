package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/svc"
	"github.com/tunnelmesh/tunnelmesh/internal/update"
)

var (
	updateCheck     bool
	updateForce     bool
	updateNoRestart bool
)

func newUpdateCmd() *cobra.Command {
	updateCmd := &cobra.Command{
		Use:   "update [version]",
		Short: "Update tunnelmesh to the latest version",
		Long: `Update tunnelmesh to the latest version from GitHub releases.

By default, downloads the latest release. Optionally specify a version tag.

Examples:
  tunnelmesh update           # Update to latest
  tunnelmesh update v1.2.3    # Update to specific version
  tunnelmesh update --check   # Check for updates without installing
  tunnelmesh update --force   # Force update even if same version`,
		Args: cobra.MaximumNArgs(1),
		RunE: runUpdate,
	}

	updateCmd.Flags().BoolVar(&updateCheck, "check", false, "Only check for updates, don't install")
	updateCmd.Flags().BoolVarP(&updateForce, "force", "f", false, "Force update even if already at same version")
	updateCmd.Flags().BoolVar(&updateNoRestart, "no-restart", false, "Don't restart service after update")

	return updateCmd
}

func runUpdate(cmd *cobra.Command, args []string) error {
	setupLogging()

	// Parse current version
	currentVersion, err := update.ParseVersion(Version)
	if err != nil {
		log.Warn().Str("version", Version).Msg("could not parse current version")
		currentVersion = &update.Version{Raw: Version}
	}

	fmt.Printf("Current version: %s\n", Version)

	// Create updater
	updater := update.NewUpdater(update.Config{
		GitHubOwner: "zombar",
		GitHubRepo:  "tunnelmesh",
	})

	// Get release info
	var release *update.ReleaseInfo
	var targetVersion string

	if len(args) > 0 {
		targetVersion = args[0]
		fmt.Printf("Target version:  %s\n", targetVersion)
		release, err = updater.CheckVersion(targetVersion)
		if err != nil {
			return fmt.Errorf("failed to fetch release %s: %w", targetVersion, err)
		}
	} else {
		release, err = updater.CheckLatest()
		if err != nil {
			return fmt.Errorf("failed to fetch latest release: %w", err)
		}
		fmt.Printf("Latest version:  %s\n", release.TagName)
	}

	// Parse remote version
	remoteVersion, err := update.ParseVersion(release.TagName)
	if err != nil {
		return fmt.Errorf("failed to parse remote version %s: %w", release.TagName, err)
	}

	// Compare versions
	needsUpdate := currentVersion.NeedsUpdate(remoteVersion)
	isDowngrade := remoteVersion.NeedsUpdate(currentVersion)

	if !needsUpdate && !updateForce {
		if isDowngrade && !updateForce {
			fmt.Println("\nCurrent version is newer than target. Use --force to downgrade.")
			return nil
		}
		fmt.Println("\nAlready up to date.")
		return nil
	}

	if isDowngrade {
		fmt.Println("\nWarning: This is a downgrade.")
	}

	// Check-only mode
	if updateCheck {
		if needsUpdate {
			fmt.Println("\nUpdate available!")
			fmt.Printf("Run 'tunnelmesh update' to install %s\n", release.TagName)
		}
		return nil
	}

	// Find asset for current platform
	asset, err := release.FindAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return fmt.Errorf("no binary available for %s/%s: %w", runtime.GOOS, runtime.GOARCH, err)
	}

	fmt.Printf("\nDownloading %s...\n", asset.Name)

	// Download with progress
	downloadPath, err := updater.Download(asset, func(downloaded, total int64) {
		if total > 0 {
			percent := float64(downloaded) / float64(total) * 100
			fmt.Printf("\r  %.1f%% (%d / %d bytes)", percent, downloaded, total)
		}
	})
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer os.Remove(downloadPath)
	fmt.Println() // newline after progress

	// Verify checksum
	fmt.Print("Verifying checksum... ")
	if err := updater.VerifyDownload(downloadPath, asset.Name, release); err != nil {
		fmt.Println("FAILED")
		return fmt.Errorf("checksum verification failed: %w", err)
	}
	fmt.Println("OK")

	// Verify the new binary runs
	fmt.Print("Verifying binary... ")
	if err := verifyBinary(downloadPath); err != nil {
		fmt.Println("FAILED")
		return fmt.Errorf("binary verification failed: %w", err)
	}
	fmt.Println("OK")

	// Get current executable path
	currentPath, err := update.GetExecutablePath()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Check if running as service
	var serviceCfg *svc.ServiceConfig
	var serviceRunning bool

	// Try to detect running service
	for _, mode := range []string{"join", "serve"} {
		cfg := &svc.ServiceConfig{
			Name:       svc.DefaultServiceName(mode),
			Mode:       mode,
			ConfigPath: svc.DefaultConfigPath(mode),
		}
		status, err := svc.Status(cfg)
		if err == nil && status == 1 { // StatusRunning
			serviceCfg = cfg
			serviceRunning = true
			fmt.Printf("\nDetected service: %s (running)\n", cfg.Name)
			break
		}
	}

	// If service is running and user didn't use --no-restart, handle it
	if serviceRunning && !updateNoRestart {
		// Check for privileges
		if err := svc.CheckPrivileges(); err != nil {
			fmt.Println("\nService is running but you don't have privileges to restart it.")
			fmt.Println("Either run with sudo, or use --no-restart and restart manually.")
			return err
		}

		fmt.Print("Stopping service... ")
		if err := svc.Stop(serviceCfg); err != nil {
			fmt.Println("FAILED")
			log.Warn().Err(err).Msg("failed to stop service")
		} else {
			fmt.Println("OK")
		}
	}

	// Replace binary
	fmt.Print("Replacing binary... ")
	backupPath, err := update.ReplaceExecutableWithBackup(currentPath, downloadPath)
	if err != nil {
		fmt.Println("FAILED")
		return fmt.Errorf("failed to replace binary: %w", err)
	}
	fmt.Println("OK")

	// Verify replacement
	fmt.Print("Verifying update... ")
	if err := verifyBinary(currentPath); err != nil {
		fmt.Println("FAILED")
		fmt.Println("Rolling back...")
		if rbErr := update.RollbackReplace(currentPath, backupPath); rbErr != nil {
			return fmt.Errorf("update failed and rollback failed: update error: %w, rollback error: %v", err, rbErr)
		}
		fmt.Println("Rolled back to previous version")
		return fmt.Errorf("update verification failed: %w", err)
	}
	fmt.Println("OK")

	// Cleanup backup
	update.CleanupBackup(backupPath)

	// Restart service if needed
	if serviceRunning && !updateNoRestart {
		fmt.Print("Starting service... ")
		if err := svc.Start(serviceCfg); err != nil {
			fmt.Println("FAILED")
			return fmt.Errorf("failed to restart service: %w", err)
		}
		fmt.Println("OK")
	}

	fmt.Printf("\nUpdated successfully to %s\n", release.TagName)

	if !serviceRunning || updateNoRestart {
		fmt.Println("Please restart tunnelmesh to use the new version.")
	}

	return nil
}

// verifyBinary runs the binary with --version to verify it's valid
func verifyBinary(binaryPath string) error {
	// Make it executable first (for downloaded files)
	_ = os.Chmod(binaryPath, 0755)

	cmd := exec.Command(binaryPath, "version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("binary failed to run: %w (output: %s)", err, string(output))
	}

	// Check that output looks like a version
	outputStr := strings.TrimSpace(string(output))
	if !strings.Contains(outputStr, "tunnelmesh") {
		return fmt.Errorf("unexpected version output: %s", outputStr)
	}

	return nil
}
