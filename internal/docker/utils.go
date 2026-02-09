package docker

import (
	"strconv"
	"strings"
	"time"
)

// parseDockerTimestamp converts Unix timestamp to time.Time.
func parseDockerTimestamp(timestamp int64) time.Time {
	if timestamp == 0 {
		return time.Time{}
	}
	return time.Unix(timestamp, 0)
}

// parseDockerTime parses Docker's RFC3339 time format.
func parseDockerTime(timeStr string) time.Time {
	if timeStr == "" || timeStr == "0001-01-01T00:00:00Z" {
		return time.Time{}
	}

	t, err := time.Parse(time.RFC3339Nano, timeStr)
	if err != nil {
		// Try without nano precision
		t, err = time.Parse(time.RFC3339, timeStr)
		if err != nil {
			return time.Time{}
		}
	}

	return t
}

// parsePortProto parses "8080/tcp" into port and protocol.
func parsePortProto(portProto string) (uint16, string) {
	parts := strings.Split(portProto, "/")
	if len(parts) != 2 {
		return 0, "tcp"
	}

	port, err := strconv.ParseUint(parts[0], 10, 16)
	if err != nil {
		return 0, "tcp"
	}

	return uint16(port), parts[1]
}

// parseHostPort parses host port string to uint16.
func parseHostPort(portStr string) uint16 {
	if portStr == "" {
		return 0
	}

	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return 0
	}

	return uint16(port)
}
