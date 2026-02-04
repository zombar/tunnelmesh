//go:build !windows

package update

import (
	"fmt"
	"os"
	"path/filepath"
)

// ReplaceExecutable replaces the current executable with a new one.
// On Unix systems, this works because file descriptors remain valid
// even if the file is unlinked/replaced while running.
func ReplaceExecutable(currentPath, newPath string) error {
	// Verify new file exists
	newInfo, err := os.Stat(newPath)
	if err != nil {
		return fmt.Errorf("stat new file: %w", err)
	}
	if newInfo.IsDir() {
		return fmt.Errorf("new path is a directory")
	}

	// Get current file info for permissions
	currentInfo, err := os.Stat(currentPath)
	if err != nil {
		return fmt.Errorf("stat current file: %w", err)
	}

	// Try atomic rename first (only works on same filesystem)
	err = os.Rename(newPath, currentPath)
	if err == nil {
		// Preserve original permissions
		_ = os.Chmod(currentPath, currentInfo.Mode())
		return nil
	}

	// If rename failed (cross-filesystem), fall back to copy
	if err := copyFile(newPath, currentPath); err != nil {
		return fmt.Errorf("copy file: %w", err)
	}

	// Set executable permissions
	if err := os.Chmod(currentPath, currentInfo.Mode()); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	// Remove the source file
	_ = os.Remove(newPath)

	return nil
}

// ReplaceExecutableWithBackup replaces the current executable with a new one,
// creating a backup of the original. Returns the backup path.
func ReplaceExecutableWithBackup(currentPath, newPath string) (backupPath string, err error) {
	// Verify new file exists
	if _, err := os.Stat(newPath); err != nil {
		return "", fmt.Errorf("stat new file: %w", err)
	}

	// Create backup path
	backupPath = currentPath + ".backup"

	// Remove old backup if exists
	_ = os.Remove(backupPath)

	// Copy current to backup
	if err := copyFile(currentPath, backupPath); err != nil {
		return "", fmt.Errorf("create backup: %w", err)
	}

	// Replace current with new
	if err := ReplaceExecutable(currentPath, newPath); err != nil {
		// Try to restore backup
		_ = os.Remove(currentPath)
		_ = os.Rename(backupPath, currentPath)
		return "", fmt.Errorf("replace: %w", err)
	}

	return backupPath, nil
}

// RollbackReplace restores the original executable from a backup.
func RollbackReplace(currentPath, backupPath string) error {
	if backupPath == "" {
		return fmt.Errorf("no backup path provided")
	}

	// Verify backup exists
	if _, err := os.Stat(backupPath); err != nil {
		return fmt.Errorf("backup not found: %w", err)
	}

	// Get permissions from current (or backup if current is gone)
	var mode os.FileMode = 0755
	if info, err := os.Stat(currentPath); err == nil {
		mode = info.Mode()
	} else if info, err := os.Stat(backupPath); err == nil {
		mode = info.Mode()
	}

	// Remove current
	_ = os.Remove(currentPath)

	// Rename backup to current
	if err := os.Rename(backupPath, currentPath); err != nil {
		// Fall back to copy
		if err := copyFile(backupPath, currentPath); err != nil {
			return fmt.Errorf("restore from backup: %w", err)
		}
	}

	// Ensure executable
	_ = os.Chmod(currentPath, mode)

	return nil
}

// GetExecutablePath returns the path to the current running executable.
// It resolves symlinks to get the actual binary path.
func GetExecutablePath() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("get executable: %w", err)
	}

	// Resolve symlinks
	realPath, err := filepath.EvalSymlinks(exePath)
	if err != nil {
		// If we can't resolve symlinks, use the original path
		return exePath, nil
	}

	return realPath, nil
}
