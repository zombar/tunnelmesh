package dns

import (
	"sync"
	"testing"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetGlobalRegistry clears the global SRV registry for test isolation
func resetGlobalRegistry() {
	globalSRVRegistry.mu.Lock()
	globalSRVRegistry.records = make(map[string]*srvRecord)
	globalSRVRegistry.mu.Unlock()
}

func TestPublishCoordinator(t *testing.T) {
	resetGlobalRegistry()

	tests := []struct {
		name        string
		coordName   string
		addr        string
		expectError bool
		expectPort  uint16
	}{
		{
			name:        "standard address with port",
			coordName:   "coord1.tunnelmesh",
			addr:        ":8443",
			expectError: false,
			expectPort:  8443,
		},
		{
			name:        "address with host and port",
			coordName:   "coord2.tunnelmesh",
			addr:        "0.0.0.0:9000",
			expectError: false,
			expectPort:  9000,
		},
		{
			name:        "just port number",
			coordName:   "coord3.tunnelmesh",
			addr:        "443",
			expectError: false,
			expectPort:  443,
		},
		{
			name:        "invalid port",
			coordName:   "coord4.tunnelmesh",
			addr:        "invalid",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := PublishCoordinator(tt.coordName, tt.addr)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Verify record was added
			globalSRVRegistry.mu.RLock()
			record, exists := globalSRVRegistry.records[tt.coordName]
			globalSRVRegistry.mu.RUnlock()

			assert.True(t, exists)
			assert.Equal(t, tt.coordName, record.Target)
			assert.Equal(t, tt.expectPort, record.Port)
			assert.Equal(t, uint16(10), record.Priority)
			assert.Equal(t, uint16(10), record.Weight)
		})
	}
}

func TestUnpublishCoordinator(t *testing.T) {
	resetGlobalRegistry()

	// Publish a coordinator
	err := PublishCoordinator("coord1.tunnelmesh", ":8443")
	require.NoError(t, err)

	// Verify it exists
	assert.Equal(t, 1, GetCoordinatorCount())

	// Unpublish it
	UnpublishCoordinator("coord1.tunnelmesh")

	// Verify it's gone
	assert.Equal(t, 0, GetCoordinatorCount())

	// Unpublishing non-existent coordinator should not panic
	UnpublishCoordinator("nonexistent.tunnelmesh")
}

func TestDiscoverCoordinators(t *testing.T) {
	resetGlobalRegistry()

	tests := []struct {
		name          string
		setup         func()
		domain        string
		expectError   bool
		expectCount   int
		expectContain []string
	}{
		{
			name: "discover multiple coordinators",
			setup: func() {
				_ = PublishCoordinator("coord1.tunnelmesh", ":8443")
				_ = PublishCoordinator("coord2.tunnelmesh", ":8443")
				_ = PublishCoordinator("coord3.tunnelmesh", ":9000")
			},
			domain:      "tunnelmesh",
			expectError: false,
			expectCount: 3,
			expectContain: []string{
				"coord1.tunnelmesh:8443",
				"coord2.tunnelmesh:8443",
				"coord3.tunnelmesh:9000",
			},
		},
		{
			name: "discover with empty domain defaults to tunnelmesh",
			setup: func() {
				_ = PublishCoordinator("coord1.tunnelmesh", ":8443")
			},
			domain:      "",
			expectError: false,
			expectCount: 1,
			expectContain: []string{
				"coord1.tunnelmesh:8443",
			},
		},
		{
			name:        "no coordinators published",
			setup:       func() {},
			domain:      "tunnelmesh",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetGlobalRegistry()
			tt.setup()

			addrs, err := DiscoverCoordinators(tt.domain)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Len(t, addrs, tt.expectCount)

			// Verify all expected addresses are present
			for _, expected := range tt.expectContain {
				assert.Contains(t, addrs, expected)
			}
		})
	}
}

func TestHandleSRVQuery(t *testing.T) {
	resetGlobalRegistry()

	tests := []struct {
		name        string
		setup       func()
		qname       string
		expectCount int
		expectNil   bool
	}{
		{
			name: "valid SRV query returns records",
			setup: func() {
				_ = PublishCoordinator("coord1.tunnelmesh", ":8443")
				_ = PublishCoordinator("coord2.tunnelmesh", ":9000")
			},
			qname:       "_coord._tcp.tunnelmesh.",
			expectCount: 2,
			expectNil:   false,
		},
		{
			name:        "wrong query name returns nil",
			setup:       func() {},
			qname:       "_other._tcp.tunnelmesh.",
			expectCount: 0,
			expectNil:   true,
		},
		{
			name:        "no coordinators returns nil",
			setup:       func() {},
			qname:       "_coord._tcp.tunnelmesh.",
			expectCount: 0,
			expectNil:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetGlobalRegistry()
			tt.setup()

			records := HandleSRVQuery(tt.qname)

			if tt.expectNil {
				assert.Nil(t, records)
				return
			}

			require.NotNil(t, records)
			assert.Len(t, records, tt.expectCount)

			// Verify SRV record structure
			for _, rr := range records {
				srv, ok := rr.(*dns.SRV)
				require.True(t, ok, "record should be *dns.SRV")
				assert.Equal(t, "_coord._tcp.tunnelmesh.", srv.Hdr.Name)
				assert.Equal(t, dns.TypeSRV, srv.Hdr.Rrtype)
				assert.Equal(t, uint16(dns.ClassINET), srv.Hdr.Class)
				assert.Equal(t, uint32(300), srv.Hdr.Ttl)
				assert.Equal(t, uint16(10), srv.Priority)
				assert.Equal(t, uint16(10), srv.Weight)
				assert.NotEmpty(t, srv.Target)
				assert.Greater(t, srv.Port, uint16(0))
			}
		})
	}
}

func TestGetCoordinatorCount(t *testing.T) {
	resetGlobalRegistry()

	assert.Equal(t, 0, GetCoordinatorCount())

	_ = PublishCoordinator("coord1.tunnelmesh", ":8443")
	assert.Equal(t, 1, GetCoordinatorCount())

	_ = PublishCoordinator("coord2.tunnelmesh", ":8443")
	assert.Equal(t, 2, GetCoordinatorCount())

	UnpublishCoordinator("coord1.tunnelmesh")
	assert.Equal(t, 1, GetCoordinatorCount())

	UnpublishCoordinator("coord2.tunnelmesh")
	assert.Equal(t, 0, GetCoordinatorCount())
}

func TestConcurrentAccess(t *testing.T) {
	resetGlobalRegistry()

	const goroutines = 50
	const opsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines * 3) // 3 types of operations

	// Concurrent publishers
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				name := "coord" + string(rune('a'+id%26)) + ".tunnelmesh"
				_ = PublishCoordinator(name, ":8443")
			}
		}(i)
	}

	// Concurrent discoverers
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				_, _ = DiscoverCoordinators("tunnelmesh")
			}
		}()
	}

	// Concurrent unpublishers
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				name := "coord" + string(rune('a'+id%26)) + ".tunnelmesh"
				UnpublishCoordinator(name)
			}
		}(i)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	// No assertions needed - if we get here without panicking, the test passes
	// This tests thread safety of the SRV registry
}

func TestPublishCoordinator_UpdateExisting(t *testing.T) {
	resetGlobalRegistry()

	// Publish coordinator on port 8443
	err := PublishCoordinator("coord1.tunnelmesh", ":8443")
	require.NoError(t, err)

	// Verify initial port
	addrs, err := DiscoverCoordinators("tunnelmesh")
	require.NoError(t, err)
	assert.Contains(t, addrs, "coord1.tunnelmesh:8443")

	// Republish same coordinator on different port (simulates restart)
	err = PublishCoordinator("coord1.tunnelmesh", ":9000")
	require.NoError(t, err)

	// Verify port was updated
	addrs, err = DiscoverCoordinators("tunnelmesh")
	require.NoError(t, err)
	assert.Contains(t, addrs, "coord1.tunnelmesh:9000")
	assert.NotContains(t, addrs, "coord1.tunnelmesh:8443")

	// Should still have only one coordinator
	assert.Equal(t, 1, GetCoordinatorCount())
}
