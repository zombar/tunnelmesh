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
	assert.True(t, objs[0].Forwarded, "PUT entry should be marked as Forwarded")
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
	assert.True(t, recycled[0].Forwarded, "DELETE recycled entry should be marked as Forwarded")
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

func TestLoadPeerIndexes_PreservesForwardedEntries(t *testing.T) {
	srv := newTestServerWithListingIndex(t)

	// Inject a forwarded entry via updatePeerListingsAfterForward
	req := httptest.NewRequest(http.MethodPut, "/api/s3/buckets/test-bucket/objects/fwd.txt", nil)
	req.Header.Set("Content-Type", "text/plain")
	req.ContentLength = 99
	srv.updatePeerListingsAfterForward("test-bucket", "fwd.txt", req)

	// Simulate having two coordinators so loadPeerIndexes doesn't short-circuit
	srv.storeCoordIPs([]string{"10.0.0.1", "10.0.0.2"})

	// Run loadPeerIndexes — system store has no peer data, so without
	// forwarded-entry preservation the entry would be lost.
	srv.loadPeerIndexes(context.Background())

	pl := srv.peerListings.Load()
	require.NotNil(t, pl)
	objs := pl.Objects["test-bucket"]
	require.Len(t, objs, 1, "forwarded entry should survive loadPeerIndexes")
	assert.Equal(t, "fwd.txt", objs[0].Key)
	assert.True(t, objs[0].Forwarded)
}

func TestLoadPeerIndexes_SupersedesForwardedEntry(t *testing.T) {
	srv := newTestServerWithListingIndex(t)

	// Inject a forwarded entry
	req := httptest.NewRequest(http.MethodPut, "/api/s3/buckets/test-bucket/objects/fwd.txt", nil)
	req.Header.Set("Content-Type", "text/plain")
	req.ContentLength = 99
	srv.updatePeerListingsAfterForward("test-bucket", "fwd.txt", req)

	// Simulate two coordinators
	srv.storeCoordIPs([]string{"10.0.0.1", "10.0.0.2"})

	// Persist a peer index for 10.0.0.2 that contains the same key with a newer timestamp
	peerIdx := &listingIndex{
		Buckets: map[string]*bucketListing{
			"test-bucket": {
				Objects: []S3ObjectInfo{
					{
						Key:          "fwd.txt",
						Size:         99,
						LastModified: "2099-01-01T00:00:00Z",
						ContentType:  "text/plain",
						Owner:        "remote-peer",
					},
				},
			},
		},
	}
	err := srv.s3SystemStore.SaveJSON(context.Background(), "listings/10.0.0.2.json", peerIdx)
	require.NoError(t, err)

	// Run loadPeerIndexes — system store entry should supersede the forwarded one
	srv.loadPeerIndexes(context.Background())

	pl := srv.peerListings.Load()
	require.NotNil(t, pl)
	objs := pl.Objects["test-bucket"]
	require.Len(t, objs, 1)
	assert.Equal(t, "fwd.txt", objs[0].Key)
	assert.Equal(t, "remote-peer", objs[0].Owner, "system store entry should win")
	assert.False(t, objs[0].Forwarded, "persisted entry should not be marked Forwarded")
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
