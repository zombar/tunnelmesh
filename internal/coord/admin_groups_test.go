package coord

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroups_List(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	req := httptest.NewRequest(http.MethodGet, "/api/groups", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var groups []json.RawMessage
	err := json.NewDecoder(rec.Body).Decode(&groups)
	require.NoError(t, err)
	// Should have at least built-in groups (everyone, admins)
	assert.GreaterOrEqual(t, len(groups), 2)
}

func TestGroups_Create(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	body, _ := json.Marshal(GroupCreateRequest{
		Name:        "test-group",
		Description: "A test group",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestGroups_Create_Duplicate(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	body, _ := json.Marshal(GroupCreateRequest{Name: "dup-group"})

	// First creation should succeed
	req := httptest.NewRequest(http.MethodPost, "/api/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code)

	// Second creation should conflict
	body, _ = json.Marshal(GroupCreateRequest{Name: "dup-group"})
	req = httptest.NewRequest(http.MethodPost, "/api/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestGroupByName_Get(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	// Built-in "everyone" group should be accessible
	req := httptest.NewRequest(http.MethodGet, "/api/groups/everyone", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGroupByName_Delete(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	// Create a group first
	body, _ := json.Marshal(GroupCreateRequest{Name: "deletable"})
	req := httptest.NewRequest(http.MethodPost, "/api/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	// Delete it
	req = httptest.NewRequest(http.MethodDelete, "/api/groups/deletable", nil)
	rec = httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGroupByName_DeleteBuiltin_Forbidden(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/groups/everyone", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "cannot delete built-in group")
}

func TestGroupMembers_AddAndRemove(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	// Create a group
	body, _ := json.Marshal(GroupCreateRequest{Name: "members-test"})
	req := httptest.NewRequest(http.MethodPost, "/api/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	// Add a member
	body, _ = json.Marshal(GroupMemberRequest{UserID: "user-123"})
	req = httptest.NewRequest(http.MethodPost, "/api/groups/members-test/members", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code)

	// List members
	req = httptest.NewRequest(http.MethodGet, "/api/groups/members-test/members", nil)
	rec = httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var members []string
	err := json.NewDecoder(rec.Body).Decode(&members)
	require.NoError(t, err)
	assert.Contains(t, members, "user-123")

	// Remove member
	req = httptest.NewRequest(http.MethodDelete, "/api/groups/members-test/members/user-123", nil)
	rec = httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGroupBindings_AddAndList(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	// Create a group
	body, _ := json.Marshal(GroupCreateRequest{Name: "bind-test"})
	req := httptest.NewRequest(http.MethodPost, "/api/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	// Add a binding
	body, _ = json.Marshal(GroupBindingRequest{RoleName: "reader"})
	req = httptest.NewRequest(http.MethodPost, "/api/groups/bind-test/bindings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code)

	// List bindings
	req = httptest.NewRequest(http.MethodGet, "/api/groups/bind-test/bindings", nil)
	rec = httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}
