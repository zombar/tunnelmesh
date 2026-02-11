package wireguard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Mock client response matching coord server format
type mockClientListResponse struct {
	Clients []mockClient `json:"clients"`
}

type mockClient struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	MeshIP    string `json:"mesh_ip"`
	DNSName   string `json:"dns_name"`
	Enabled   bool   `json:"enabled"`
}

func TestConcentratorConfigParsing(t *testing.T) {
	cfg := &ConcentratorConfig{
		ServerURL:    "https://coord.example.com",
		AuthToken:    "test-token",
		ListenPort:   51820,
		DataDir:      t.TempDir(),
		SyncInterval: 30 * time.Second,
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("valid config should not error: %v", err)
	}
}

func TestConcentratorConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ConcentratorConfig
		wantErr bool
	}{
		{
			name: "valid config",
			cfg: ConcentratorConfig{
				ServerURL:    "https://coord.example.com",
				AuthToken:    "token",
				ListenPort:   51820,
				DataDir:      "/tmp/wg",
				SyncInterval: 30 * time.Second,
			},
			wantErr: false,
		},
		{
			name: "missing server URL",
			cfg: ConcentratorConfig{
				AuthToken:    "token",
				ListenPort:   51820,
				DataDir:      "/tmp/wg",
				SyncInterval: 30 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "missing auth token",
			cfg: ConcentratorConfig{
				ServerURL:    "https://coord.example.com",
				ListenPort:   51820,
				DataDir:      "/tmp/wg",
				SyncInterval: 30 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "invalid listen port",
			cfg: ConcentratorConfig{
				ServerURL:    "https://coord.example.com",
				AuthToken:    "token",
				ListenPort:   0,
				DataDir:      "/tmp/wg",
				SyncInterval: 30 * time.Second,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClientSyncParsing(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/wireguard/clients" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Check auth header
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		resp := mockClientListResponse{
			Clients: []mockClient{
				{
					ID:        "id1",
					Name:      "iPhone",
					PublicKey: "xTIBA5rboUvnH4htodjb60Y7YAf21J7YQMlNGC8HQ14=",
					MeshIP:    "10.42.100.1",
					DNSName:   "iphone",
					Enabled:   true,
				},
				{
					ID:        "id2",
					Name:      "Android",
					PublicKey: "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykI=",
					MeshIP:    "10.42.100.2",
					DNSName:   "android",
					Enabled:   true,
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	clients, err := FetchClients(context.Background(), server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to fetch clients: %v", err)
	}

	if len(clients) != 2 {
		t.Errorf("expected 2 clients, got %d", len(clients))
	}

	if clients[0].Name != "iPhone" {
		t.Errorf("expected first client to be iPhone, got %s", clients[0].Name)
	}
	if clients[0].MeshIP != "10.42.100.1" {
		t.Errorf("expected mesh IP 10.42.100.1, got %s", clients[0].MeshIP)
	}
}

func TestClientSyncAuthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := FetchClients(context.Background(), server.URL, "wrong-token")
	if err == nil {
		t.Error("expected error for unauthorized request")
	}
}

func TestClientSyncTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := FetchClients(ctx, server.URL, "token")
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestClientToPeerConfig(t *testing.T) {
	client := &Client{
		ID:        "id1",
		Name:      "iPhone",
		PublicKey: "xTIBA5rboUvnH4htodjb60Y7YAf21J7YQMlNGC8HQ14=",
		MeshIP:    "10.42.100.1",
		DNSName:   "iphone",
		Enabled:   true,
	}

	peerCfg := client.ToPeerConfig()

	if peerCfg.PublicKey != client.PublicKey {
		t.Errorf("public key mismatch")
	}
	if len(peerCfg.AllowedIPs) != 1 {
		t.Errorf("expected 1 allowed IP, got %d", len(peerCfg.AllowedIPs))
	}
	if peerCfg.AllowedIPs[0] != "10.42.100.1/32" {
		t.Errorf("expected allowed IP 10.42.100.1/32, got %s", peerCfg.AllowedIPs[0])
	}
}

func TestClientsDiff(t *testing.T) {
	old := []Client{
		{ID: "1", PublicKey: "key1", MeshIP: "10.42.100.1"},
		{ID: "2", PublicKey: "key2", MeshIP: "10.42.100.2"},
	}
	// nolint:revive // new variable name is clear in context of old/new comparison
	new := []Client{
		{ID: "2", PublicKey: "key2", MeshIP: "10.42.100.2"},
		{ID: "3", PublicKey: "key3", MeshIP: "10.42.100.3"},
	}

	added, removed := DiffClients(old, new)

	if len(added) != 1 {
		t.Errorf("expected 1 added client, got %d", len(added))
	}
	if added[0].ID != "3" {
		t.Errorf("expected added client ID 3, got %s", added[0].ID)
	}

	if len(removed) != 1 {
		t.Errorf("expected 1 removed client, got %d", len(removed))
	}
	if removed[0].ID != "1" {
		t.Errorf("expected removed client ID 1, got %s", removed[0].ID)
	}
}

func TestConcentratorSetOnPacketFromWG(t *testing.T) {
	cfg := &ConcentratorConfig{
		ServerURL:    "https://coord.example.com",
		AuthToken:    "test-token",
		ListenPort:   51820,
		DataDir:      t.TempDir(),
		SyncInterval: 30 * time.Second,
	}

	conc, err := NewConcentrator(cfg)
	if err != nil {
		t.Fatalf("failed to create concentrator: %v", err)
	}

	// nolint:revive // packet required by callback signature but not used in test
	var callbackCalled bool
	conc.SetOnPacketFromWG(func(packet []byte) {
		callbackCalled = true
	})

	// Verify callback is set by accessing the field
	if conc.onPacketFromWG == nil {
		t.Error("onPacketFromWG callback should be set")
	}

	// Suppress unused variable warning
	_ = callbackCalled
}

func TestConcentratorIsDeviceRunning(t *testing.T) {
	cfg := &ConcentratorConfig{
		ServerURL:    "https://coord.example.com",
		AuthToken:    "test-token",
		ListenPort:   51820,
		DataDir:      t.TempDir(),
		SyncInterval: 30 * time.Second,
	}

	conc, err := NewConcentrator(cfg)
	if err != nil {
		t.Fatalf("failed to create concentrator: %v", err)
	}

	// Device should not be running initially
	if conc.IsDeviceRunning() {
		t.Error("device should not be running initially")
	}
}

func TestConcentratorSendPacketWithoutDevice(t *testing.T) {
	cfg := &ConcentratorConfig{
		ServerURL:    "https://coord.example.com",
		AuthToken:    "test-token",
		ListenPort:   51820,
		DataDir:      t.TempDir(),
		SyncInterval: 30 * time.Second,
	}

	conc, err := NewConcentrator(cfg)
	if err != nil {
		t.Fatalf("failed to create concentrator: %v", err)
	}

	// SendPacket should fail when device is not running
	err = conc.SendPacket([]byte{0x45, 0x00, 0x00, 0x28})
	if err == nil {
		t.Error("expected error when sending packet without device")
	}
}

func TestConcentratorUpdateClientsWithoutDevice(t *testing.T) {
	cfg := &ConcentratorConfig{
		ServerURL:    "https://coord.example.com",
		AuthToken:    "test-token",
		ListenPort:   51820,
		DataDir:      t.TempDir(),
		SyncInterval: 30 * time.Second,
	}

	conc, err := NewConcentrator(cfg)
	if err != nil {
		t.Fatalf("failed to create concentrator: %v", err)
	}

	// Track if callback was called
	var callbackClients []Client
	conc.SetOnClientsUpdated(func(clients []Client) {
		callbackClients = clients
	})

	clients := []Client{
		{ID: "1", Name: "iPhone", PublicKey: "key1", MeshIP: "10.42.100.1", Enabled: true},
		{ID: "2", Name: "Android", PublicKey: "key2", MeshIP: "10.42.100.2", Enabled: true},
	}

	// UpdateClients should work even without device (just updates internal list)
	err = conc.UpdateClients(clients)
	if err != nil {
		t.Errorf("UpdateClients failed: %v", err)
	}

	// Verify clients were stored
	if len(conc.Clients()) != 2 {
		t.Errorf("expected 2 clients, got %d", len(conc.Clients()))
	}

	// Verify callback was called
	if len(callbackClients) != 2 {
		t.Errorf("callback should have received 2 clients, got %d", len(callbackClients))
	}
}

func TestConcentratorStopDeviceWhenNotRunning(t *testing.T) {
	cfg := &ConcentratorConfig{
		ServerURL:    "https://coord.example.com",
		AuthToken:    "test-token",
		ListenPort:   51820,
		DataDir:      t.TempDir(),
		SyncInterval: 30 * time.Second,
	}

	conc, err := NewConcentrator(cfg)
	if err != nil {
		t.Fatalf("failed to create concentrator: %v", err)
	}

	// StopDevice should not error when device is not running
	err = conc.StopDevice()
	if err != nil {
		t.Errorf("StopDevice should not error when device not running: %v", err)
	}
}
