// Package mesh provides constants for the TunnelMesh network configuration.
package mesh

const (
	// Domain suffixes - canonical and aliases
	DomainSuffix = ".tunnelmesh" // Canonical domain suffix
	AliasTM      = ".tm"         // Short alias
	AliasMesh    = ".mesh"       // Alternative alias

	// Network configuration
	CIDR = "172.30.0.0/16" // Mesh network CIDR - all peers get IPs from this range
)

// AllSuffixes returns all supported domain suffixes (canonical first).
func AllSuffixes() []string {
	return []string{DomainSuffix, AliasTM, AliasMesh}
}

// IsValidSuffix returns true if the suffix is a recognized mesh domain suffix.
func IsValidSuffix(suffix string) bool {
	return suffix == DomainSuffix || suffix == AliasTM || suffix == AliasMesh
}
