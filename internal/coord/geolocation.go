package coord

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

// ipAPIResponse represents the response from ip-api.com.
type ipAPIResponse struct {
	Status     string  `json:"status"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	City       string  `json:"city"`
	RegionName string  `json:"regionName"`
	Country    string  `json:"country"`
	Message    string  `json:"message,omitempty"`
}

// IPGeoCache caches IP geolocation results with rate limiting and LRU eviction.
// It uses ip-api.com for lookups (free tier: 45 req/min).
type IPGeoCache struct {
	baseURL              string
	cache                map[string]*proto.GeoLocation
	accessOrder          []string // LRU order: oldest at front, newest at back
	maxCacheSize         int
	mu                   sync.RWMutex
	client               *http.Client
	maxRequestsPerMinute int
	requestCount         int
	windowStart          time.Time
}

// DefaultMaxCacheSize is the default maximum number of entries in the geolocation cache.
const DefaultMaxCacheSize = 1000

// NewIPGeoCache creates a new IP geolocation cache.
// The baseURL should include the trailing slash, e.g., "http://ip-api.com/json/".
func NewIPGeoCache(baseURL string) *IPGeoCache {
	if baseURL == "" {
		baseURL = "http://ip-api.com/json/"
	}
	return &IPGeoCache{
		baseURL:      baseURL,
		cache:        make(map[string]*proto.GeoLocation),
		accessOrder:  make([]string, 0, DefaultMaxCacheSize),
		maxCacheSize: DefaultMaxCacheSize,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		maxRequestsPerMinute: 40, // Stay below ip-api.com's 45/min limit
	}
}

// Lookup returns geolocation for an IP, using cache when available.
// Returns nil (not error) if the IP cannot be geolocated (e.g., private IP).
func (c *IPGeoCache) Lookup(ctx context.Context, ip string) (*proto.GeoLocation, error) {
	// Normalize IP (remove port if present)
	host, _, err := net.SplitHostPort(ip)
	if err == nil && host != "" {
		ip = host
	}

	// Check cache first
	c.mu.Lock()
	if loc, ok := c.cache[ip]; ok {
		// Move to end of access order (most recently used)
		c.moveToEnd(ip)
		c.mu.Unlock()
		return loc, nil
	}
	c.mu.Unlock()

	// Rate limit check
	c.mu.Lock()
	now := time.Now()
	if now.Sub(c.windowStart) > time.Minute {
		c.requestCount = 0
		c.windowStart = now
	}
	if c.requestCount >= c.maxRequestsPerMinute {
		c.mu.Unlock()
		log.Debug().Str("ip", ip).Msg("IP geolocation rate limited")
		return nil, nil
	}
	c.requestCount++
	c.mu.Unlock()

	// Make API request
	url := c.baseURL + ip
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}

	var result ipAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if result.Status != "success" {
		// Not an error - just can't geolocate this IP (e.g., private range)
		log.Debug().Str("ip", ip).Str("reason", result.Message).Msg("IP geolocation failed")
		return nil, nil
	}

	loc := &proto.GeoLocation{
		Latitude:  result.Lat,
		Longitude: result.Lon,
		Accuracy:  50000, // ~50km for city-level accuracy
		Source:    "ip",
		City:      result.City,
		Region:    result.RegionName,
		Country:   result.Country,
		UpdatedAt: time.Now(),
	}

	// Cache the result with LRU eviction
	c.mu.Lock()
	// Evict oldest entries if at capacity
	for len(c.cache) >= c.maxCacheSize && len(c.accessOrder) > 0 {
		oldest := c.accessOrder[0]
		c.accessOrder = c.accessOrder[1:]
		delete(c.cache, oldest)
	}
	c.cache[ip] = loc
	c.accessOrder = append(c.accessOrder, ip)
	c.mu.Unlock()

	log.Debug().
		Str("ip", ip).
		Float64("lat", loc.Latitude).
		Float64("lon", loc.Longitude).
		Str("city", loc.City).
		Msg("IP geolocation cached")

	return loc, nil
}

// moveToEnd moves an IP to the end of the access order (most recently used).
// Must be called with c.mu held.
func (c *IPGeoCache) moveToEnd(ip string) {
	for i, v := range c.accessOrder {
		if v == ip {
			c.accessOrder = append(c.accessOrder[:i], c.accessOrder[i+1:]...)
			c.accessOrder = append(c.accessOrder, ip)
			return
		}
	}
}

// Len returns the current number of entries in the cache.
func (c *IPGeoCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.cache)
}
