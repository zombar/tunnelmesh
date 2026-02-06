package promsd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPeersToTargets(t *testing.T) {
	now := time.Now()
	threshold := 2 * time.Minute
	metricsPort := "9443"

	tests := []struct {
		name     string
		peers    []Peer
		expected int
	}{
		{
			name:     "empty peers",
			peers:    []Peer{},
			expected: 0,
		},
		{
			name: "single online peer",
			peers: []Peer{
				{Name: "peer1", MeshIP: "10.99.0.1", LastSeen: now.Add(-30 * time.Second)},
			},
			expected: 1,
		},
		{
			name: "single offline peer",
			peers: []Peer{
				{Name: "peer1", MeshIP: "10.99.0.1", LastSeen: now.Add(-5 * time.Minute)},
			},
			expected: 0,
		},
		{
			name: "peer without mesh IP",
			peers: []Peer{
				{Name: "peer1", MeshIP: "", LastSeen: now.Add(-30 * time.Second)},
			},
			expected: 0,
		},
		{
			name: "mixed peers",
			peers: []Peer{
				{Name: "online1", MeshIP: "10.99.0.1", LastSeen: now.Add(-30 * time.Second)},
				{Name: "offline1", MeshIP: "10.99.0.2", LastSeen: now.Add(-5 * time.Minute)},
				{Name: "online2", MeshIP: "10.99.0.3", LastSeen: now.Add(-1 * time.Minute)},
				{Name: "no-ip", MeshIP: "", LastSeen: now.Add(-30 * time.Second)},
			},
			expected: 2,
		},
		{
			name: "peer exactly at threshold",
			peers: []Peer{
				{Name: "peer1", MeshIP: "10.99.0.1", LastSeen: now.Add(-threshold)},
			},
			expected: 0, // >= threshold means offline
		},
		{
			name: "peer just before threshold",
			peers: []Peer{
				{Name: "peer1", MeshIP: "10.99.0.1", LastSeen: now.Add(-threshold + time.Second)},
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targets := PeersToTargets(tt.peers, metricsPort, threshold, now)
			if len(targets) != tt.expected {
				t.Errorf("expected %d targets, got %d", tt.expected, len(targets))
			}
		})
	}
}

func TestPeersToTargets_TargetFormat(t *testing.T) {
	now := time.Now()
	peers := []Peer{
		{Name: "test-peer", MeshIP: "10.99.0.42", LastSeen: now},
	}

	targets := PeersToTargets(peers, "9443", 2*time.Minute, now)

	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}

	target := targets[0]

	// Check targets format
	if len(target.Targets) != 1 {
		t.Errorf("expected 1 target address, got %d", len(target.Targets))
	}
	if target.Targets[0] != "10.99.0.42:9443" {
		t.Errorf("expected target address '10.99.0.42:9443', got '%s'", target.Targets[0])
	}

	// Check labels
	if target.Labels["peer"] != "test-peer" {
		t.Errorf("expected peer label 'test-peer', got '%s'", target.Labels["peer"])
	}
}

func TestWriteTargets(t *testing.T) {
	tmpDir := t.TempDir()
	outputFile := filepath.Join(tmpDir, "targets.json")

	targets := []Target{
		{
			Targets: []string{"10.99.0.1:9443"},
			Labels:  map[string]string{"peer": "peer1"},
		},
		{
			Targets: []string{"10.99.0.2:9443"},
			Labels:  map[string]string{"peer": "peer2"},
		},
	}

	err := WriteTargets(targets, outputFile)
	if err != nil {
		t.Fatalf("WriteTargets failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(outputFile); os.IsNotExist(err) {
		t.Fatal("output file was not created")
	}

	// Verify content
	data, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	var readTargets []Target
	if err := json.Unmarshal(data, &readTargets); err != nil {
		t.Fatalf("failed to parse output file: %v", err)
	}

	if len(readTargets) != 2 {
		t.Errorf("expected 2 targets in file, got %d", len(readTargets))
	}

	// Verify temp file was cleaned up
	tmpFile := outputFile + ".tmp"
	if _, err := os.Stat(tmpFile); !os.IsNotExist(err) {
		t.Error("temp file was not cleaned up")
	}
}

func TestWriteTargets_EmptyTargets(t *testing.T) {
	tmpDir := t.TempDir()
	outputFile := filepath.Join(tmpDir, "targets.json")

	err := WriteTargets([]Target{}, outputFile)
	if err != nil {
		t.Fatalf("WriteTargets failed: %v", err)
	}

	data, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	// Empty array should be valid JSON
	var readTargets []Target
	if err := json.Unmarshal(data, &readTargets); err != nil {
		t.Fatalf("failed to parse output file: %v", err)
	}

	if len(readTargets) != 0 {
		t.Errorf("expected 0 targets, got %d", len(readTargets))
	}
}

func TestWriteTargets_InvalidPath(t *testing.T) {
	err := WriteTargets([]Target{}, "/nonexistent/directory/targets.json")
	if err == nil {
		t.Error("expected error for invalid path, got nil")
	}
}

// mockFetcher implements PeerFetcher for testing.
type mockFetcher struct {
	peers []Peer
	err   error
}

func (m *mockFetcher) FetchPeers() ([]Peer, error) {
	return m.peers, m.err
}

func TestGenerator_Generate(t *testing.T) {
	tmpDir := t.TempDir()
	outputFile := filepath.Join(tmpDir, "targets.json")

	cfg := DefaultConfig()
	cfg.OutputFile = outputFile

	g := NewGenerator(cfg)

	now := time.Now()
	g.SetFetcher(&mockFetcher{
		peers: []Peer{
			{Name: "peer1", MeshIP: "10.99.0.1", LastSeen: now},
			{Name: "peer2", MeshIP: "10.99.0.2", LastSeen: now.Add(-5 * time.Minute)}, // offline
		},
	})

	count, err := g.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if count != 1 {
		t.Errorf("expected 1 target generated, got %d", count)
	}

	// Verify file was written
	if _, err := os.Stat(outputFile); os.IsNotExist(err) {
		t.Error("output file was not created")
	}
}

func TestGenerator_Generate_FetchError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OutputFile = filepath.Join(t.TempDir(), "targets.json")

	g := NewGenerator(cfg)
	g.SetFetcher(&mockFetcher{
		err: os.ErrNotExist,
	})

	_, err := g.Generate()
	if err == nil {
		t.Error("expected error from fetch failure, got nil")
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.CoordURL != "https://localhost:443" {
		t.Errorf("unexpected default CoordURL: %s", cfg.CoordURL)
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("unexpected default PollInterval: %v", cfg.PollInterval)
	}
	if cfg.MetricsPort != "9443" {
		t.Errorf("unexpected default MetricsPort: %s", cfg.MetricsPort)
	}
	if cfg.OnlineThreshold != 2*time.Minute {
		t.Errorf("unexpected default OnlineThreshold: %v", cfg.OnlineThreshold)
	}
}

func TestNewGenerator(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TLSSkipVerify = true

	g := NewGenerator(cfg)

	if g.config.TLSSkipVerify != true {
		t.Error("TLSSkipVerify not set correctly")
	}
	if g.client == nil {
		t.Error("HTTP client not initialized")
	}
	if g.fetcher == nil {
		t.Error("fetcher not initialized")
	}
}

func TestGenerator_Config(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CoordURL = "https://test.example.com"
	cfg.MetricsPort = "1234"

	g := NewGenerator(cfg)
	returnedCfg := g.Config()

	if returnedCfg.CoordURL != "https://test.example.com" {
		t.Errorf("expected CoordURL 'https://test.example.com', got '%s'", returnedCfg.CoordURL)
	}
	if returnedCfg.MetricsPort != "1234" {
		t.Errorf("expected MetricsPort '1234', got '%s'", returnedCfg.MetricsPort)
	}
}

func TestWriteTargets_NilTargets(t *testing.T) {
	tmpDir := t.TempDir()
	outputFile := filepath.Join(tmpDir, "targets.json")

	// nil slice should work like empty slice
	err := WriteTargets(nil, outputFile)
	if err != nil {
		t.Fatalf("WriteTargets with nil failed: %v", err)
	}

	data, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	// Should be valid JSON (null or empty array)
	if string(data) != "null" && string(data) != "[]" {
		var targets []Target
		if err := json.Unmarshal(data, &targets); err != nil {
			t.Errorf("output is not valid JSON: %s", string(data))
		}
	}
}

func TestGenerator_Generate_WriteError(t *testing.T) {
	cfg := DefaultConfig()
	// Invalid path that can't be written to
	cfg.OutputFile = "/nonexistent/dir/targets.json"

	g := NewGenerator(cfg)
	g.SetFetcher(&mockFetcher{
		peers: []Peer{
			{Name: "peer1", MeshIP: "10.99.0.1", LastSeen: time.Now()},
		},
	})

	_, err := g.Generate()
	if err == nil {
		t.Error("expected error from write failure, got nil")
	}
}

func TestHTTPFetcher_Success(t *testing.T) {
	now := time.Now()
	response := PeersResponse{
		Peers: []Peer{
			{Name: "peer1", MeshIP: "10.99.0.1", LastSeen: now},
			{Name: "peer2", MeshIP: "10.99.0.2", LastSeen: now},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/peers" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := DefaultConfig()
	cfg.CoordURL = server.URL
	cfg.OutputFile = filepath.Join(t.TempDir(), "targets.json")

	g := NewGenerator(cfg)
	count, err := g.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 targets, got %d", count)
	}
}

func TestHTTPFetcher_WithAuthToken(t *testing.T) {
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PeersResponse{Peers: []Peer{}})
	}))
	defer server.Close()

	cfg := DefaultConfig()
	cfg.CoordURL = server.URL
	cfg.AuthToken = "test-token-123"
	cfg.OutputFile = filepath.Join(t.TempDir(), "targets.json")

	g := NewGenerator(cfg)
	_, err := g.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if receivedAuth != "Bearer test-token-123" {
		t.Errorf("expected auth header 'Bearer test-token-123', got '%s'", receivedAuth)
	}
}

func TestHTTPFetcher_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal server error"))
	}))
	defer server.Close()

	cfg := DefaultConfig()
	cfg.CoordURL = server.URL
	cfg.OutputFile = filepath.Join(t.TempDir(), "targets.json")

	g := NewGenerator(cfg)
	_, err := g.Generate()
	if err == nil {
		t.Error("expected error from API failure, got nil")
	}
}

func TestHTTPFetcher_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not valid json"))
	}))
	defer server.Close()

	cfg := DefaultConfig()
	cfg.CoordURL = server.URL
	cfg.OutputFile = filepath.Join(t.TempDir(), "targets.json")

	g := NewGenerator(cfg)
	_, err := g.Generate()
	if err == nil {
		t.Error("expected error from invalid JSON, got nil")
	}
}

func TestHTTPFetcher_ConnectionError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CoordURL = "http://localhost:99999" // invalid port
	cfg.OutputFile = filepath.Join(t.TempDir(), "targets.json")

	g := NewGenerator(cfg)
	_, err := g.Generate()
	if err == nil {
		t.Error("expected error from connection failure, got nil")
	}
}
