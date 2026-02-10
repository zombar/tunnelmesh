package dns

import (
	"fmt"
	"net"
	"sync"

	"github.com/miekg/dns"
	"github.com/rs/zerolog/log"
)

// SRVRegistry manages SRV records for coordinator discovery.
// Coordinators publish themselves via PublishCoordinator().
// Peers discover coordinators via DiscoverCoordinators().
type SRVRegistry struct {
	mu      sync.RWMutex
	records map[string]*srvRecord // hostname -> SRV record
}

type srvRecord struct {
	Target   string
	Port     uint16
	Priority uint16
	Weight   uint16
}

var (
	// Global SRV registry for coordinator discovery
	globalSRVRegistry = &SRVRegistry{
		records: make(map[string]*srvRecord),
	}
)

// PublishCoordinator publishes this coordinator as an SRV record.
// name: Coordinator hostname (e.g., "coord1.tunnelmesh")
// addr: Listen address (e.g., ":8443" or "0.0.0.0:8443")
func PublishCoordinator(name, addr string) error {
	// Parse port from address
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		// If no port specified, try parsing as just port
		portStr = addr
		if portStr[0] == ':' {
			portStr = portStr[1:]
		}
	}

	port, err := net.LookupPort("tcp", portStr)
	if err != nil {
		return fmt.Errorf("invalid port in address %q: %w", addr, err)
	}

	record := &srvRecord{
		Target:   name,
		Port:     uint16(port),
		Priority: 10,
		Weight:   10,
	}

	globalSRVRegistry.mu.Lock()
	globalSRVRegistry.records[name] = record
	globalSRVRegistry.mu.Unlock()

	log.Info().
		Str("name", name).
		Uint16("port", uint16(port)).
		Msg("published coordinator SRV record")

	return nil
}

// UnpublishCoordinator removes a coordinator's SRV record.
func UnpublishCoordinator(name string) {
	globalSRVRegistry.mu.Lock()
	delete(globalSRVRegistry.records, name)
	globalSRVRegistry.mu.Unlock()

	log.Info().Str("name", name).Msg("unpublished coordinator SRV record")
}

// DiscoverCoordinators returns the list of coordinator addresses via SRV lookup.
// This performs a DNS SRV query for _coord._tcp.tunnelmesh
// Returns addresses in the format "hostname:port"
func DiscoverCoordinators(domain string) ([]string, error) {
	if domain == "" {
		domain = "tunnelmesh"
	}

	// Query: _coord._tcp.tunnelmesh
	query := fmt.Sprintf("_coord._tcp.%s.", domain)

	globalSRVRegistry.mu.RLock()
	defer globalSRVRegistry.mu.RUnlock()

	if len(globalSRVRegistry.records) == 0 {
		return nil, fmt.Errorf("no coordinator SRV records found")
	}

	addrs := make([]string, 0, len(globalSRVRegistry.records))
	for _, record := range globalSRVRegistry.records {
		addr := fmt.Sprintf("%s:%d", record.Target, record.Port)
		addrs = append(addrs, addr)
	}

	log.Debug().
		Str("query", query).
		Strs("coordinators", addrs).
		Msg("discovered coordinators via SRV")

	return addrs, nil
}

// HandleSRVQuery handles DNS SRV queries for coordinator discovery.
// This is called by the internal DNS server when it receives an SRV query.
// Returns SRV records for _coord._tcp.tunnelmesh queries.
func HandleSRVQuery(qname string) []dns.RR {
	// Only handle _coord._tcp.tunnelmesh queries
	if qname != "_coord._tcp.tunnelmesh." {
		return nil
	}

	globalSRVRegistry.mu.RLock()
	defer globalSRVRegistry.mu.RUnlock()

	if len(globalSRVRegistry.records) == 0 {
		return nil
	}

	records := make([]dns.RR, 0, len(globalSRVRegistry.records))
	for _, record := range globalSRVRegistry.records {
		srv := &dns.SRV{
			Hdr: dns.RR_Header{
				Name:   qname,
				Rrtype: dns.TypeSRV,
				Class:  dns.ClassINET,
				Ttl:    300, // 5 minutes
			},
			Priority: record.Priority,
			Weight:   record.Weight,
			Port:     record.Port,
			Target:   record.Target,
		}
		records = append(records, srv)
	}

	log.Debug().
		Str("query", qname).
		Int("count", len(records)).
		Msg("returning SRV records for coordinator discovery")

	return records
}

// GetCoordinatorCount returns the number of published coordinators.
// Useful for testing and monitoring.
func GetCoordinatorCount() int {
	globalSRVRegistry.mu.RLock()
	defer globalSRVRegistry.mu.RUnlock()
	return len(globalSRVRegistry.records)
}
