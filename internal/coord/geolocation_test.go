package coord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIPGeoCache_Lookup(t *testing.T) {
	// Create mock ip-api.com server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract IP from path (format: /json/{ip})
		ip := r.URL.Path[len("/json/"):]

		resp := map[string]interface{}{
			"status":     "success",
			"lat":        51.5074,
			"lon":        -0.1278,
			"city":       "London",
			"regionName": "England",
			"country":    "United Kingdom",
		}

		// Return different location for different IPs
		if ip == "8.8.8.8" {
			resp["lat"] = 37.751
			resp["lon"] = -97.822
			resp["city"] = "Cheney"
			resp["regionName"] = "Kansas"
			resp["country"] = "United States"
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	cache := NewIPGeoCache(ts.URL + "/json/")

	ctx := context.Background()

	// Test lookup
	loc, err := cache.Lookup(ctx, "1.2.3.4")
	require.NoError(t, err)
	require.NotNil(t, loc)

	assert.Equal(t, 51.5074, loc.Latitude)
	assert.Equal(t, -0.1278, loc.Longitude)
	assert.Equal(t, "ip", loc.Source)
	assert.Equal(t, "London", loc.City)
	assert.Equal(t, "England", loc.Region)
	assert.Equal(t, "United Kingdom", loc.Country)
	assert.Equal(t, float64(50000), loc.Accuracy) // ~50km for IP geolocation
}

func TestIPGeoCache_CachesResults(t *testing.T) {
	var requestCount int

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		resp := map[string]interface{}{
			"status":     "success",
			"lat":        51.5074,
			"lon":        -0.1278,
			"city":       "London",
			"regionName": "England",
			"country":    "United Kingdom",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	cache := NewIPGeoCache(ts.URL + "/json/")
	ctx := context.Background()

	// First lookup
	_, err := cache.Lookup(ctx, "1.2.3.4")
	require.NoError(t, err)

	// Second lookup - should use cache
	_, err = cache.Lookup(ctx, "1.2.3.4")
	require.NoError(t, err)

	// Should only have made one request
	assert.Equal(t, 1, requestCount)
}

func TestIPGeoCache_StripPort(t *testing.T) {
	var requestedIP string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedIP = r.URL.Path[len("/json/"):]
		resp := map[string]interface{}{
			"status":     "success",
			"lat":        51.5074,
			"lon":        -0.1278,
			"city":       "London",
			"regionName": "England",
			"country":    "United Kingdom",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	cache := NewIPGeoCache(ts.URL + "/json/")
	ctx := context.Background()

	// Lookup with port
	_, err := cache.Lookup(ctx, "1.2.3.4:8080")
	require.NoError(t, err)

	// Should have stripped the port
	assert.Equal(t, "1.2.3.4", requestedIP)
}

func TestIPGeoCache_RateLimiting(t *testing.T) {
	var requestCount int

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		resp := map[string]interface{}{
			"status":     "success",
			"lat":        51.5074,
			"lon":        -0.1278,
			"city":       "London",
			"regionName": "England",
			"country":    "United Kingdom",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	cache := NewIPGeoCache(ts.URL + "/json/")
	cache.maxRequestsPerMinute = 5 // Lower limit for testing
	ctx := context.Background()

	// Make more requests than the limit (different IPs to avoid cache)
	for i := 0; i < 10; i++ {
		ip := "1.2.3." + string(rune('0'+i))
		_, _ = cache.Lookup(ctx, ip)
	}

	// Should have been rate limited
	assert.LessOrEqual(t, requestCount, 5)
}

func TestIPGeoCache_FailedLookup(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"status":  "fail",
			"message": "reserved range",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	cache := NewIPGeoCache(ts.URL + "/json/")
	ctx := context.Background()

	// Private IP should fail
	loc, err := cache.Lookup(ctx, "192.168.1.1")
	require.NoError(t, err) // No error, just nil result
	assert.Nil(t, loc)
}

func TestIPGeoCache_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	cache := NewIPGeoCache(ts.URL + "/json/")
	ctx := context.Background()

	loc, err := cache.Lookup(ctx, "1.2.3.4")
	require.Error(t, err)
	assert.Nil(t, loc)
}

func TestIPGeoCache_Timeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		resp := map[string]interface{}{
			"status": "success",
			"lat":    51.5074,
			"lon":    -0.1278,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	cache := NewIPGeoCache(ts.URL + "/json/")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := cache.Lookup(ctx, "1.2.3.4")
	assert.Error(t, err) // Should timeout
}
