// Package bytesize provides utilities for parsing and formatting byte sizes.
package bytesize

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Common byte size units.
const (
	B  int64 = 1
	KB int64 = 1024
	MB int64 = 1024 * KB
	GB int64 = 1024 * MB
	TB int64 = 1024 * GB
)

// Network rate units (bits per second, using SI units).
const (
	Kbps int64 = 1000 / 8        // kilobits per second in bytes
	Mbps int64 = 1000 * 1000 / 8 // megabits per second in bytes
	Gbps int64 = 1000 * 1000 * 1000 / 8
)

var (
	// sizePattern matches size strings like "100MB", "1.5 GB", "1024"
	sizePattern = regexp.MustCompile(`^\s*(\d+(?:\.\d+)?)\s*([a-zA-Z]*)\s*$`)

	// ratePattern matches rate strings like "10mbps", "100KB/s"
	ratePattern = regexp.MustCompile(`^\s*(\d+(?:\.\d+)?)\s*([a-zA-Z/]+)\s*$`)
)

// Parse parses a byte size string like "100MB", "1.5GB", or "1024" into bytes.
// Supported units: B, KB, MB, GB, TB (case-insensitive).
// If no unit is specified, bytes are assumed.
func Parse(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size string")
	}

	matches := sizePattern.FindStringSubmatch(s)
	if matches == nil {
		return 0, fmt.Errorf("invalid size format: %q", s)
	}

	value, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number: %q", matches[1])
	}

	if value < 0 {
		return 0, fmt.Errorf("negative size not allowed: %v", value)
	}

	unit := strings.ToUpper(matches[2])
	var multiplier int64

	switch unit {
	case "", "B":
		multiplier = B
	case "KB", "K", "KI": // Ki = Kubernetes-style binary kibibyte
		multiplier = KB
	case "MB", "M", "MI": // Mi = Kubernetes-style binary mebibyte
		multiplier = MB
	case "GB", "G", "GI": // Gi = Kubernetes-style binary gibibyte
		multiplier = GB
	case "TB", "T", "TI": // Ti = Kubernetes-style binary tebibyte
		multiplier = TB
	default:
		return 0, fmt.Errorf("unknown unit: %q", matches[2])
	}

	return int64(value * float64(multiplier)), nil
}

// MustParse is like Parse but panics on error.
func MustParse(s string) int64 {
	v, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}

// Format formats a byte count into a human-readable string.
func Format(bytes int64) string {
	if bytes == 0 {
		return "0 B"
	}

	units := []struct {
		threshold int64
		unit      string
	}{
		{TB, "TB"},
		{GB, "GB"},
		{MB, "MB"},
		{KB, "KB"},
	}

	for _, u := range units {
		if bytes >= u.threshold {
			return fmt.Sprintf("%.2f %s", float64(bytes)/float64(u.threshold), u.unit)
		}
	}

	return fmt.Sprintf("%d B", bytes)
}

// ParseRate parses a network rate string like "10mbps" or "100KB/s" into bytes per second.
// Supported formats:
//   - Bits: kbps, mbps, gbps (SI units, case-insensitive)
//   - Bytes: KB/s, MB/s, GB/s (binary units)
func ParseRate(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty rate string")
	}

	matches := ratePattern.FindStringSubmatch(s)
	if matches == nil {
		return 0, fmt.Errorf("invalid rate format: %q", s)
	}

	value, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number: %q", matches[1])
	}

	if value < 0 {
		return 0, fmt.Errorf("negative rate not allowed: %v", value)
	}

	unit := strings.ToLower(matches[2])
	var bytesPerSec int64

	switch unit {
	// Bits per second (SI units)
	case "bps":
		bytesPerSec = int64(value / 8)
	case "kbps":
		bytesPerSec = int64(value * float64(Kbps))
	case "mbps":
		bytesPerSec = int64(value * float64(Mbps))
	case "gbps":
		bytesPerSec = int64(value * float64(Gbps))

	// Bytes per second (binary units)
	case "b/s":
		bytesPerSec = int64(value)
	case "kb/s":
		bytesPerSec = int64(value * float64(KB))
	case "mb/s":
		bytesPerSec = int64(value * float64(MB))
	case "gb/s":
		bytesPerSec = int64(value * float64(GB))

	default:
		return 0, fmt.Errorf("unknown rate unit: %q", matches[2])
	}

	return bytesPerSec, nil
}

// MustParseRate is like ParseRate but panics on error.
func MustParseRate(s string) int64 {
	v, err := ParseRate(s)
	if err != nil {
		panic(err)
	}
	return v
}

// FormatRate formats bytes per second into a human-readable bit rate string.
func FormatRate(bytesPerSec int64) string {
	if bytesPerSec == 0 {
		return "0 bps"
	}

	bitsPerSec := bytesPerSec * 8

	units := []struct {
		threshold int64
		unit      string
	}{
		{1000 * 1000 * 1000, "Gbps"},
		{1000 * 1000, "Mbps"},
		{1000, "Kbps"},
	}

	for _, u := range units {
		if bitsPerSec >= u.threshold {
			return fmt.Sprintf("%.2f %s", float64(bitsPerSec)/float64(u.threshold), u.unit)
		}
	}

	return fmt.Sprintf("%d bps", bitsPerSec)
}

// Size is a byte size that can be unmarshaled from YAML as either
// a number (bytes) or a string with units ("10Gi", "500Mi", "1TB").
type Size int64

// UnmarshalYAML implements yaml.Unmarshaler for Size.
func (s *Size) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// First try as a string
	var str string
	if err := unmarshal(&str); err == nil {
		bytes, err := Parse(str)
		if err != nil {
			return fmt.Errorf("invalid size %q: %w", str, err)
		}
		*s = Size(bytes)
		return nil
	}

	// Try as an integer (bytes)
	var i int64
	if err := unmarshal(&i); err == nil {
		*s = Size(i)
		return nil
	}

	return fmt.Errorf("size must be a number or string with units (e.g., 10Gi, 500Mi)")
}

// Bytes returns the size in bytes.
func (s Size) Bytes() int64 {
	return int64(s)
}

// String returns a human-readable representation.
func (s Size) String() string {
	return Format(int64(s))
}
