package peer

import (
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTunnel is a simple mock for testing
type mockTunnel struct {
	closed bool
	mu     sync.Mutex
}

func newMockTunnel() *mockTunnel {
	return &mockTunnel{}
}

func (m *mockTunnel) Read(p []byte) (n int, err error) {
	return 0, io.EOF
}

func (m *mockTunnel) Write(p []byte) (n int, err error) {
	return len(p), nil
}

func (m *mockTunnel) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockTunnel) IsClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func TestNewTunnelAdapter(t *testing.T) {
	adapter := NewTunnelAdapter()

	require.NotNil(t, adapter)
	assert.Empty(t, adapter.List())
}

func TestTunnelAdapter_AddAndGet(t *testing.T) {
	adapter := NewTunnelAdapter()
	tunnel := newMockTunnel()

	adapter.Add("peer1", tunnel)

	got, ok := adapter.Get("peer1")
	assert.True(t, ok)
	assert.Same(t, tunnel, got)
}

func TestTunnelAdapter_Get_NotFound(t *testing.T) {
	adapter := NewTunnelAdapter()

	got, ok := adapter.Get("nonexistent")

	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestTunnelAdapter_Add_ReplacesExisting(t *testing.T) {
	adapter := NewTunnelAdapter()
	tunnel1 := newMockTunnel()
	tunnel2 := newMockTunnel()

	adapter.Add("peer1", tunnel1)
	adapter.Add("peer1", tunnel2)

	// Old tunnel should be closed
	assert.True(t, tunnel1.IsClosed())
	// New tunnel should be active
	got, ok := adapter.Get("peer1")
	assert.True(t, ok)
	assert.Same(t, tunnel2, got)
}

func TestTunnelAdapter_Remove(t *testing.T) {
	adapter := NewTunnelAdapter()
	tunnel := newMockTunnel()

	adapter.Add("peer1", tunnel)
	adapter.Remove("peer1")

	_, ok := adapter.Get("peer1")
	assert.False(t, ok)
	assert.True(t, tunnel.IsClosed())
}

func TestTunnelAdapter_Remove_NonExistent(t *testing.T) {
	adapter := NewTunnelAdapter()

	// Should not panic
	adapter.Remove("nonexistent")
}

func TestTunnelAdapter_Remove_CallsCallback(t *testing.T) {
	adapter := NewTunnelAdapter()
	tunnel := newMockTunnel()
	callbackCalled := false

	adapter.SetOnRemove(func() {
		callbackCalled = true
	})
	adapter.Add("peer1", tunnel)
	adapter.Remove("peer1")

	assert.True(t, callbackCalled)
}

func TestTunnelAdapter_RemoveIfMatch(t *testing.T) {
	adapter := NewTunnelAdapter()
	tunnel := newMockTunnel()

	adapter.Add("peer1", tunnel)
	adapter.RemoveIfMatch("peer1", tunnel)

	_, ok := adapter.Get("peer1")
	assert.False(t, ok)
}

func TestTunnelAdapter_RemoveIfMatch_DoesNotRemoveDifferent(t *testing.T) {
	adapter := NewTunnelAdapter()
	tunnel1 := newMockTunnel()
	tunnel2 := newMockTunnel()

	adapter.Add("peer1", tunnel1)
	adapter.RemoveIfMatch("peer1", tunnel2) // Different tunnel

	// tunnel1 should still be there
	got, ok := adapter.Get("peer1")
	assert.True(t, ok)
	assert.Same(t, tunnel1, got)
}

func TestTunnelAdapter_RemoveIfMatch_CallsCallbackOnlyOnRemoval(t *testing.T) {
	adapter := NewTunnelAdapter()
	tunnel1 := newMockTunnel()
	tunnel2 := newMockTunnel()
	callbackCount := 0

	adapter.SetOnRemove(func() {
		callbackCount++
	})
	adapter.Add("peer1", tunnel1)

	// Non-matching should not call callback
	adapter.RemoveIfMatch("peer1", tunnel2)
	assert.Equal(t, 0, callbackCount)

	// Matching should call callback
	adapter.RemoveIfMatch("peer1", tunnel1)
	assert.Equal(t, 1, callbackCount)
}

func TestTunnelAdapter_CloseAll(t *testing.T) {
	adapter := NewTunnelAdapter()
	tunnel1 := newMockTunnel()
	tunnel2 := newMockTunnel()

	adapter.Add("peer1", tunnel1)
	adapter.Add("peer2", tunnel2)
	adapter.CloseAll()

	assert.Empty(t, adapter.List())
	assert.True(t, tunnel1.IsClosed())
	assert.True(t, tunnel2.IsClosed())
}

func TestTunnelAdapter_List(t *testing.T) {
	adapter := NewTunnelAdapter()

	adapter.Add("peer1", newMockTunnel())
	adapter.Add("peer2", newMockTunnel())
	adapter.Add("peer3", newMockTunnel())

	list := adapter.List()
	assert.Len(t, list, 3)
	assert.Contains(t, list, "peer1")
	assert.Contains(t, list, "peer2")
	assert.Contains(t, list, "peer3")
}

func TestTunnelAdapter_ConcurrentAccess(t *testing.T) {
	adapter := NewTunnelAdapter()
	var wg sync.WaitGroup

	// Concurrent adds
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := string(rune('a' + i%26))
			adapter.Add(name, newMockTunnel())
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := string(rune('a' + i%26))
			adapter.Get(name)
		}(i)
	}

	// Concurrent lists
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			adapter.List()
		}()
	}

	wg.Wait()
	// No race conditions should occur
}
