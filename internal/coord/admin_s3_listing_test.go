package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpsertObjectList_NewKey(t *testing.T) {
	objs := []S3ObjectInfo{
		{Key: "a.txt", Size: 10},
	}

	info := S3ObjectInfo{Key: "b.txt", Size: 20}
	result := upsertObjectList(objs, "b.txt", info)

	assert.Len(t, result, 2)
	assert.Equal(t, "a.txt", result[0].Key)
	assert.Equal(t, "b.txt", result[1].Key)
}

func TestRemoveFromObjectList_NotFound(t *testing.T) {
	objs := []S3ObjectInfo{
		{Key: "a.txt", Size: 10},
		{Key: "b.txt", Size: 20},
	}

	result, removed := removeFromObjectList(objs, "nonexistent.txt")

	assert.Len(t, result, 2)
	assert.Nil(t, removed)
}

func TestListingIndexEqual_BothNil(t *testing.T) {
	assert.True(t, listingIndexEqual(nil, nil))
}

func TestListingIndexEqual_OneNil(t *testing.T) {
	idx := &listingIndex{Buckets: make(map[string]*bucketListing)}
	assert.False(t, listingIndexEqual(nil, idx))
	assert.False(t, listingIndexEqual(idx, nil))
}

func TestListingIndexEqual_IgnoresSeq(t *testing.T) {
	a := &listingIndex{
		Buckets: map[string]*bucketListing{
			"b": {Objects: []S3ObjectInfo{{Key: "k", Size: 1, LastModified: "t1"}}},
		},
		Seq: 5,
	}
	b := &listingIndex{
		Buckets: map[string]*bucketListing{
			"b": {Objects: []S3ObjectInfo{{Key: "k", Size: 1, LastModified: "t1"}}},
		},
		Seq: 99,
	}
	assert.True(t, listingIndexEqual(a, b), "Seq should be ignored in equality comparison")
}

func TestUpdateListingIndex_IncrementsSeq(t *testing.T) {
	srv := newTestServerWithListingIndex(t)

	info := S3ObjectInfo{Key: "a.txt", Size: 10, LastModified: "2024-01-01T00:00:00Z"}
	srv.updateListingIndex("bkt", "a.txt", &info, "put")

	idx := srv.localListingIndex.Load()
	require.NotNil(t, idx)
	assert.Equal(t, uint64(1), idx.Seq)

	// Second update should increment again
	info2 := S3ObjectInfo{Key: "b.txt", Size: 20, LastModified: "2024-01-02T00:00:00Z"}
	srv.updateListingIndex("bkt", "b.txt", &info2, "put")

	idx2 := srv.localListingIndex.Load()
	assert.Equal(t, uint64(2), idx2.Seq)
}

func TestReconcileLocalIndex_MergesWithConcurrentUpdates(t *testing.T) {
	srv := newTestServerWithListingIndex(t)

	// Pre-populate listing index (Seq=1)
	info := S3ObjectInfo{Key: "file.txt", Size: 100, LastModified: "2024-01-01T00:00:00Z"}
	srv.updateListingIndex("test-bucket", "file.txt", &info, "put")

	before := srv.localListingIndex.Load()
	require.NotNil(t, before)
	require.Equal(t, uint64(1), before.Seq)

	// Run reconcile in a goroutine so we can do an incremental update while
	// it scans the filesystem (ListBuckets → ListObjects → ListRecycledObjects).
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.reconcileLocalIndex(t.Context())
	}()

	// Give reconcile time to capture preSeq and start the filesystem scan.
	time.Sleep(20 * time.Millisecond)

	// Incremental update bumps Seq from 1 → 2 while reconcile is mid-scan.
	info2 := S3ObjectInfo{Key: "concurrent.txt", Size: 50, LastModified: "2024-01-02T00:00:00Z"}
	srv.updateListingIndex("test-bucket", "concurrent.txt", &info2, "put")

	<-done

	after := srv.localListingIndex.Load()
	require.NotNil(t, after)

	// The concurrent update must be preserved — reconcile merges filesystem
	// scan (ground truth) with incremental updates that arrived during the scan.
	assert.Equal(t, uint64(2), after.Seq)
	bl := after.Buckets["test-bucket"]
	require.NotNil(t, bl)
	found := false
	for _, obj := range bl.Objects {
		if obj.Key == "concurrent.txt" {
			found = true
		}
	}
	assert.True(t, found, "concurrent incremental update should be preserved via merge")
}

func TestReconcileLocalIndex_RemovesStaleEntries(t *testing.T) {
	srv := newTestServerWithListingIndex(t)

	// Add a stale entry that does NOT exist on the filesystem
	stale := S3ObjectInfo{Key: "stale-ghost.txt", Size: 999, LastModified: "2024-01-01T00:00:00Z"}
	srv.updateListingIndex("test-bucket", "stale-ghost.txt", &stale, "put")

	// Also add one more to bump Seq (simulating sustained writes)
	info := S3ObjectInfo{Key: "another.txt", Size: 10, LastModified: "2024-01-02T00:00:00Z"}
	srv.updateListingIndex("test-bucket", "another.txt", &info, "put")

	before := srv.localListingIndex.Load()
	require.NotNil(t, before)
	require.Equal(t, uint64(2), before.Seq)

	// Reconcile scans the actual filesystem — neither stale-ghost.txt nor
	// another.txt exist on disk, so both should be removed.
	srv.reconcileLocalIndex(t.Context())

	after := srv.localListingIndex.Load()
	require.NotNil(t, after)

	// The stale entry should be gone (filesystem is ground truth)
	bl := after.Buckets["test-bucket"]
	if bl != nil {
		for _, obj := range bl.Objects {
			assert.NotEqual(t, "stale-ghost.txt", obj.Key, "stale entry should be removed by reconcile")
		}
	}
}

func TestForwardedEntryTTL(t *testing.T) {
	srv := newTestServerWithListingIndex(t)

	// Need multiple coord IPs for loadPeerIndexes to process forwarded entries
	srv.storeCoordIPs([]string{"10.0.0.1", "10.0.0.2"})

	// Seed peerListings with a forwarded entry that has already expired
	expired := &peerListings{
		Objects: map[string][]S3ObjectInfo{
			"bkt": {
				{
					Key:         "old.txt",
					Size:        10,
					Forwarded:   true,
					ForwardedAt: time.Now().Add(-15 * time.Minute), // 15min ago, well past 10min TTL
					SourceIP:    "10.0.0.2",
				},
				{
					Key:         "fresh.txt",
					Size:        20,
					Forwarded:   true,
					ForwardedAt: time.Now(), // just now
					SourceIP:    "10.0.0.2",
				},
			},
		},
		Recycled: make(map[string][]S3ObjectInfo),
	}
	srv.peerListings.Store(expired)

	// loadPeerIndexes should drop "old.txt" (expired) and keep "fresh.txt"
	srv.loadPeerIndexes(t.Context())

	pl := srv.peerListings.Load()
	require.NotNil(t, pl)

	var foundOld, foundFresh bool
	for _, obj := range pl.Objects["bkt"] {
		if obj.Key == "old.txt" {
			foundOld = true
		}
		if obj.Key == "fresh.txt" {
			foundFresh = true
		}
	}
	assert.False(t, foundOld, "expired forwarded entry should be removed")
	assert.True(t, foundFresh, "fresh forwarded entry should be preserved")
}

func TestUpdateListingIndex_DeleteDeduplicatesRecycled(t *testing.T) {
	srv := newTestServerWithListingIndex(t)

	// Put → Delete → Put → Delete the same key
	info := S3ObjectInfo{Key: "file.txt", Size: 100, LastModified: "2024-01-01T00:00:00Z"}
	srv.updateListingIndex("bkt", "file.txt", &info, "put")
	srv.updateListingIndex("bkt", "file.txt", nil, "delete")

	info2 := S3ObjectInfo{Key: "file.txt", Size: 200, LastModified: "2024-02-01T00:00:00Z"}
	srv.updateListingIndex("bkt", "file.txt", &info2, "put")
	srv.updateListingIndex("bkt", "file.txt", nil, "delete")

	idx := srv.localListingIndex.Load()
	require.NotNil(t, idx)
	bl := idx.Buckets["bkt"]
	require.NotNil(t, bl)

	// Should have exactly one recycled entry (dedup by key)
	assert.Len(t, bl.Recycled, 1, "recycled list should be deduplicated")
	assert.Equal(t, "file.txt", bl.Recycled[0].Key)
}
