package wireguard

import (
	"testing"
	"time"
)

func TestStoreCreate(t *testing.T) {
	store := NewStore("172.30.0.0/16")

	client, err := store.Create("iPhone")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	if client.ID == "" {
		t.Error("client ID should not be empty")
	}
	if client.Name != "iPhone" {
		t.Errorf("name mismatch: got %q, want %q", client.Name, "iPhone")
	}
	if client.PublicKey == "" {
		t.Error("public key should not be empty")
	}
	if client.MeshIP == "" {
		t.Error("mesh IP should not be empty")
	}
	if client.DNSName == "" {
		t.Error("DNS name should not be empty")
	}
	if !client.Enabled {
		t.Error("new client should be enabled by default")
	}
	if client.CreatedAt.IsZero() {
		t.Error("created_at should be set")
	}
}

func TestStoreCreatePrivateKey(t *testing.T) {
	store := NewStore("172.30.0.0/16")

	client, privateKey, err := store.CreateWithPrivateKey("iPhone")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	if privateKey == "" {
		t.Error("private key should be returned")
	}
	if client.PublicKey == "" {
		t.Error("public key should not be empty")
	}
	// Private key should NOT be stored in the client
	// (we only return it once at creation)
}

func TestStoreGet(t *testing.T) {
	store := NewStore("172.30.0.0/16")

	client, _ := store.Create("iPhone")

	// Get by ID
	retrieved, err := store.Get(client.ID)
	if err != nil {
		t.Fatalf("failed to get client: %v", err)
	}
	if retrieved.Name != "iPhone" {
		t.Errorf("name mismatch: got %q, want %q", retrieved.Name, "iPhone")
	}

	// Get non-existent
	_, err = store.Get("non-existent-id")
	if err == nil {
		t.Error("expected error for non-existent client")
	}
}

func TestStoreList(t *testing.T) {
	store := NewStore("172.30.0.0/16")

	// Empty list
	clients := store.List()
	if len(clients) != 0 {
		t.Errorf("expected empty list, got %d clients", len(clients))
	}

	// Create some clients
	_, _ = store.Create("iPhone")
	_, _ = store.Create("Android")
	_, _ = store.Create("Laptop")

	clients = store.List()
	if len(clients) != 3 {
		t.Errorf("expected 3 clients, got %d", len(clients))
	}
}

func TestStoreUpdate(t *testing.T) {
	store := NewStore("172.30.0.0/16")

	client, _ := store.Create("iPhone")
	if !client.Enabled {
		t.Fatal("client should be enabled initially")
	}

	// Disable
	enabled := false
	updated, err := store.Update(client.ID, &UpdateClientRequest{Enabled: &enabled})
	if err != nil {
		t.Fatalf("failed to update client: %v", err)
	}
	if updated.Enabled {
		t.Error("client should be disabled after update")
	}

	// Verify persistence
	retrieved, _ := store.Get(client.ID)
	if retrieved.Enabled {
		t.Error("disabled state should persist")
	}

	// Update non-existent
	_, err = store.Update("non-existent-id", &UpdateClientRequest{Enabled: &enabled})
	if err == nil {
		t.Error("expected error for non-existent client")
	}
}

func TestStoreDelete(t *testing.T) {
	store := NewStore("172.30.0.0/16")

	client, _ := store.Create("iPhone")

	// Delete
	err := store.Delete(client.ID)
	if err != nil {
		t.Fatalf("failed to delete client: %v", err)
	}

	// Verify deleted
	_, err = store.Get(client.ID)
	if err == nil {
		t.Error("expected error after deletion")
	}

	// Delete non-existent
	err = store.Delete("non-existent-id")
	if err == nil {
		t.Error("expected error for non-existent client")
	}
}

func TestStoreIPAllocation(t *testing.T) {
	store := NewStore("172.30.0.0/16")

	ips := make(map[string]bool)
	for i := 0; i < 10; i++ {
		client, _ := store.Create("device")
		if ips[client.MeshIP] {
			t.Errorf("duplicate IP allocated: %s", client.MeshIP)
		}
		ips[client.MeshIP] = true
	}

	// All IPs should be in the WG client range (172.30.100.x - 172.30.199.x)
	for ip := range ips {
		if !isInWGClientRange(ip) {
			t.Errorf("IP %s is not in WG client range (172.30.100.0 - 172.30.199.255)", ip)
		}
	}
}

func TestStoreDNSNameGeneration(t *testing.T) {
	store := NewStore("172.30.0.0/16")

	tests := []struct {
		name     string
		expected string
	}{
		{"iPhone", "iphone"},
		{"My Android Phone", "my-android-phone"},
		{"LAPTOP-ABC123", "laptop-abc123"},
		{"Device #1", "device-1"},
		{"  Spaces  ", "spaces"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := store.Create(tt.name)
			if client.DNSName != tt.expected {
				t.Errorf("DNS name for %q: got %q, want %q", tt.name, client.DNSName, tt.expected)
			}
		})
	}
}

func TestStoreUpdateLastSeen(t *testing.T) {
	store := NewStore("172.30.0.0/16")

	client, _ := store.Create("iPhone")
	initialLastSeen := client.LastSeen

	// Wait a bit
	time.Sleep(10 * time.Millisecond)

	// Update last seen
	err := store.UpdateLastSeen(client.ID, time.Now())
	if err != nil {
		t.Fatalf("failed to update last seen: %v", err)
	}

	// Verify
	retrieved, _ := store.Get(client.ID)
	if !retrieved.LastSeen.After(initialLastSeen) {
		t.Error("last seen should have been updated")
	}
}

func TestStoreConcurrency(t *testing.T) {
	store := NewStore("172.30.0.0/16")

	// Create clients concurrently
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			_, _ = store.Create("device")
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Should have 10 unique clients
	clients := store.List()
	if len(clients) != 10 {
		t.Errorf("expected 10 clients, got %d", len(clients))
	}
}

// isInWGClientRange checks if an IP is in the WireGuard client range
func isInWGClientRange(ip string) bool {
	// WG clients use 172.30.100.0 - 172.30.199.255
	var a, b, c, d int
	n, _ := parseIP(ip, &a, &b, &c, &d)
	if n != 4 {
		return false
	}
	return a == 172 && b == 30 && c >= 100 && c <= 199
}

func parseIP(ip string, a, b, c, d *int) (int, error) {
	n, err := sscanf(ip, "%d.%d.%d.%d", a, b, c, d)
	return n, err
}

func sscanf(s, format string, args ...interface{}) (int, error) {
	// Simple sscanf implementation for IP parsing
	var n int
	_, err := __import_fmt().Sscanf(s, format, args...)
	if err == nil {
		n = len(args)
	}
	return n, err
}

// nolint:revive // Underscore names used to prevent unused import errors in generated test
func __import_fmt() interface {
	Sscanf(string, string, ...interface{}) (int, error)
} {
	return __fmtImport{}
}
// nolint:revive // Underscore names used to prevent unused import errors in generated test

type __fmtImport struct{}

func (__fmtImport) Sscanf(s, format string, a ...interface{}) (int, error) {
	return scanfImpl(s, format, a...)
// nolint:revive // format required by type interface but not used
}

func scanfImpl(s, format string, a ...interface{}) (int, error) {
	// Minimal implementation for tests
	parts := splitDots(s)
	if len(parts) != 4 || len(a) != 4 {
		return 0, nil
	}
	for i, p := range parts {
		var v int
		for _, c := range p {
			if c < '0' || c > '9' {
				return i, nil
			}
			v = v*10 + int(c-'0')
		}
		*a[i].(*int) = v
	}
	return 4, nil
}

func splitDots(s string) []string {
	var result []string
	var current string
	for _, c := range s {
		if c == '.' {
			result = append(result, current)
			current = ""
		} else {
			current += string(c)
		}
	}
	result = append(result, current)
	return result
}
