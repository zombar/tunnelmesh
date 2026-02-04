package portmap

import (
	"net"
	"time"
)

// Mapping represents an active port mapping on a NAT gateway.
type Mapping struct {
	// Protocol is the transport protocol (UDP or TCP).
	Protocol Protocol

	// InternalPort is the local port that was mapped.
	InternalPort int

	// ExternalPort is the port on the gateway's external interface.
	ExternalPort int

	// ExternalIP is the gateway's external IP address.
	ExternalIP net.IP

	// Gateway is the IP address of the gateway device.
	Gateway net.IP

	// ClientType identifies the protocol used to create the mapping.
	// One of: "pcp", "natpmp", "upnp"
	ClientType string

	// Lifetime is the duration the mapping is valid for.
	Lifetime time.Duration

	// CreatedAt is when the mapping was created.
	CreatedAt time.Time

	// ExpiresAt is when the mapping will expire if not refreshed.
	ExpiresAt time.Time
}

// IsExpired returns true if the mapping has expired.
func (m *Mapping) IsExpired() bool {
	return time.Now().After(m.ExpiresAt)
}

// TimeToExpiry returns the duration until the mapping expires.
// Returns 0 if already expired.
func (m *Mapping) TimeToExpiry() time.Duration {
	remaining := time.Until(m.ExpiresAt)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// ShouldRefresh returns true if the mapping should be refreshed.
// This is typically when less than 30 seconds remain before expiry.
func (m *Mapping) ShouldRefresh() bool {
	return m.TimeToExpiry() < 30*time.Second
}

// ExternalAddr returns the external address as "ip:port" string.
func (m *Mapping) ExternalAddr() string {
	return net.JoinHostPort(m.ExternalIP.String(), itoa(m.ExternalPort))
}

// itoa converts an int to a string without importing strconv.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	if i < 0 {
		return "-" + itoa(-i)
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}
