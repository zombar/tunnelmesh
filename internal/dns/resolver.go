// Package dns implements a local DNS resolver for the mesh network.
package dns

import (
	"net"
	"strings"
	"sync"

	"github.com/miekg/dns"
	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/mesh"
)

// Resolver is a local DNS server that resolves mesh hostnames.
type Resolver struct {
	ttl         uint32            // TTL for DNS responses
	records     map[string]string // hostname (without suffix) -> IP
	coordMeshIP string            // Coordinator's mesh IP for "this.tunnelmesh" resolution
	mu          sync.RWMutex
	server      *dns.Server
	shutdown    chan struct{}
}

// NewResolver creates a new DNS resolver.
// The suffix parameter is ignored - all supported suffixes (.tunnelmesh, .tm, .mesh) are handled.
func NewResolver(_ string, ttl int) *Resolver {
	return &Resolver{
		ttl:      uint32(ttl),
		records:  make(map[string]string),
		shutdown: make(chan struct{}),
	}
}

// AddRecord adds or updates a DNS record.
func (r *Resolver) AddRecord(hostname, ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Store without suffix
	hostname = r.stripSuffix(hostname)
	r.records[hostname] = ip

	log.Debug().
		Str("hostname", hostname).
		Str("ip", ip).
		Msg("DNS record added")
}

// RemoveRecord removes a DNS record.
func (r *Resolver) RemoveRecord(hostname string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	hostname = r.stripSuffix(hostname)
	delete(r.records, hostname)

	log.Debug().
		Str("hostname", hostname).
		Msg("DNS record removed")
}

// UpdateRecords replaces all records with a new set.
func (r *Resolver) UpdateRecords(records map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.records = make(map[string]string, len(records))
	for hostname, ip := range records {
		hostname = r.stripSuffix(hostname)
		r.records[hostname] = ip
		log.Debug().
			Str("hostname", hostname).
			Str("ip", ip).
			Msg("DNS record synced")
	}

	log.Debug().
		Int("count", len(records)).
		Msg("DNS records updated")
}

// SetCoordMeshIP sets the coordinator's mesh IP for "this.tunnelmesh" resolution.
func (r *Resolver) SetCoordMeshIP(ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.coordMeshIP = ip
	log.Debug().Str("ip", ip).Msg("coordinator mesh IP set for 'this' DNS entry")
}

// Resolve looks up a hostname and returns its IP.
func (r *Resolver) Resolve(hostname string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	hostname = r.stripSuffix(hostname)

	// Special case: "this" resolves to coordinator's mesh IP (admin access)
	if hostname == "this" {
		if r.coordMeshIP != "" {
			return r.coordMeshIP, true
		}
		return "", false
	}

	ip, ok := r.records[hostname]
	return ip, ok
}

// ListRecords returns a copy of all records.
func (r *Resolver) ListRecords() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]string, len(r.records))
	for k, v := range r.records {
		result[k] = v
	}
	return result
}

// ListenAndServe starts the DNS server.
func (r *Resolver) ListenAndServe(addr string) error {
	r.server = &dns.Server{
		Addr: addr,
		Net:  "udp",
	}

	// Register handlers for all supported domain suffixes
	for _, suffix := range mesh.AllSuffixes() {
		zone := strings.TrimPrefix(suffix, ".")
		dns.HandleFunc(zone, r.handleDNS)
	}

	log.Info().
		Str("addr", addr).
		Strs("suffixes", mesh.AllSuffixes()).
		Msg("starting DNS server")

	return r.server.ListenAndServe()
}

// Shutdown stops the DNS server.
func (r *Resolver) Shutdown() error {
	if r.server != nil {
		return r.server.Shutdown()
	}
	return nil
}

func (r *Resolver) handleDNS(w dns.ResponseWriter, req *dns.Msg) {
	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.Authoritative = true

	for _, q := range req.Question {
		if q.Qtype != dns.TypeA && q.Qtype != dns.TypeAAAA {
			continue
		}

		// Extract hostname from FQDN
		hostname := strings.TrimSuffix(q.Name, ".")
		hostname = r.stripSuffix(hostname)

		ip, ok := r.Resolve(hostname)
		if !ok {
			resp.Rcode = dns.RcodeNameError
			continue
		}

		if q.Qtype == dns.TypeA {
			rr := &dns.A{
				Hdr: dns.RR_Header{
					Name:   q.Name,
					Rrtype: dns.TypeA,
					Class:  dns.ClassINET,
					Ttl:    r.ttl,
				},
				A: net.ParseIP(ip),
			}
			resp.Answer = append(resp.Answer, rr)
		}
	}

	if len(req.Question) > 0 && len(resp.Answer) == 0 {
		resp.Rcode = dns.RcodeNameError
	}

	_ = w.WriteMsg(resp)
}

func (r *Resolver) stripSuffix(hostname string) string {
	hostname = strings.TrimSuffix(hostname, ".")
	// Try all supported suffixes
	for _, suffix := range mesh.AllSuffixes() {
		if strings.HasSuffix(hostname, suffix) {
			return strings.TrimSuffix(hostname, suffix)
		}
	}
	return hostname
}
