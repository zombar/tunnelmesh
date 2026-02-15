package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
