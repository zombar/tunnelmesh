package nfs

import (
	"context"
	"testing"

	nfs "github.com/willscott/go-nfs"
)

func TestPasswordStore_SetAndVerify(t *testing.T) {
	ps := NewPasswordStore()

	// Set password
	err := ps.SetPassword("alice", "secret123")
	if err != nil {
		t.Fatalf("SetPassword failed: %v", err)
	}

	// Verify correct password
	if !ps.VerifyPassword("alice", "secret123") {
		t.Error("VerifyPassword should return true for correct password")
	}

	// Verify incorrect password
	if ps.VerifyPassword("alice", "wrong") {
		t.Error("VerifyPassword should return false for incorrect password")
	}

	// Verify unknown user
	if ps.VerifyPassword("bob", "secret123") {
		t.Error("VerifyPassword should return false for unknown user")
	}
}

func TestPasswordStore_HasPassword(t *testing.T) {
	ps := NewPasswordStore()

	// No password set
	if ps.HasPassword("alice") {
		t.Error("HasPassword should return false for user without password")
	}

	// Set password
	_ = ps.SetPassword("alice", "secret")

	// Password set
	if !ps.HasPassword("alice") {
		t.Error("HasPassword should return true for user with password")
	}
}

func TestPasswordStore_UpdatePassword(t *testing.T) {
	ps := NewPasswordStore()

	// Set initial password
	_ = ps.SetPassword("alice", "old123")
	if !ps.VerifyPassword("alice", "old123") {
		t.Error("Initial password should work")
	}

	// Update password
	_ = ps.SetPassword("alice", "new456")
	if ps.VerifyPassword("alice", "old123") {
		t.Error("Old password should not work after update")
	}
	if !ps.VerifyPassword("alice", "new456") {
		t.Error("New password should work after update")
	}
}

func TestNewHandler(t *testing.T) {
	// Create a handler
	h := NewHandler(nil, nil, nil, nil)
	if h == nil {
		t.Fatal("NewHandler returned nil")
	}

	if h.cachingLimit != 1024 {
		t.Errorf("cachingLimit = %d, want 1024", h.cachingLimit)
	}

	if h.connUsers == nil {
		t.Error("connUsers should be initialized")
	}
}

func TestHandler_HandleLimit(t *testing.T) {
	h := NewHandler(nil, nil, nil, nil)
	if h.HandleLimit() != 1024 {
		t.Errorf("HandleLimit = %d, want 1024", h.HandleLimit())
	}
}

func TestHandler_Change(t *testing.T) {
	h := NewHandler(nil, nil, nil, nil)
	// Change should return nil (read-only changes not supported)
	if h.Change(nil) != nil {
		t.Error("Change should return nil")
	}
}

func TestHandler_FSStat(t *testing.T) {
	h := NewHandler(nil, nil, nil, nil)

	var stat nfs.FSStat
	err := h.FSStat(context.Background(), nil, &stat)
	if err != nil {
		t.Fatalf("FSStat failed: %v", err)
	}

	if stat.TotalSize != 1<<40 {
		t.Errorf("TotalSize = %d, want %d", stat.TotalSize, 1<<40)
	}
	if stat.FreeSize != 1<<40 {
		t.Errorf("FreeSize = %d, want %d", stat.FreeSize, 1<<40)
	}
	if stat.AvailableSize != 1<<40 {
		t.Errorf("AvailableSize = %d, want %d", stat.AvailableSize, 1<<40)
	}
}
