package nfs

import (
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
)

func newTestServer(t *testing.T) (*s3.Store, *s3.FileShareManager, *auth.Authorizer, *PasswordStore) {
	t.Helper()
	masterKey := [32]byte{1, 2, 3, 4, 5, 6, 7, 8}
	store, err := s3.NewStoreWithCAS(t.TempDir(), nil, masterKey)
	if err != nil {
		t.Fatalf("NewStoreWithCAS failed: %v", err)
	}
	systemStore, err := s3.NewSystemStore(store, "svc:coordinator")
	if err != nil {
		t.Fatalf("NewSystemStore failed: %v", err)
	}
	authorizer := auth.NewAuthorizer()
	shares := s3.NewFileShareManager(store, systemStore, authorizer)
	passwords := NewPasswordStore()
	return store, shares, authorizer, passwords
}

func TestNewServer(t *testing.T) {
	store, shares, authorizer, passwords := newTestServer(t)

	cfg := Config{
		Address: ":2049",
	}

	srv := NewServer(store, shares, authorizer, passwords, cfg)
	if srv == nil {
		t.Fatal("NewServer returned nil")
	}

	if srv.addr != ":2049" {
		t.Errorf("addr = %q, want %q", srv.addr, ":2049")
	}

	if srv.handler == nil {
		t.Error("handler should not be nil")
	}

	if srv.tlsConfig != nil {
		t.Error("tlsConfig should be nil when not provided")
	}
}

func TestServer_Handler(t *testing.T) {
	store, shares, authorizer, passwords := newTestServer(t)

	cfg := Config{
		Address: ":2049",
	}

	srv := NewServer(store, shares, authorizer, passwords, cfg)
	h := srv.Handler()
	if h == nil {
		t.Error("Handler() returned nil")
	}
}

func TestServer_StopWithoutStart(t *testing.T) {
	store, shares, authorizer, passwords := newTestServer(t)

	cfg := Config{
		Address: ":0", // Random port
	}

	srv := NewServer(store, shares, authorizer, passwords, cfg)

	// Stop without starting should be safe
	err := srv.Stop()
	if err != nil {
		t.Errorf("Stop without Start returned error: %v", err)
	}
}

func TestConfig(t *testing.T) {
	cfg := Config{
		Address: "10.100.0.1:2049",
	}

	if cfg.Address != "10.100.0.1:2049" {
		t.Errorf("Address = %q, want %q", cfg.Address, "10.100.0.1:2049")
	}
}
