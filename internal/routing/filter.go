// Package routing handles packet routing for the mesh network.
package routing

import (
	"encoding/binary"
	"sync"
	"sync/atomic"
	"time"
)

// RuleSource identifies where a filter rule originated from.
type RuleSource int

const (
	// SourceCoordinator indicates rules pushed from the coordinator (global).
	SourceCoordinator RuleSource = iota
	// SourcePeerConfig indicates rules from the local peer config file.
	SourcePeerConfig
	// SourceTemporary indicates rules added via CLI or admin panel.
	SourceTemporary
	// SourceService indicates auto-generated rules for coordinator services.
	// These cannot be removed via CLI or admin panel.
	SourceService
)

// String returns a human-readable name for the rule source.
func (s RuleSource) String() string {
	switch s {
	case SourceCoordinator:
		return "coordinator"
	case SourcePeerConfig:
		return "config"
	case SourceTemporary:
		return "temporary"
	case SourceService:
		return "service"
	default:
		return "unknown"
	}
}

// FilterAction represents whether to allow or deny traffic.
type FilterAction uint8

const (
	// ActionAllow permits the packet.
	ActionAllow FilterAction = iota
	// ActionDeny drops the packet.
	ActionDeny
)

// String returns a human-readable name for the action.
func (a FilterAction) String() string {
	if a == ActionAllow {
		return "allow"
	}
	return "deny"
}

// ParseFilterAction converts a string to FilterAction.
func ParseFilterAction(s string) FilterAction {
	if s == "allow" {
		return ActionAllow
	}
	return ActionDeny
}

// ProtocolFromString converts a protocol name to its number.
func ProtocolFromString(s string) uint8 {
	switch s {
	case "tcp", "TCP":
		return ProtoTCP
	case "udp", "UDP":
		return ProtoUDP
	default:
		return 0
	}
}

// ProtocolToString converts a protocol number to its name.
func ProtocolToString(proto uint8) string {
	switch proto {
	case ProtoTCP:
		return "tcp"
	case ProtoUDP:
		return "udp"
	default:
		return "other"
	}
}

// FilterRule represents a single packet filter rule.
type FilterRule struct {
	Port       uint16       // Port number (1-65535)
	Protocol   uint8        // 6=TCP, 17=UDP
	Action     FilterAction // Allow or deny
	Expires    int64        // Unix timestamp, 0=permanent
	SourcePeer string       // Source peer name (empty = any peer)
}

// IsExpired returns true if the rule has expired.
func (r FilterRule) IsExpired() bool {
	return r.Expires > 0 && time.Now().Unix() > r.Expires
}

// FilterRuleKey is used as map key for O(1) lookup.
// SourcePeer is included for peer-specific rules; empty means any peer (global rule).
type FilterRuleKey struct {
	Port       uint16
	Protocol   uint8
	SourcePeer string // Empty = any peer (global rule)
}

// FilterRuleWithSource combines a rule with its source for display.
type FilterRuleWithSource struct {
	Rule   FilterRule
	Source RuleSource
}

// ruleMap is the type used for atomic pointer storage.
type ruleMap map[FilterRuleKey]FilterRule

// PacketFilter filters incoming packets based on port and protocol.
// Uses a 4-layer rule system with copy-on-write for lock-free reads.
//
// Rule precedence (most restrictive wins):
//   - If ANY layer denies, the packet is denied
//   - Allow only wins if no layer denies
type PacketFilter struct {
	// Separate maps for each layer - enables clean replacement
	coordinator atomic.Pointer[ruleMap] // Global rules from server config
	peerConfig  atomic.Pointer[ruleMap] // Local rules from peer.yaml
	temporary   atomic.Pointer[ruleMap] // CLI / admin panel (runtime)
	service     atomic.Pointer[ruleMap] // Auto-generated for coordinator services (read-only)

	mu          sync.Mutex // Serializes writes
	defaultDeny bool       // If true, deny by default (allowlist mode)
}

// NewPacketFilter creates a new packet filter.
// If defaultDeny is true, all ports are blocked unless explicitly allowed.
func NewPacketFilter(defaultDeny bool) *PacketFilter {
	f := &PacketFilter{
		defaultDeny: defaultDeny,
	}
	// Initialize empty maps
	emptyCoord := make(ruleMap)
	emptyConfig := make(ruleMap)
	emptyTemp := make(ruleMap)
	emptyService := make(ruleMap)
	f.coordinator.Store(&emptyCoord)
	f.peerConfig.Store(&emptyConfig)
	f.temporary.Store(&emptyTemp)
	f.service.Store(&emptyService)
	return f
}

// SetCoordinatorRules replaces all coordinator-level rules.
// These are global rules pushed from the server config.
func (f *PacketFilter) SetCoordinatorRules(rules []FilterRule) {
	f.mu.Lock()
	defer f.mu.Unlock()

	newMap := make(ruleMap, len(rules))
	for _, r := range rules {
		key := FilterRuleKey{Port: r.Port, Protocol: r.Protocol, SourcePeer: r.SourcePeer}
		newMap[key] = r
	}
	f.coordinator.Store(&newMap)
}

// SetPeerConfigRules replaces all peer config-level rules.
// These are local rules from the peer's config file.
func (f *PacketFilter) SetPeerConfigRules(rules []FilterRule) {
	f.mu.Lock()
	defer f.mu.Unlock()

	newMap := make(ruleMap, len(rules))
	for _, r := range rules {
		key := FilterRuleKey{Port: r.Port, Protocol: r.Protocol, SourcePeer: r.SourcePeer}
		newMap[key] = r
	}
	f.peerConfig.Store(&newMap)
}

// SetServiceRules replaces all service-level rules.
// These are auto-generated rules for coordinator services (admin, metrics, etc).
// Service rules cannot be removed via CLI or admin panel.
func (f *PacketFilter) SetServiceRules(rules []FilterRule) {
	f.mu.Lock()
	defer f.mu.Unlock()

	newMap := make(ruleMap, len(rules))
	for _, r := range rules {
		key := FilterRuleKey{Port: r.Port, Protocol: r.Protocol, SourcePeer: r.SourcePeer}
		newMap[key] = r
	}
	f.service.Store(&newMap)
}

// AddTemporaryRule adds a rule to the temporary layer.
// Temporary rules are added via CLI or admin panel and persist across
// coordinator restarts when S3 is enabled.
func (f *PacketFilter) AddTemporaryRule(rule FilterRule) {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := FilterRuleKey{Port: rule.Port, Protocol: rule.Protocol, SourcePeer: rule.SourcePeer}

	// Copy-on-write
	oldMap := f.temporary.Load()
	newMap := make(ruleMap, len(*oldMap)+1)
	for k, v := range *oldMap {
		newMap[k] = v
	}
	newMap[key] = rule
	f.temporary.Store(&newMap)
}

// RemoveTemporaryRule removes a global rule from the temporary layer.
// Use RemoveTemporaryRuleForPeer for peer-specific rules.
func (f *PacketFilter) RemoveTemporaryRule(port uint16, protocol uint8) {
	f.RemoveTemporaryRuleForPeer(port, protocol, "")
}

// RemoveTemporaryRuleForPeer removes a rule from the temporary layer for a specific peer.
// Pass empty string for sourcePeer to remove global rules.
func (f *PacketFilter) RemoveTemporaryRuleForPeer(port uint16, protocol uint8, sourcePeer string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := FilterRuleKey{Port: port, Protocol: protocol, SourcePeer: sourcePeer}

	oldMap := f.temporary.Load()
	if _, exists := (*oldMap)[key]; !exists {
		return // Nothing to remove
	}

	// Copy-on-write
	newMap := make(ruleMap, len(*oldMap))
	for k, v := range *oldMap {
		if k != key {
			newMap[k] = v
		}
	}
	f.temporary.Store(&newMap)
}

// ClearTemporaryRules removes all temporary rules.
func (f *PacketFilter) ClearTemporaryRules() {
	f.mu.Lock()
	defer f.mu.Unlock()

	emptyMap := make(ruleMap)
	f.temporary.Store(&emptyMap)
}

// effectiveAction determines the action for a port/protocol/peer combination.
// Returns (action, matched). If not matched, returns (deny/allow based on default, false).
// The sourcePeer parameter allows for peer-specific rule matching (empty = any peer).
func (f *PacketFilter) effectiveAction(port uint16, protocol uint8, sourcePeer string) (FilterAction, bool) {
	// Load all layers once (lock-free)
	coordRules := f.coordinator.Load()
	configRules := f.peerConfig.Load()
	tempRules := f.temporary.Load()
	serviceRules := f.service.Load()

	layers := []*ruleMap{coordRules, configRules, tempRules, serviceRules}

	// Check if ANY layer denies - deny wins (most restrictive)
	// Check peer-specific rules first, then global rules
	for _, layer := range layers {
		// First check peer-specific deny
		if sourcePeer != "" {
			peerKey := FilterRuleKey{Port: port, Protocol: protocol, SourcePeer: sourcePeer}
			if rule, ok := (*layer)[peerKey]; ok {
				if !rule.IsExpired() && rule.Action == ActionDeny {
					return ActionDeny, true
				}
			}
		}
		// Then check global deny
		globalKey := FilterRuleKey{Port: port, Protocol: protocol, SourcePeer: ""}
		if rule, ok := (*layer)[globalKey]; ok {
			if !rule.IsExpired() && rule.Action == ActionDeny {
				return ActionDeny, true
			}
		}
	}

	// Check if any layer explicitly allows (reverse order for precedence display)
	// Check peer-specific allow first, then global allow
	for _, layer := range []*ruleMap{serviceRules, tempRules, configRules, coordRules} {
		// First check peer-specific allow
		if sourcePeer != "" {
			peerKey := FilterRuleKey{Port: port, Protocol: protocol, SourcePeer: sourcePeer}
			if rule, ok := (*layer)[peerKey]; ok {
				if !rule.IsExpired() && rule.Action == ActionAllow {
					return ActionAllow, true
				}
			}
		}
		// Then check global allow
		globalKey := FilterRuleKey{Port: port, Protocol: protocol, SourcePeer: ""}
		if rule, ok := (*layer)[globalKey]; ok {
			if !rule.IsExpired() && rule.Action == ActionAllow {
				return ActionAllow, true
			}
		}
	}

	// No matching rule - apply default policy
	if f.defaultDeny {
		return ActionDeny, false
	}
	return ActionAllow, false
}

// FilterResult contains the result of a filter check.
type FilterResult struct {
	Drop       bool   // Whether to drop the packet
	Protocol   uint8  // Protocol that was filtered (6=TCP, 17=UDP, 0=not filtered)
	SourcePeer string // Source peer that was checked (for metrics)
}

// ShouldDrop returns true if the packet should be dropped.
// This is the main filter check called on the hot path.
// Does not consider peer-specific rules - use ShouldDropFromPeer for that.
func (f *PacketFilter) ShouldDrop(packet []byte) bool {
	result := f.CheckPacket(packet)
	return result.Drop
}

// ShouldDropFromPeer returns true if the packet from the given peer should be dropped.
func (f *PacketFilter) ShouldDropFromPeer(packet []byte, sourcePeer string) bool {
	result := f.CheckPacketFromPeer(packet, sourcePeer)
	return result.Drop
}

// CheckPacket checks a packet and returns detailed filter result.
// Use this when you need to know what protocol was filtered.
// Does not consider peer-specific rules - use CheckPacketFromPeer for that.
func (f *PacketFilter) CheckPacket(packet []byte) FilterResult {
	return f.CheckPacketFromPeer(packet, "")
}

// CheckPacketFromPeer checks a packet from a specific peer and returns detailed filter result.
// The sourcePeer parameter enables peer-specific rule matching.
// Pass empty string for sourcePeer to only match global rules.
func (f *PacketFilter) CheckPacketFromPeer(packet []byte, sourcePeer string) FilterResult {
	// Parse IP header to get protocol
	if len(packet) < 20 {
		return FilterResult{Drop: false, Protocol: 0, SourcePeer: sourcePeer}
	}

	protocol := packet[9]

	// Only filter TCP (6) and UDP (17)
	if protocol != ProtoTCP && protocol != ProtoUDP {
		return FilterResult{Drop: false, Protocol: 0, SourcePeer: sourcePeer}
	}

	// Get IP header length
	ihl := int(packet[0]&0x0F) * 4
	if len(packet) < ihl+4 {
		return FilterResult{Drop: false, Protocol: 0, SourcePeer: sourcePeer}
	}

	// For TCP, only filter NEW connections (SYN without ACK).
	// This allows response packets (SYN-ACK, ACK, etc.) to pass through,
	// enabling outgoing connections to work even in allowlist mode.
	if protocol == ProtoTCP {
		// TCP header must be at least 20 bytes, flags are at offset 13
		if len(packet) < ihl+14 {
			return FilterResult{Drop: false, Protocol: 0, SourcePeer: sourcePeer}
		}
		tcpFlags := packet[ihl+13]
		// SYN=0x02, ACK=0x10. Only filter pure SYN (new connection attempts)
		isSYN := (tcpFlags & 0x02) != 0
		isACK := (tcpFlags & 0x10) != 0
		if !isSYN || isACK {
			// Not a new connection attempt - allow through
			return FilterResult{Drop: false, Protocol: protocol, SourcePeer: sourcePeer}
		}
	}

	// Extract destination port from TCP/UDP header (bytes 2-3)
	dstPort := binary.BigEndian.Uint16(packet[ihl+2 : ihl+4])

	// Check filter rules with peer context
	action, _ := f.effectiveAction(dstPort, protocol, sourcePeer)
	return FilterResult{Drop: action == ActionDeny, Protocol: protocol, SourcePeer: sourcePeer}
}

// ListRules returns all rules from all layers with their sources.
// Rules are returned in order: coordinator, peer config, temporary, service.
func (f *PacketFilter) ListRules() []FilterRuleWithSource {
	coordRules := f.coordinator.Load()
	configRules := f.peerConfig.Load()
	tempRules := f.temporary.Load()
	serviceRules := f.service.Load()

	var result []FilterRuleWithSource

	for _, r := range *coordRules {
		if !r.IsExpired() {
			result = append(result, FilterRuleWithSource{Rule: r, Source: SourceCoordinator})
		}
	}
	for _, r := range *configRules {
		if !r.IsExpired() {
			result = append(result, FilterRuleWithSource{Rule: r, Source: SourcePeerConfig})
		}
	}
	for _, r := range *tempRules {
		if !r.IsExpired() {
			result = append(result, FilterRuleWithSource{Rule: r, Source: SourceTemporary})
		}
	}
	for _, r := range *serviceRules {
		if !r.IsExpired() {
			result = append(result, FilterRuleWithSource{Rule: r, Source: SourceService})
		}
	}

	return result
}

// RuleCount returns the number of non-expired rules across all layers.
func (f *PacketFilter) RuleCount() int {
	count := 0
	for _, r := range *f.coordinator.Load() {
		if !r.IsExpired() {
			count++
		}
	}
	for _, r := range *f.peerConfig.Load() {
		if !r.IsExpired() {
			count++
		}
	}
	for _, r := range *f.temporary.Load() {
		if !r.IsExpired() {
			count++
		}
	}
	for _, r := range *f.service.Load() {
		if !r.IsExpired() {
			count++
		}
	}
	return count
}

// RuleCountBySource returns the number of non-expired rules for each source.
type RuleCounts struct {
	Coordinator int
	PeerConfig  int
	Temporary   int
	Service     int
}

// RuleCountBySource returns the rule counts broken down by source.
func (f *PacketFilter) RuleCountBySource() RuleCounts {
	var counts RuleCounts

	for _, r := range *f.coordinator.Load() {
		if !r.IsExpired() {
			counts.Coordinator++
		}
	}
	for _, r := range *f.peerConfig.Load() {
		if !r.IsExpired() {
			counts.PeerConfig++
		}
	}
	for _, r := range *f.temporary.Load() {
		if !r.IsExpired() {
			counts.Temporary++
		}
	}
	for _, r := range *f.service.Load() {
		if !r.IsExpired() {
			counts.Service++
		}
	}

	return counts
}

// IsDefaultDeny returns true if the filter is in allowlist mode (deny by default).
func (f *PacketFilter) IsDefaultDeny() bool {
	return f.defaultDeny
}
