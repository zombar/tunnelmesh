package coord

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

func TestDownsampleHistory(t *testing.T) {
	// Create sample data
	now := time.Now()
	data := make([]StatsDataPoint, 100)
	for i := 0; i < 100; i++ {
		data[i] = StatsDataPoint{
			Timestamp:     now.Add(time.Duration(i) * time.Second),
			BytesSentRate: float64(i * 100),
		}
	}

	tests := []struct {
		name         string
		input        []StatsDataPoint
		targetPoints int
		wantLen      int
	}{
		{
			name:         "downsample 100 to 10",
			input:        data,
			targetPoints: 10,
			wantLen:      10,
		},
		{
			name:         "target larger than input returns input",
			input:        data[:5],
			targetPoints: 10,
			wantLen:      5,
		},
		{
			name:         "target equal to input returns input",
			input:        data[:10],
			targetPoints: 10,
			wantLen:      10,
		},
		{
			name:         "target 0 returns input",
			input:        data,
			targetPoints: 0,
			wantLen:      100,
		},
		{
			name:         "empty input returns empty",
			input:        []StatsDataPoint{},
			targetPoints: 10,
			wantLen:      0,
		},
		{
			name:         "nil input returns nil",
			input:        nil,
			targetPoints: 10,
			wantLen:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := downsampleHistory(tt.input, tt.targetPoints)
			if len(result) != tt.wantLen {
				t.Errorf("downsampleHistory() len = %d, want %d", len(result), tt.wantLen)
			}

			// For downsampled results, verify first and last points are preserved
			if tt.wantLen > 0 && len(tt.input) > 0 && tt.targetPoints > 0 && tt.targetPoints < len(tt.input) {
				if result[0].BytesSentRate != tt.input[0].BytesSentRate {
					t.Errorf("first point not preserved: got %f, want %f",
						result[0].BytesSentRate, tt.input[0].BytesSentRate)
				}
				if result[len(result)-1].BytesSentRate != tt.input[len(tt.input)-1].BytesSentRate {
					t.Errorf("last point not preserved: got %f, want %f",
						result[len(result)-1].BytesSentRate, tt.input[len(tt.input)-1].BytesSentRate)
				}
			}
		})
	}
}

func TestAdminOverview_IncludesLocation(t *testing.T) {
	cfg := &config.ServerConfig{
		Listen:       ":0",
		AuthToken:    "test-token",
		MeshCIDR:     "10.99.0.0/16",
		DomainSuffix: ".tunnelmesh",
		Locations:    true, // Enable location tracking for this test
		Admin: config.AdminConfig{
			Enabled: true,
		},
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Register a peer with location
	client := NewClient(ts.URL, "test-token")
	location := &proto.GeoLocation{
		Latitude:  51.5074,
		Longitude: -0.1278,
		Accuracy:  100,
		Source:    "manual",
		City:      "London",
		Country:   "United Kingdom",
	}
	_, err = client.Register("geonode", "SHA256:abc123", []string{"1.2.3.4"}, nil, 2222, 0, false, "v1.0.0", location)
	require.NoError(t, err)

	// Fetch admin overview
	resp, err := http.Get(ts.URL + "/admin/api/overview")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var overview AdminOverview
	err = json.NewDecoder(resp.Body).Decode(&overview)
	require.NoError(t, err)

	// Verify location is included in peer info
	require.Len(t, overview.Peers, 1)
	require.NotNil(t, overview.Peers[0].Location)
	assert.Equal(t, 51.5074, overview.Peers[0].Location.Latitude)
	assert.Equal(t, -0.1278, overview.Peers[0].Location.Longitude)
	assert.Equal(t, "manual", overview.Peers[0].Location.Source)
	assert.Equal(t, "London", overview.Peers[0].Location.City)
	assert.Equal(t, "United Kingdom", overview.Peers[0].Location.Country)
}

func TestDownsampleHistory_Uniformity(t *testing.T) {
	// Test that sampling is roughly uniform
	now := time.Now()
	data := make([]StatsDataPoint, 100)
	for i := 0; i < 100; i++ {
		data[i] = StatsDataPoint{
			Timestamp:     now.Add(time.Duration(i) * time.Second),
			BytesSentRate: float64(i),
		}
	}

	result := downsampleHistory(data, 10)
	if len(result) != 10 {
		t.Fatalf("expected 10 points, got %d", len(result))
	}

	// Check that indices are roughly evenly spaced
	// With 100 points downsampled to 10, expect indices 0, 11, 22, 33, ... 99
	expectedStep := float64(99) / float64(9) // ~11
	for i := 0; i < len(result); i++ {
		expectedValue := float64(int(float64(i) * expectedStep))
		if result[i].BytesSentRate != expectedValue {
			t.Errorf("point %d: got %f, expected %f", i, result[i].BytesSentRate, expectedValue)
		}
	}
}
