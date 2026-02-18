package coord

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterRules_MethodNotAllowed(t *testing.T) {
	srv := newTestServerWithS3(t)
	require.NotNil(t, srv.adminMux)

	req := httptest.NewRequest(http.MethodPatch, "/api/filter/rules", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestFilterRulesList_NoPeer(t *testing.T) {
	srv := newTestServerWithS3(t)
	require.NotNil(t, srv.adminMux)

	// GET without ?peer= parameter should return 400
	req := httptest.NewRequest(http.MethodGet, "/api/filter/rules", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "peer parameter required")
}
