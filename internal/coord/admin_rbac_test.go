package coord

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
)

func TestBindings_ListIncludesGroupBindings(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	req := httptest.NewRequest(http.MethodGet, "/api/bindings", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var bindings []BindingInfo
	err := json.NewDecoder(rec.Body).Decode(&bindings)
	require.NoError(t, err)

	// Should include group bindings (default panel bindings are group-based)
	hasGroupBinding := false
	for _, b := range bindings {
		if b.GroupName != "" {
			hasGroupBinding = true
			break
		}
	}
	assert.True(t, hasGroupBinding, "binding list should include group bindings")
}

func TestIsProtectedGroupBinding(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	// Built-in group binding should be protected
	gb := &auth.GroupBinding{
		GroupName: "everyone",
		RoleName:  "panel-viewer",
	}
	assert.True(t, srv.isProtectedGroupBinding(gb))

	// Custom group binding should not be protected
	// Create a custom group first
	_, err := srv.s3Authorizer.Groups.Create("custom-group", "test")
	require.NoError(t, err)

	customGB := &auth.GroupBinding{
		GroupName: "custom-group",
		RoleName:  "reader",
	}
	assert.False(t, srv.isProtectedGroupBinding(customGB))
}
