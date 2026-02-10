package cluster

import (
	"fmt"
	"net"
	"time"

	"github.com/hashicorp/memberlist"
	"github.com/rs/zerolog/log"
)

// CoordinatorCluster wraps memberlist for coordinator discovery and clustering.
// Only admin peers (coordinators) join the memberlist cluster for auto-discovery.
// Regular peers discover coordinators via static config, SRV records, or RegisterResponse.
type CoordinatorCluster struct {
	ml           *memberlist.Memberlist
	bindAddr     string
	coordAddress string // This coordinator's listen address (for node metadata)
}

// NodeMeta stores coordinator metadata in memberlist.
// This allows peers to discover the coordinator's API address.
type NodeMeta struct {
	CoordAddress string // Coordinator listen address (e.g., "coord1.tunnelmesh:8443")
}

// NewCoordinatorCluster creates a new coordinator cluster using memberlist.
// bindAddr: Address for memberlist gossip (e.g., ":7946")
// coordAddress: This coordinator's API listen address (stored in node metadata)
// seeds: List of seed addresses to join (e.g., ["coord1.example.com:7946"])
func NewCoordinatorCluster(bindAddr, coordAddress string, seeds []string) (*CoordinatorCluster, error) {
	cfg := memberlist.DefaultLocalConfig()
	cfg.Name = coordAddress // Use coord address as node name for uniqueness
	cfg.BindAddr = "0.0.0.0"

	// Parse port from bindAddr
	_, port, err := net.SplitHostPort(bindAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid bind address %q: %w", bindAddr, err)
	}

	portNum, err := net.LookupPort("tcp", port)
	if err != nil {
		return nil, fmt.Errorf("invalid port %q: %w", port, err)
	}
	cfg.BindPort = portNum

	// Configure memberlist for LAN (low latency, high bandwidth)
	cfg.TCPTimeout = 10 * time.Second
	cfg.IndirectChecks = 3
	cfg.RetransmitMult = 4
	cfg.SuspicionMult = 4
	cfg.ProbeTimeout = 500 * time.Millisecond
	cfg.ProbeInterval = 1 * time.Second
	cfg.GossipInterval = 200 * time.Millisecond
	cfg.GossipNodes = 3

	// Disable memberlist's built-in logger (we use zerolog)
	cfg.LogOutput = &logAdapter{}

	ml, err := memberlist.Create(cfg)
	if err != nil {
		return nil, fmt.Errorf("create memberlist: %w", err)
	}

	cluster := &CoordinatorCluster{
		ml:           ml,
		bindAddr:     bindAddr,
		coordAddress: coordAddress,
	}

	// Join seed nodes if provided
	if len(seeds) > 0 {
		if err := cluster.Join(seeds); err != nil {
			log.Warn().Err(err).Strs("seeds", seeds).Msg("failed to join some seed nodes (will retry via gossip)")
		}
	}

	return cluster, nil
}

// Join attempts to join the cluster by contacting seed nodes.
// Returns error only if ALL seeds fail (partial success is OK).
func (c *CoordinatorCluster) Join(seeds []string) error {
	if len(seeds) == 0 {
		return nil
	}

	joined, err := c.ml.Join(seeds)
	if err != nil {
		return fmt.Errorf("join cluster: %w", err)
	}

	if joined == 0 {
		return fmt.Errorf("failed to join any seed nodes")
	}

	log.Info().Int("joined", joined).Int("total_seeds", len(seeds)).Msg("joined coordinator cluster")
	return nil
}

// Members returns the list of live coordinator nodes in the cluster.
func (c *CoordinatorCluster) Members() []*memberlist.Node {
	return c.ml.Members()
}

// NumMembers returns the number of live nodes in the cluster.
func (c *CoordinatorCluster) NumMembers() int {
	return c.ml.NumMembers()
}

// Leave gracefully leaves the cluster.
func (c *CoordinatorCluster) Leave() error {
	timeout := 5 * time.Second
	if err := c.ml.Leave(timeout); err != nil {
		return fmt.Errorf("leave cluster: %w", err)
	}
	return nil
}

// Shutdown shuts down the memberlist instance.
func (c *CoordinatorCluster) Shutdown() error {
	if err := c.ml.Shutdown(); err != nil {
		return fmt.Errorf("shutdown memberlist: %w", err)
	}
	return nil
}

// GetCoordinatorAddresses returns the list of coordinator API addresses from cluster members.
// This can be used to populate LiveCoordinators in RegisterResponse.
func (c *CoordinatorCluster) GetCoordinatorAddresses() []string {
	members := c.ml.Members()
	addrs := make([]string, 0, len(members))

	for _, node := range members {
		// Node name is the coordinator address (set in NewCoordinatorCluster)
		if node.Name != "" {
			addrs = append(addrs, node.Name)
		}
	}

	return addrs
}

// logAdapter adapts memberlist's log output to zerolog.
type logAdapter struct{}

func (l *logAdapter) Write(p []byte) (n int, err error) {
	log.Debug().Str("source", "memberlist").Msg(string(p))
	return len(p), nil
}
