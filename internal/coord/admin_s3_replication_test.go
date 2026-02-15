package coord

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdatePeerListingsAfterForward_Put(t *testing.T) {
	srv := newTestServerWithListingIndex(t)

	// Create a PUT request to simulate a forwarded write
	req := httptest.NewRequest(http.MethodPut, "/api/s3/buckets/test-bucket/objects/forwarded.txt", nil)
	req.Header.Set("Content-Type", "text/plain")
	req.ContentLength = 42

	srv.updatePeerListingsAfterForward("test-bucket", "forwarded.txt", req)

	pl := srv.peerListings.Load()
	require.NotNil(t, pl)
	objs := pl.Objects["test-bucket"]
	require.Len(t, objs, 1)
	assert.Equal(t, "forwarded.txt", objs[0].Key)
	assert.Equal(t, int64(42), objs[0].Size)
	assert.Equal(t, "text/plain", objs[0].ContentType)
}

func TestUpdatePeerListingsAfterForward_Delete(t *testing.T) {
	srv := newTestServerWithListingIndex(t)

	// First add an entry via PUT
	putReq := httptest.NewRequest(http.MethodPut, "/api/s3/buckets/test-bucket/objects/del.txt", nil)
	putReq.ContentLength = 10
	srv.updatePeerListingsAfterForward("test-bucket", "del.txt", putReq)

	// Then delete it
	delReq := httptest.NewRequest(http.MethodDelete, "/api/s3/buckets/test-bucket/objects/del.txt", nil)
	srv.updatePeerListingsAfterForward("test-bucket", "del.txt", delReq)

	pl := srv.peerListings.Load()
	require.NotNil(t, pl)
	assert.Empty(t, pl.Objects["test-bucket"])

	// Verify the deleted object appears in the recycled list with a DeletedAt timestamp
	recycled := pl.Recycled["test-bucket"]
	require.Len(t, recycled, 1)
	assert.Equal(t, "del.txt", recycled[0].Key)
	assert.Equal(t, int64(10), recycled[0].Size)
	assert.NotEmpty(t, recycled[0].DeletedAt)
}

func TestUpdatePeerListingsAfterForward_DeleteNonExistent(t *testing.T) {
	srv := newTestServerWithListingIndex(t)

	// Delete a key that was never added — recycled list should stay empty
	delReq := httptest.NewRequest(http.MethodDelete, "/api/s3/buckets/test-bucket/objects/ghost.txt", nil)
	srv.updatePeerListingsAfterForward("test-bucket", "ghost.txt", delReq)

	pl := srv.peerListings.Load()
	require.NotNil(t, pl)
	assert.Empty(t, pl.Objects["test-bucket"])
	assert.Empty(t, pl.Recycled["test-bucket"])
}

func TestDiscardResponseWriter_CapturesStatus(t *testing.T) {
	d := &discardResponseWriter{header: make(http.Header)}

	d.WriteHeader(http.StatusNotFound)
	assert.Equal(t, http.StatusNotFound, d.status)

	n, err := d.Write([]byte("discarded"))
	assert.NoError(t, err)
	assert.Equal(t, 9, n) // len("discarded")
}

func TestObjectPrimaryCoordinator_NilIPs(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Coordinator.Enabled = true
	srv, err := NewServer(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(t, srv) })

	// No stored IPs - should return empty
	assert.Equal(t, "", srv.objectPrimaryCoordinator("bucket", "key"))
}
