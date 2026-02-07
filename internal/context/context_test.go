package context

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContext_ServiceName(t *testing.T) {
	tests := []struct {
		name     string
		ctx      Context
		expected string
	}{
		{
			name:     "default context",
			ctx:      Context{Name: "default"},
			expected: "tunnelmesh",
		},
		{
			name:     "work context",
			ctx:      Context{Name: "work"},
			expected: "tunnelmesh-work",
		},
		{
			name:     "home context",
			ctx:      Context{Name: "home"},
			expected: "tunnelmesh-home",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.ctx.ServiceName())
		})
	}
}

func TestStore_LoadEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".context")

	store, err := LoadFromPath(path)
	require.NoError(t, err)
	assert.NotNil(t, store.Contexts)
	assert.Empty(t, store.Contexts)
	assert.Empty(t, store.Active)
}

func TestStore_AddAndGet(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".context")

	store, err := LoadFromPath(path)
	require.NoError(t, err)

	ctx := Context{
		Name:       "work",
		ConfigPath: "/path/to/config.yaml",
		Server:     "https://mesh.work.com",
		Domain:     ".work.tunnelmesh",
		MeshIP:     "172.30.0.5",
		DNSListen:  "127.0.0.53:5353",
	}

	store.Add(ctx)

	retrieved := store.Get("work")
	require.NotNil(t, retrieved)
	assert.Equal(t, ctx.Name, retrieved.Name)
	assert.Equal(t, ctx.ConfigPath, retrieved.ConfigPath)
	assert.Equal(t, ctx.Server, retrieved.Server)
	assert.Equal(t, ctx.Domain, retrieved.Domain)
	assert.Equal(t, ctx.MeshIP, retrieved.MeshIP)
	assert.Equal(t, ctx.DNSListen, retrieved.DNSListen)
}

func TestStore_GetNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".context")

	store, err := LoadFromPath(path)
	require.NoError(t, err)

	retrieved := store.Get("nonexistent")
	assert.Nil(t, retrieved)
}

func TestStore_Remove(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".context")

	store, err := LoadFromPath(path)
	require.NoError(t, err)

	store.Add(Context{Name: "work", ConfigPath: "/path/to/work.yaml"})
	store.Add(Context{Name: "home", ConfigPath: "/path/to/home.yaml"})

	assert.Equal(t, 2, store.Count())

	store.Remove("work")

	assert.Equal(t, 1, store.Count())
	assert.Nil(t, store.Get("work"))
	assert.NotNil(t, store.Get("home"))
}

func TestStore_RemoveClearsActive(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".context")

	store, err := LoadFromPath(path)
	require.NoError(t, err)

	store.Add(Context{Name: "work", ConfigPath: "/path/to/work.yaml"})
	store.Active = "work"

	store.Remove("work")

	assert.Empty(t, store.Active)
}

func TestStore_SetActive(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".context")

	store, err := LoadFromPath(path)
	require.NoError(t, err)

	store.Add(Context{Name: "work", ConfigPath: "/path/to/work.yaml"})

	err = store.SetActive("work")
	require.NoError(t, err)
	assert.Equal(t, "work", store.Active)
}

func TestStore_SetActiveNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".context")

	store, err := LoadFromPath(path)
	require.NoError(t, err)

	err = store.SetActive("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_GetActive(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".context")

	store, err := LoadFromPath(path)
	require.NoError(t, err)

	// No active context
	assert.Nil(t, store.GetActive())

	store.Add(Context{Name: "work", ConfigPath: "/path/to/work.yaml"})
	store.Active = "work"

	active := store.GetActive()
	require.NotNil(t, active)
	assert.Equal(t, "work", active.Name)
}

func TestStore_List(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".context")

	store, err := LoadFromPath(path)
	require.NoError(t, err)

	store.Add(Context{Name: "home", ConfigPath: "/path/to/home.yaml"})
	store.Add(Context{Name: "work", ConfigPath: "/path/to/work.yaml"})
	store.Add(Context{Name: "alpha", ConfigPath: "/path/to/alpha.yaml"})

	list := store.List()
	require.Len(t, list, 3)

	// Should be sorted alphabetically
	assert.Equal(t, "alpha", list[0].Name)
	assert.Equal(t, "home", list[1].Name)
	assert.Equal(t, "work", list[2].Name)
}

func TestStore_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".tunnelmesh", ".context")

	store, err := LoadFromPath(path)
	require.NoError(t, err)

	store.Add(Context{
		Name:       "work",
		ConfigPath: "/path/to/work.yaml",
		Server:     "https://mesh.work.com",
		Domain:     ".work.tunnelmesh",
		MeshIP:     "172.30.0.5",
		DNSListen:  "127.0.0.53:5353",
	})
	store.Add(Context{
		Name:       "home",
		ConfigPath: "/path/to/home.yaml",
		Server:     "https://mesh.home.net",
	})
	store.Active = "work"

	err = store.Save()
	require.NoError(t, err)

	// Verify file was created
	_, err = os.Stat(path)
	require.NoError(t, err)

	// Load again
	store2, err := LoadFromPath(path)
	require.NoError(t, err)

	assert.Equal(t, "work", store2.Active)
	assert.Equal(t, 2, store2.Count())

	work := store2.Get("work")
	require.NotNil(t, work)
	assert.Equal(t, "https://mesh.work.com", work.Server)
	assert.Equal(t, ".work.tunnelmesh", work.Domain)
	assert.Equal(t, "172.30.0.5", work.MeshIP)
}

func TestStore_IsEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".context")

	store, err := LoadFromPath(path)
	require.NoError(t, err)

	assert.True(t, store.IsEmpty())

	store.Add(Context{Name: "work"})

	assert.False(t, store.IsEmpty())
}

func TestStore_HasActive(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".context")

	store, err := LoadFromPath(path)
	require.NoError(t, err)

	assert.False(t, store.HasActive())

	store.Add(Context{Name: "work"})
	assert.False(t, store.HasActive())

	store.Active = "work"
	assert.True(t, store.HasActive())

	// Active points to non-existent context
	store.Active = "nonexistent"
	assert.False(t, store.HasActive())
}

func TestStore_UpdateExisting(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".context")

	store, err := LoadFromPath(path)
	require.NoError(t, err)

	store.Add(Context{Name: "work", Server: "https://old.server.com"})

	// Update with new values
	store.Add(Context{Name: "work", Server: "https://new.server.com", MeshIP: "172.30.0.10"})

	work := store.Get("work")
	require.NotNil(t, work)
	assert.Equal(t, "https://new.server.com", work.Server)
	assert.Equal(t, "172.30.0.10", work.MeshIP)
	assert.Equal(t, 1, store.Count()) // Should still be just one context
}

func TestDefaultStorePath(t *testing.T) {
	path, err := DefaultStorePath()
	require.NoError(t, err)
	assert.Contains(t, path, ".tunnelmesh")
	assert.Contains(t, path, ".context")
}

func TestLoad(t *testing.T) {
	// Load uses DefaultStorePath which may or may not have a file
	// This test just ensures it doesn't panic
	store, err := Load()
	require.NoError(t, err)
	assert.NotNil(t, store)
}

func TestLoadFromPath_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".context")

	// Write invalid JSON
	err := os.WriteFile(path, []byte("not valid json"), 0600)
	require.NoError(t, err)

	_, err = LoadFromPath(path)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse context file")
}

func TestLoadFromPath_NilContextsMap(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".context")

	// Write JSON with null contexts
	err := os.WriteFile(path, []byte(`{"active":"test","contexts":null}`), 0600)
	require.NoError(t, err)

	store, err := LoadFromPath(path)
	require.NoError(t, err)
	assert.NotNil(t, store.Contexts)
}

func TestStore_AddToNilMap(t *testing.T) {
	store := &Store{
		Contexts: nil,
	}

	store.Add(Context{Name: "test"})

	assert.NotNil(t, store.Contexts)
	assert.Equal(t, 1, len(store.Contexts))
}

func TestStore_SaveCreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "nested", "dir", ".context")

	store := &Store{
		Contexts: make(map[string]Context),
		path:     path,
	}
	store.Add(Context{Name: "test"})

	err := store.Save()
	require.NoError(t, err)

	// Verify directory was created
	_, err = os.Stat(filepath.Dir(path))
	require.NoError(t, err)
}
