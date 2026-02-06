package promsd

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// PeerFetcher is an interface for fetching peers from a coordination server.
type PeerFetcher interface {
	FetchPeers() ([]Peer, error)
}

// Generator generates Prometheus file_sd target files from TunnelMesh peers.
type Generator struct {
	config  Config
	client  *http.Client
	fetcher PeerFetcher
}

// NewGenerator creates a new Generator with the given configuration.
func NewGenerator(cfg Config) *Generator {
	g := &Generator{
		config: cfg,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: cfg.TLSSkipVerify,
				},
			},
		},
	}
	// Default fetcher uses HTTP client
	g.fetcher = &httpFetcher{generator: g}
	return g
}

// SetFetcher sets a custom peer fetcher (useful for testing).
func (g *Generator) SetFetcher(f PeerFetcher) {
	g.fetcher = f
}

// httpFetcher implements PeerFetcher using HTTP.
type httpFetcher struct {
	generator *Generator
}

func (f *httpFetcher) FetchPeers() ([]Peer, error) {
	url := fmt.Sprintf("%s/api/v1/peers", f.generator.config.CoordURL)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if f.generator.config.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+f.generator.config.AuthToken)
	}

	resp, err := f.generator.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch peers: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var peersResp PeersResponse
	if err := json.NewDecoder(resp.Body).Decode(&peersResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return peersResp.Peers, nil
}

// PeersToTargets converts a list of peers to Prometheus targets.
// It filters out peers that are offline or have no mesh IP.
func PeersToTargets(peers []Peer, metricsPort string, onlineThreshold time.Duration, now time.Time) []Target {
	var targets []Target
	for _, peer := range peers {
		// Check if peer is online (last_seen within threshold)
		if now.Sub(peer.LastSeen) >= onlineThreshold {
			continue
		}
		if peer.MeshIP == "" {
			continue
		}
		targets = append(targets, Target{
			Targets: []string{fmt.Sprintf("%s:%s", peer.MeshIP, metricsPort)},
			Labels: map[string]string{
				"peer": peer.Name,
			},
		})
	}
	return targets
}

// WriteTargets writes targets to a file atomically.
func WriteTargets(targets []Target, outputFile string) error {
	data, err := json.MarshalIndent(targets, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal targets: %w", err)
	}

	// Write atomically using temp file
	tmpFile := outputFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpFile, outputFile); err != nil {
		// Clean up temp file on rename failure
		_ = os.Remove(tmpFile)
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

// Generate fetches peers and writes the targets file.
func (g *Generator) Generate() (int, error) {
	peers, err := g.fetcher.FetchPeers()
	if err != nil {
		return 0, err
	}

	targets := PeersToTargets(peers, g.config.MetricsPort, g.config.OnlineThreshold, time.Now())

	if err := WriteTargets(targets, g.config.OutputFile); err != nil {
		return 0, err
	}

	return len(targets), nil
}

// Config returns the generator's configuration.
func (g *Generator) Config() Config {
	return g.config
}
