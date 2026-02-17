package coord

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWGClients_MethodNotAllowed(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	// WireGuard is not enabled in this test config, but we can test the handler directly
	// by checking that unsupported methods return 405.
	req := httptest.NewRequest(http.MethodDelete, "/api/wireguard/clients", nil)
	rec := httptest.NewRecorder()
	srv.handleWGClients(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestWGClientByID_MissingID(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	req := httptest.NewRequest(http.MethodGet, "/api/wireguard/clients/", nil)
	rec := httptest.NewRecorder()
	srv.handleWGClientByID(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "client ID required")
}
