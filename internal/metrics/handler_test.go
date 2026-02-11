package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

func TestHandler(t *testing.T) {
	oldRegistry := Registry
	Registry = prometheus.NewRegistry()
	defer func() { Registry = oldRegistry }()

	Registry.MustRegister(collectors.NewGoCollector())
	Registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	// Initialize metrics
	m := InitMetrics("test-peer", "10.42.0.1", "1.0.0")

	// Set some values
	m.PacketsSent.Add(100)
	m.ActiveTunnels.Set(5)

	// Create handler
	handler := Handler()

	// Create test request
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()

	// Serve request
	handler.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// Check content type
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/plain") && !strings.Contains(contentType, "application/openmetrics-text") {
		t.Errorf("Unexpected content type: %s", contentType)
	}

	// Read body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	bodyStr := string(body)

	// Verify metrics are present
	expectedMetrics := []string{
		"tunnelmesh_packets_sent_total",
		"tunnelmesh_active_tunnels",
		"tunnelmesh_peer_info",
		"go_goroutines",       // Standard Go metrics
		"process_cpu_seconds", // Standard process metrics
	}

	for _, metric := range expectedMetrics {
		if !strings.Contains(bodyStr, metric) {
			t.Errorf("Expected metric %s not found in response", metric)
		}
	}

	// Verify our custom metric values
	if !strings.Contains(bodyStr, "tunnelmesh_packets_sent_total{peer=\"test-peer\"} 100") {
		t.Error("Expected packets_sent_total with value 100")
	}

	if !strings.Contains(bodyStr, "tunnelmesh_active_tunnels{peer=\"test-peer\"} 5") {
		t.Error("Expected active_tunnels with value 5")
	}
}

func TestHandler_EmptyRegistry(t *testing.T) {
	oldRegistry := Registry
	Registry = prometheus.NewRegistry()
	defer func() { Registry = oldRegistry }()

	// Don't register any collectors - empty registry

	handler := Handler()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	// Should still return 200 OK
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
}

func TestHandler_OpenMetricsFormat(t *testing.T) {
	oldRegistry := Registry
	Registry = prometheus.NewRegistry()
	defer func() { Registry = oldRegistry }()

	Registry.MustRegister(collectors.NewGoCollector())
	Registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	_ = InitMetrics("test-peer", "10.42.0.1", "1.0.0")

	handler := Handler()

	// Request OpenMetrics format
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "application/openmetrics-text")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// OpenMetrics format should be served
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "openmetrics") && !strings.Contains(contentType, "text/plain") {
		t.Logf("Content-Type: %s (OpenMetrics may fall back to text/plain)", contentType)
	}
}

func TestHandler_LabeledMetrics(t *testing.T) {
	oldRegistry := Registry
	Registry = prometheus.NewRegistry()
	defer func() { Registry = oldRegistry }()

	Registry.MustRegister(collectors.NewGoCollector())
	Registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	m := InitMetrics("test-peer", "10.42.0.1", "1.0.0")

	// Set labeled metrics
	m.ConnectionState.WithLabelValues("peer-a", "ssh").Set(2)
	m.ConnectionState.WithLabelValues("peer-b", "udp").Set(1)
	m.ExitPeerInfo.WithLabelValues("exit-server").Set(1)

	handler := Handler()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	body, _ := io.ReadAll(w.Result().Body)
	bodyStr := string(body)

	// Check labeled metrics are present with correct labels
	if !strings.Contains(bodyStr, `tunnelmesh_connection_state{peer="test-peer",target_peer="peer-a",transport="ssh"} 2`) {
		t.Error("Expected connection_state for peer-a with ssh transport")
	}

	if !strings.Contains(bodyStr, `tunnelmesh_connection_state{peer="test-peer",target_peer="peer-b",transport="udp"} 1`) {
		t.Error("Expected connection_state for peer-b with udp transport")
	}

	if !strings.Contains(bodyStr, `tunnelmesh_exit_node_info{exit_node="exit-server",peer="test-peer"} 1`) {
		t.Error("Expected exit_node_info with exit-server label")
	}
}
