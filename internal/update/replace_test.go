package update

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReplaceExecutable(t *testing.T) {
	// Create a temp directory for our test
	tmpDir := t.TempDir()

	// Create "current" binary
	currentPath := filepath.Join(tmpDir, "current-binary")
	if runtime.GOOS == "windows" {
		currentPath += ".exe"
	}
	currentContent := []byte("original binary content")
	if err := os.WriteFile(currentPath, currentContent, 0755); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	// Create "new" binary
	newPath := filepath.Join(tmpDir, "new-binary")
	if runtime.GOOS == "windows" {
		newPath += ".exe"
	}
	newContent := []byte("new binary content - updated version")
	if err := os.WriteFile(newPath, newContent, 0755); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	// Replace the binary
	if err := ReplaceExecutable(currentPath, newPath); err != nil {
		t.Fatalf("ReplaceExecutable() error: %v", err)
	}

	// Verify the current binary now has new content
	content, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}

	if string(content) != string(newContent) {
		t.Errorf("content = %q, want %q", content, newContent)
	}

	// Verify the new binary was removed (or can be reused)
	// On some platforms, the new file might still exist after a move vs copy
	// This is implementation-dependent, so we just verify the replacement worked
}

func TestReplaceExecutableWithBackup(t *testing.T) {
	tmpDir := t.TempDir()

	// Create "current" binary
	currentPath := filepath.Join(tmpDir, "current-binary")
	if runtime.GOOS == "windows" {
		currentPath += ".exe"
	}
	currentContent := []byte("original content")
	if err := os.WriteFile(currentPath, currentContent, 0755); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	// Create "new" binary
	newPath := filepath.Join(tmpDir, "new-binary")
	if runtime.GOOS == "windows" {
		newPath += ".exe"
	}
	newContent := []byte("new content")
	if err := os.WriteFile(newPath, newContent, 0755); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	// Replace with backup
	backupPath, err := ReplaceExecutableWithBackup(currentPath, newPath)
	if err != nil {
		t.Fatalf("ReplaceExecutableWithBackup() error: %v", err)
	}

	// Verify current has new content
	content, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if string(content) != string(newContent) {
		t.Errorf("content = %q, want %q", content, newContent)
	}

	// Verify backup exists with original content
	if backupPath == "" {
		t.Fatal("backupPath is empty")
	}
	backupContent, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("ReadFile(backup) error: %v", err)
	}
	if string(backupContent) != string(currentContent) {
		t.Errorf("backup content = %q, want %q", backupContent, currentContent)
	}
}

func TestReplaceExecutablePreservesPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Permission test not applicable on Windows")
	}

	tmpDir := t.TempDir()

	// Create "current" binary with specific permissions
	currentPath := filepath.Join(tmpDir, "current-binary")
	currentContent := []byte("original")
	if err := os.WriteFile(currentPath, currentContent, 0755); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	// Create "new" binary with different permissions
	newPath := filepath.Join(tmpDir, "new-binary")
	newContent := []byte("new")
	if err := os.WriteFile(newPath, newContent, 0644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	// Replace
	if err := ReplaceExecutable(currentPath, newPath); err != nil {
		t.Fatalf("ReplaceExecutable() error: %v", err)
	}

	// Check that current is still executable
	info, err := os.Stat(currentPath)
	if err != nil {
		t.Fatalf("Stat() error: %v", err)
	}

	// Should preserve original permissions (executable)
	mode := info.Mode()
	if mode&0100 == 0 { // Owner execute bit
		t.Errorf("mode = %o, expected executable (owner execute bit set)", mode)
	}
}

func TestReplaceExecutableNonExistentNew(t *testing.T) {
	tmpDir := t.TempDir()

	currentPath := filepath.Join(tmpDir, "current-binary")
	if err := os.WriteFile(currentPath, []byte("content"), 0755); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	newPath := filepath.Join(tmpDir, "non-existent")

	err := ReplaceExecutable(currentPath, newPath)
	if err == nil {
		t.Error("ReplaceExecutable() expected error for non-existent new file")
	}
}

func TestRollbackReplace(t *testing.T) {
	tmpDir := t.TempDir()

	// Create "current" binary
	currentPath := filepath.Join(tmpDir, "current-binary")
	if runtime.GOOS == "windows" {
		currentPath += ".exe"
	}
	currentContent := []byte("original content")
	if err := os.WriteFile(currentPath, currentContent, 0755); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	// Create "new" binary
	newPath := filepath.Join(tmpDir, "new-binary")
	if runtime.GOOS == "windows" {
		newPath += ".exe"
	}
	newContent := []byte("new content")
	if err := os.WriteFile(newPath, newContent, 0755); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	// Replace with backup
	backupPath, err := ReplaceExecutableWithBackup(currentPath, newPath)
	if err != nil {
		t.Fatalf("ReplaceExecutableWithBackup() error: %v", err)
	}

	// Verify replacement happened
	content, _ := os.ReadFile(currentPath)
	if string(content) != string(newContent) {
		t.Fatal("Replacement didn't work")
	}

	// Now rollback
	if err := RollbackReplace(currentPath, backupPath); err != nil {
		t.Fatalf("RollbackReplace() error: %v", err)
	}

	// Verify original content is restored
	content, err = os.ReadFile(currentPath)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if string(content) != string(currentContent) {
		t.Errorf("after rollback content = %q, want %q", content, currentContent)
	}
}

func TestCleanupBackup(t *testing.T) {
	tmpDir := t.TempDir()

	backupPath := filepath.Join(tmpDir, "backup-file")
	if err := os.WriteFile(backupPath, []byte("backup"), 0644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	CleanupBackup(backupPath)

	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Error("backup file should be deleted")
	}
}

func TestCleanupBackupNonExistent(t *testing.T) {
	// Should not panic or error on non-existent file
	CleanupBackup("/non/existent/path")
}
