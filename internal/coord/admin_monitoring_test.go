package coord

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupMonitoringProxies_NilAdminMux(t *testing.T) {
	// Construct a bare server with nil adminMux to test the guard
	srv := &Server{}
	require.Nil(t, srv.adminMux)

	// Should not panic with nil adminMux
	srv.SetupMonitoringProxies(MonitoringProxyConfig{
		PrometheusURL: "http://localhost:9090",
	})
}

func TestForwardToMonitoringCoordinator_NoCoordinator(t *testing.T) {
	srv := newTestServer(t)

	// No monitoring coordinator available (empty coordinators map)
	req := httptest.NewRequest(http.MethodGet, "/grafana/", nil)
	rec := httptest.NewRecorder()
	srv.forwardToMonitoringCoordinator(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "no monitoring coordinator available")
}
