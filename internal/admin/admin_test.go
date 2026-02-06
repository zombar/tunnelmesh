package admin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/metrics"
	"github.com/tunnelmesh/tunnelmesh/internal/tracing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

func TestNewAdminServer(t *testing.T) {
	server := NewAdminServer()
	if server == nil {
		t.Fatal("NewAdminServer returned nil")
	}
	if server.mux == nil {
		t.Error("mux is nil")
	}
}

func TestAdminServer_HealthEndpoint(t *testing.T) {
	server := NewAdminServer()

	// Find a free port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	// Start without TLS for testing
	if err := server.StartInsecure(addr); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	// Give server time to start
	time.Sleep(50 * time.Millisecond)

	// Test health endpoint
	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("Failed to get /health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ok") {
		t.Errorf("Expected body to contain 'ok', got: %s", body)
	}
}

func TestAdminServer_MetricsEndpoint(t *testing.T) {
	// Reset metrics registry for test
	oldRegistry := metrics.Registry
	metrics.Registry = prometheus.NewRegistry()
	defer func() { metrics.Registry = oldRegistry }()

	metrics.Registry.MustRegister(collectors.NewGoCollector())
	m := metrics.InitMetrics("test-peer", "10.99.0.1", "1.0.0")
	m.ActiveTunnels.Set(3)

	server := NewAdminServer()

	// Find a free port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	if err := server.StartInsecure(addr); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("Failed to get /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Verify metrics are present
	if !strings.Contains(bodyStr, "tunnelmesh_active_tunnels") {
		t.Error("Expected tunnelmesh_active_tunnels metric")
	}
	if !strings.Contains(bodyStr, "tunnelmesh_peer_info") {
		t.Error("Expected tunnelmesh_peer_info metric")
	}
}

func TestAdminServer_StartStop(t *testing.T) {
	server := NewAdminServer()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	if err := server.StartInsecure(addr); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Server should be running
	_, err = http.Get("http://" + addr + "/health")
	if err != nil {
		t.Error("Server should be reachable")
	}

	// Stop the server
	if err := server.Stop(); err != nil {
		t.Errorf("Failed to stop server: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Server should no longer be reachable
	client := &http.Client{Timeout: 100 * time.Millisecond}
	_, err = client.Get("http://" + addr + "/health")
	if err == nil {
		t.Error("Server should not be reachable after stop")
	}
}

func TestAdminServer_StartTLS(t *testing.T) {
	// Generate a self-signed certificate for testing
	cert, err := generateTestCert()
	if err != nil {
		t.Fatalf("Failed to generate test cert: %v", err)
	}

	server := NewAdminServer()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	if err := server.Start(addr, cert); err != nil {
		t.Fatalf("Failed to start TLS server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	time.Sleep(50 * time.Millisecond)

	// Create client that skips TLS verification for test
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	resp, err := client.Get("https://" + addr + "/health")
	if err != nil {
		t.Fatalf("Failed to get /health over TLS: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
}

func TestAdminServer_NotFoundHandler(t *testing.T) {
	server := NewAdminServer()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	if err := server.StartInsecure(addr); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get("http://" + addr + "/nonexistent")
	if err != nil {
		t.Fatalf("Failed to get /nonexistent: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", resp.StatusCode)
	}
}

// generateTestCert creates a self-signed certificate for testing
func generateTestCert() (*tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Test"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	privBytes, _ := x509.MarshalECPrivateKey(priv)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	return &cert, nil
}

func TestAdminServer_TraceEndpoint_Disabled(t *testing.T) {
	// Ensure tracing is disabled
	tracing.Stop()

	server := NewAdminServer()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	if err := server.StartInsecure(addr); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get("http://" + addr + "/debug/trace")
	if err != nil {
		t.Fatalf("Failed to get /debug/trace: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Should return 503 when tracing is disabled
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "tracing not enabled") {
		t.Errorf("Expected error message about tracing, got: %s", body)
	}
}

func TestAdminServer_TraceEndpoint_Enabled(t *testing.T) {
	// Enable tracing
	tracing.Stop() // Clean state
	if err := tracing.Init(true, tracing.DefaultBufferSize); err != nil {
		t.Fatalf("Failed to init tracing: %v", err)
	}
	defer tracing.Stop()

	server := NewAdminServer()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	if err := server.StartInsecure(addr); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get("http://" + addr + "/debug/trace")
	if err != nil {
		t.Fatalf("Failed to get /debug/trace: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// Check content type
	contentType := resp.Header.Get("Content-Type")
	if contentType != "application/octet-stream" {
		t.Errorf("Expected Content-Type application/octet-stream, got %s", contentType)
	}

	// Check content disposition
	contentDisp := resp.Header.Get("Content-Disposition")
	if !strings.Contains(contentDisp, "trace.out") {
		t.Errorf("Expected Content-Disposition with trace.out, got %s", contentDisp)
	}

	// Should have written something
	body, _ := io.ReadAll(resp.Body)
	if len(body) == 0 {
		t.Error("Expected non-empty trace data")
	}
}
