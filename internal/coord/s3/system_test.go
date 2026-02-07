package s3

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
)

func TestNewSystemStore(t *testing.T) {
	store := newTestStore(t)

	ss, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	// System bucket should exist
	meta, err := store.HeadBucket(SystemBucket)
	require.NoError(t, err)
	assert.Equal(t, SystemBucket, meta.Name)
	assert.Equal(t, "svc:coordinator", meta.Owner)

	// Raw should return the underlying store
	assert.Equal(t, store, ss.Raw())
}

func TestNewSystemStoreExistingBucket(t *testing.T) {
	store := newTestStore(t)

	// Pre-create the system bucket
	require.NoError(t, store.CreateBucket(SystemBucket, "svc:coordinator"))

	// Should not error when bucket exists
	ss, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)
	assert.NotNil(t, ss)
}

func TestSystemStoreSaveLoadUsers(t *testing.T) {
	store := newTestStore(t)
	ss, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	users := []*auth.User{
		{ID: "alice", Name: "Alice", PublicKey: "pk1", CreatedAt: time.Now().UTC()},
		{ID: "bob", Name: "Bob", PublicKey: "pk2", CreatedAt: time.Now().UTC()},
	}

	err = ss.SaveUsers(users)
	require.NoError(t, err)

	loaded, err := ss.LoadUsers()
	require.NoError(t, err)
	require.Len(t, loaded, 2)
	assert.Equal(t, "alice", loaded[0].ID)
	assert.Equal(t, "bob", loaded[1].ID)
}

func TestSystemStoreLoadUsersNotFound(t *testing.T) {
	store := newTestStore(t)
	ss, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	// Should return nil (not error) when not found
	loaded, err := ss.LoadUsers()
	require.NoError(t, err)
	assert.Nil(t, loaded)
}

func TestSystemStoreSaveLoadRoles(t *testing.T) {
	store := newTestStore(t)
	ss, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	roles := []auth.Role{
		{Name: "custom-role", Rules: []auth.Rule{{Verbs: []string{"get"}, Resources: []string{"buckets"}}}},
	}

	err = ss.SaveRoles(roles)
	require.NoError(t, err)

	loaded, err := ss.LoadRoles()
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, "custom-role", loaded[0].Name)
}

func TestSystemStoreSaveLoadBindings(t *testing.T) {
	store := newTestStore(t)
	ss, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	bindings := []*auth.RoleBinding{
		{Name: "b1", UserID: "alice", RoleName: "admin"},
		{Name: "b2", UserID: "bob", RoleName: "bucket-read", BucketScope: "my-bucket"},
	}

	err = ss.SaveBindings(bindings)
	require.NoError(t, err)

	loaded, err := ss.LoadBindings()
	require.NoError(t, err)
	require.Len(t, loaded, 2)
	assert.Equal(t, "alice", loaded[0].UserID)
	assert.Equal(t, "bob", loaded[1].UserID)
	assert.Equal(t, "my-bucket", loaded[1].BucketScope)
}

func TestSystemStoreSaveLoadStatsHistory(t *testing.T) {
	store := newTestStore(t)
	ss, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	// Use a simple map for stats
	stats := map[string][]map[string]interface{}{
		"peer1": {
			{"ts": "2024-01-01T00:00:00Z", "tx": 100},
			{"ts": "2024-01-01T00:00:10Z", "tx": 200},
		},
	}

	err = ss.SaveStatsHistory(stats)
	require.NoError(t, err)

	var loaded map[string][]map[string]interface{}
	err = ss.LoadStatsHistory(&loaded)
	require.NoError(t, err)
	require.Contains(t, loaded, "peer1")
	assert.Len(t, loaded["peer1"], 2)
}

func TestSystemStoreSaveLoadWireGuardClients(t *testing.T) {
	store := newTestStore(t)
	ss, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	clients := []map[string]interface{}{
		{"id": "client1", "name": "laptop", "ip": "172.30.100.1"},
		{"id": "client2", "name": "phone", "ip": "172.30.100.2"},
	}

	err = ss.SaveWireGuardClients(clients)
	require.NoError(t, err)

	var loaded []map[string]interface{}
	err = ss.LoadWireGuardClients(&loaded)
	require.NoError(t, err)
	require.Len(t, loaded, 2)
}

func TestSystemStoreExists(t *testing.T) {
	store := newTestStore(t)
	ss, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	// Initially doesn't exist
	assert.False(t, ss.Exists(UsersPath))

	// Save users
	err = ss.SaveUsers([]*auth.User{{ID: "test", Name: "Test"}})
	require.NoError(t, err)

	// Now exists
	assert.True(t, ss.Exists(UsersPath))
}

func TestSystemStoreDelete(t *testing.T) {
	store := newTestStore(t)
	ss, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	// Save and then delete
	err = ss.SaveUsers([]*auth.User{{ID: "test", Name: "Test"}})
	require.NoError(t, err)
	assert.True(t, ss.Exists(UsersPath))

	err = ss.Delete(UsersPath)
	require.NoError(t, err)
	assert.False(t, ss.Exists(UsersPath))
}

func TestSystemStoreDeleteNotFound(t *testing.T) {
	store := newTestStore(t)
	ss, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	err = ss.Delete("nonexistent.json")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}
