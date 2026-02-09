package docker

import (
	"testing"
	"time"
)

func TestParseDockerTimestamp(t *testing.T) {
	tests := []struct {
		name      string
		timestamp int64
		want      time.Time
	}{
		{
			name:      "zero timestamp",
			timestamp: 0,
			want:      time.Time{},
		},
		{
			name:      "valid timestamp",
			timestamp: 1609459200, // 2021-01-01 00:00:00 UTC
			want:      time.Unix(1609459200, 0),
		},
		{
			name:      "negative timestamp",
			timestamp: -1,
			want:      time.Unix(-1, 0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDockerTimestamp(tt.timestamp)
			if !got.Equal(tt.want) {
				t.Errorf("parseDockerTimestamp() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseDockerTime(t *testing.T) {
	tests := []struct {
		name    string
		timeStr string
		want    time.Time
	}{
		{
			name:    "empty string",
			timeStr: "",
			want:    time.Time{},
		},
		{
			name:    "zero time string",
			timeStr: "0001-01-01T00:00:00Z",
			want:    time.Time{},
		},
		{
			name:    "RFC3339 format",
			timeStr: "2021-01-01T00:00:00Z",
			want:    time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "RFC3339Nano format",
			timeStr: "2021-01-01T00:00:00.123456789Z",
			want:    time.Date(2021, 1, 1, 0, 0, 0, 123456789, time.UTC),
		},
		{
			name:    "invalid format",
			timeStr: "invalid-time",
			want:    time.Time{},
		},
		{
			name:    "partial RFC3339",
			timeStr: "2021-01-01",
			want:    time.Time{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDockerTime(tt.timeStr)
			if !got.Equal(tt.want) {
				t.Errorf("parseDockerTime() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParsePortProto(t *testing.T) {
	tests := []struct {
		name      string
		portProto string
		wantPort  uint16
		wantProto string
	}{
		{
			name:      "tcp port",
			portProto: "8080/tcp",
			wantPort:  8080,
			wantProto: "tcp",
		},
		{
			name:      "udp port",
			portProto: "53/udp",
			wantPort:  53,
			wantProto: "udp",
		},
		{
			name:      "no slash",
			portProto: "8080",
			wantPort:  0,
			wantProto: "tcp",
		},
		{
			name:      "invalid port number",
			portProto: "abc/tcp",
			wantPort:  0,
			wantProto: "tcp",
		},
		{
			name:      "port too large",
			portProto: "99999/tcp",
			wantPort:  0,
			wantProto: "tcp",
		},
		{
			name:      "empty string",
			portProto: "",
			wantPort:  0,
			wantProto: "tcp",
		},
		{
			name:      "multiple slashes",
			portProto: "8080/tcp/extra",
			wantPort:  0,
			wantProto: "tcp",
		},
		{
			name:      "zero port",
			portProto: "0/tcp",
			wantPort:  0,
			wantProto: "tcp",
		},
		{
			name:      "max valid port",
			portProto: "65535/tcp",
			wantPort:  65535,
			wantProto: "tcp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPort, gotProto := parsePortProto(tt.portProto)
			if gotPort != tt.wantPort {
				t.Errorf("parsePortProto() port = %v, want %v", gotPort, tt.wantPort)
			}
			if gotProto != tt.wantProto {
				t.Errorf("parsePortProto() proto = %v, want %v", gotProto, tt.wantProto)
			}
		})
	}
}

func TestParseHostPort(t *testing.T) {
	tests := []struct {
		name    string
		portStr string
		want    uint16
	}{
		{
			name:    "valid port",
			portStr: "8080",
			want:    8080,
		},
		{
			name:    "empty string",
			portStr: "",
			want:    0,
		},
		{
			name:    "zero port",
			portStr: "0",
			want:    0,
		},
		{
			name:    "invalid port",
			portStr: "abc",
			want:    0,
		},
		{
			name:    "port too large",
			portStr: "99999",
			want:    0,
		},
		{
			name:    "negative port",
			portStr: "-1",
			want:    0,
		},
		{
			name:    "max valid port",
			portStr: "65535",
			want:    65535,
		},
		{
			name:    "port with spaces",
			portStr: " 8080 ",
			want:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseHostPort(tt.portStr)
			if got != tt.want {
				t.Errorf("parseHostPort() = %v, want %v", got, tt.want)
			}
		})
	}
}
