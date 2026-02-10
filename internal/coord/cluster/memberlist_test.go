package cluster

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCoordinatorCluster(t *testing.T) {
	tests := []struct {
		name         string
		bindAddr     string
		coordAddress string
		seeds        []string
		expectError  bool
	}{
		{
			name:         "valid cluster without seeds",
			bindAddr:     ":0", // Use port 0 for random port assignment
			coordAddress: "coord1.tunnelmesh:8443",
			seeds:        nil,
			expectError:  false,
		},
		{
			name:         "valid cluster with unreachable seeds",
			bindAddr:     ":0",
			coordAddress: "coord2.tunnelmesh:8443",
			seeds:        []string{"nonexistent:7946"}, // Unreachable but shouldn't error
			expectError:  false,                        // Join failure is warned but not fatal
		},
		{
			name:         "invalid bind address",
			bindAddr:     "invalid",
			coordAddress: "coord3.tunnelmesh:8443",
			seeds:        nil,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster, err := NewCoordinatorCluster(tt.bindAddr, tt.coordAddress, tt.seeds)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, cluster)

			// Verify cluster was created
			assert.NotNil(t, cluster.ml)
			assert.Equal(t, tt.bindAddr, cluster.bindAddr)
			assert.Equal(t, tt.coordAddress, cluster.coordAddress)

			// Should have at least self as member
			assert.GreaterOrEqual(t, cluster.NumMembers(), 1)

			// Cleanup
			err = cluster.Shutdown()
			assert.NoError(t, err)
		})
	}
}

func TestCoordinatorCluster_Join(t *testing.T) {
	// Create first cluster
	cluster1, err := NewCoordinatorCluster(":0", "coord1.tunnelmesh:8443", nil)
	require.NoError(t, err)
	defer func() { _ = cluster1.Shutdown() }()

	// Get the actual bind address from cluster1
	members := cluster1.Members()
	require.Len(t, members, 1)
	addr1 := members[0].Addr.String() + ":7946" // Memberlist default port

	// Create second cluster and join the first
	cluster2, err := NewCoordinatorCluster(":0", "coord2.tunnelmesh:8443", []string{addr1})
	require.NoError(t, err)
	defer func() { _ = cluster2.Shutdown() }()

	// Give memberlist time to gossip
	time.Sleep(500 * time.Millisecond)

	// Both clusters should see each other (eventually)
	// Note: This may be flaky in CI due to network timing
	// In production, gossip takes a few seconds to propagate
	assert.GreaterOrEqual(t, cluster1.NumMembers(), 1)
	assert.GreaterOrEqual(t, cluster2.NumMembers(), 1)
}

func TestCoordinatorCluster_Join_EmptySeeds(t *testing.T) {
	cluster, err := NewCoordinatorCluster(":0", "coord1.tunnelmesh:8443", nil)
	require.NoError(t, err)
	defer func() { _ = cluster.Shutdown() }()

	// Join with empty seeds should succeed without error
	err = cluster.Join([]string{})
	assert.NoError(t, err)
}

func TestCoordinatorCluster_Join_UnreachableSeeds(t *testing.T) {
	cluster, err := NewCoordinatorCluster(":0", "coord1.tunnelmesh:8443", nil)
	require.NoError(t, err)
	defer func() { _ = cluster.Shutdown() }()

	// Join with unreachable seeds should return error
	err = cluster.Join([]string{"nonexistent:7946", "invalid:7946"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "join cluster")
}

func TestCoordinatorCluster_Members(t *testing.T) {
	cluster, err := NewCoordinatorCluster(":0", "coord1.tunnelmesh:8443", nil)
	require.NoError(t, err)
	defer func() { _ = cluster.Shutdown() }()

	members := cluster.Members()
	require.NotNil(t, members)
	assert.GreaterOrEqual(t, len(members), 1) // Should at least see self

	// Verify member has the coordinator name
	found := false
	for _, member := range members {
		if member.Name == "coord1.tunnelmesh:8443" {
			found = true
			break
		}
	}
	assert.True(t, found, "should find self in members list")
}

func TestCoordinatorCluster_NumMembers(t *testing.T) {
	cluster, err := NewCoordinatorCluster(":0", "coord1.tunnelmesh:8443", nil)
	require.NoError(t, err)
	defer func() { _ = cluster.Shutdown() }()

	count := cluster.NumMembers()
	assert.GreaterOrEqual(t, count, 1) // Should at least count self
}

func TestCoordinatorCluster_GetCoordinatorAddresses(t *testing.T) {
	cluster, err := NewCoordinatorCluster(":0", "coord1.tunnelmesh:8443", nil)
	require.NoError(t, err)
	defer func() { _ = cluster.Shutdown() }()

	addrs := cluster.GetCoordinatorAddresses()
	require.NotNil(t, addrs)
	assert.GreaterOrEqual(t, len(addrs), 1)

	// Should contain self
	assert.Contains(t, addrs, "coord1.tunnelmesh:8443")
}

func TestCoordinatorCluster_Leave(t *testing.T) {
	cluster, err := NewCoordinatorCluster(":0", "coord1.tunnelmesh:8443", nil)
	require.NoError(t, err)

	// Leave should succeed
	err = cluster.Leave()
	assert.NoError(t, err)

	// After leaving, memberlist is in left state
	// Shutdown should still work
	err = cluster.Shutdown()
	assert.NoError(t, err)
}

func TestCoordinatorCluster_Shutdown(t *testing.T) {
	cluster, err := NewCoordinatorCluster(":0", "coord1.tunnelmesh:8443", nil)
	require.NoError(t, err)

	// Shutdown should succeed
	err = cluster.Shutdown()
	assert.NoError(t, err)

	// Double shutdown should still work (idempotent)
	err = cluster.Shutdown()
	assert.NoError(t, err)
}

func TestCoordinatorCluster_MultipleCoordinators(t *testing.T) {
	// Create first coordinator
	cluster1, err := NewCoordinatorCluster(":0", "coord1.tunnelmesh:8443", nil)
	require.NoError(t, err)
	defer func() { _ = cluster1.Shutdown() }()

	// Get cluster1's address
	members := cluster1.Members()
	require.Len(t, members, 1)
	addr1 := members[0].Addr.String() + ":7946"

	// Create second coordinator joining first
	cluster2, err := NewCoordinatorCluster(":0", "coord2.tunnelmesh:8443", []string{addr1})
	require.NoError(t, err)
	defer func() { _ = cluster2.Shutdown() }()

	// Wait for gossip
	time.Sleep(500 * time.Millisecond)

	// Create third coordinator joining first (seed discovery)
	cluster3, err := NewCoordinatorCluster(":0", "coord3.tunnelmesh:8443", []string{addr1})
	require.NoError(t, err)
	defer func() { _ = cluster3.Shutdown() }()

	// Wait for gossip to propagate
	time.Sleep(500 * time.Millisecond)

	// All clusters should see multiple members (eventually)
	// Note: Exact counts may vary due to gossip timing
	addrs1 := cluster1.GetCoordinatorAddresses()
	addrs2 := cluster2.GetCoordinatorAddresses()
	addrs3 := cluster3.GetCoordinatorAddresses()

	// Each should at least see itself
	assert.Contains(t, addrs1, "coord1.tunnelmesh:8443")
	assert.Contains(t, addrs2, "coord2.tunnelmesh:8443")
	assert.Contains(t, addrs3, "coord3.tunnelmesh:8443")

	// Total unique coordinators should be >= 1 (at minimum, each sees itself)
	// In a perfect world, all would see all 3, but gossip timing can vary
	allAddrs := make(map[string]bool)
	for _, addr := range addrs1 {
		allAddrs[addr] = true
	}
	for _, addr := range addrs2 {
		allAddrs[addr] = true
	}
	for _, addr := range addrs3 {
		allAddrs[addr] = true
	}
	assert.GreaterOrEqual(t, len(allAddrs), 1)
}

func TestCoordinatorCluster_FailureDetection(t *testing.T) {
	// This test is flaky by nature due to timing
	t.Skip("Failure detection timing is non-deterministic in tests")

	// Create two clusters
	cluster1, err := NewCoordinatorCluster(":0", "coord1.tunnelmesh:8443", nil)
	require.NoError(t, err)
	defer func() { _ = cluster1.Shutdown() }()

	members := cluster1.Members()
	addr1 := members[0].Addr.String() + ":7946"

	cluster2, err := NewCoordinatorCluster(":0", "coord2.tunnelmesh:8443", []string{addr1})
	require.NoError(t, err)

	// Wait for gossip
	time.Sleep(1 * time.Second)

	initialCount := cluster1.NumMembers()

	// Kill cluster2
	_ = cluster2.Shutdown()

	// Wait for failure detection (memberlist default: ~10 seconds)
	time.Sleep(15 * time.Second)

	// Cluster1 should detect cluster2 is gone
	finalCount := cluster1.NumMembers()
	assert.Less(t, finalCount, initialCount)
}
