//go:build windows

package update

import (
	"fmt"
	"os"
	"path/filepath"
)

// ReplaceExecutable replaces the current executable with a new one.
// On Windows, running executables are locked, so we use a rename approach:
// 1. Rename current.exe -> current.exe.old
// 2. Copy new file to current.exe
// 3. The .old file can be cleaned up on restart or later
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

	oldPath := currentPath + ".old"

	// Remove stale .old file if exists
	_ = os.Remove(oldPath)

	// Rename current to .old (this works on Windows even if running)
	if err := os.Rename(currentPath, oldPath); err != nil {
		return fmt.Errorf("rename current to old: %w", err)
	}

	// Copy new file to current location
	if err := copyFile(newPath, currentPath); err != nil {
		// Try to restore
		_ = os.Rename(oldPath, currentPath)
		return fmt.Errorf("copy new file: %w", err)
	}

	// Set permissions
	_ = os.Chmod(currentPath, currentInfo.Mode())

	// Remove source file
	_ = os.Remove(newPath)

	// Note: .old file will be cleaned up on next startup or manually
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
		_ = os.Remove(currentPath + ".old")
		_ = copyFile(backupPath, currentPath)
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

	// Get permissions from backup
	var mode os.FileMode = 0755
	if info, err := os.Stat(backupPath); err == nil {
		mode = info.Mode()
	}

	// Rename current to .old
	oldPath := currentPath + ".rollback-old"
	_ = os.Remove(oldPath)
	_ = os.Rename(currentPath, oldPath)

	// Copy backup to current
	if err := copyFile(backupPath, currentPath); err != nil {
		// Try to restore from .old
		_ = os.Rename(oldPath, currentPath)
		return fmt.Errorf("restore from backup: %w", err)
	}

	// Set permissions
	_ = os.Chmod(currentPath, mode)

	// Clean up
	_ = os.Remove(oldPath)
	_ = os.Remove(backupPath)

	return nil
}

// GetExecutablePath returns the path to the current running executable.
func GetExecutablePath() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("get executable: %w", err)
	}

	// Resolve symlinks
	realPath, err := filepath.EvalSymlinks(exePath)
	if err != nil {
		return exePath, nil
	}

	return realPath, nil
}

// CleanupOldBinary removes stale .old files from previous updates.
// Call this at startup.
func CleanupOldBinary() {
	exePath, err := GetExecutablePath()
	if err != nil {
		return
	}

	oldPath := exePath + ".old"
	_ = os.Remove(oldPath)
}
