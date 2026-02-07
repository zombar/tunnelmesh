// Benchmarker service for Docker - runs aggressive continuous benchmarks between mesh peers.
// Multiple concurrent transfers with randomized chaos settings ensure the mesh is always under load.
// Results are written to JSON files for analysis.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/benchmark"
	"github.com/tunnelmesh/tunnelmesh/pkg/bytesize"
)

type Config struct {
	// CoordURL is the coordination server URL for fetching peer list
	CoordURL string

	// AuthToken for authentication with the coord server
	AuthToken string

	// LocalPeer is the name of this peer (for benchmark results)
	LocalPeer string

	// Interval between benchmark batch starts
	Interval time.Duration

	// Concurrency is the number of simultaneous benchmarks to run
	Concurrency int

	// Size of data to transfer in each benchmark
	Size int64

	// OutputDir for JSON result files
	OutputDir string

	// TLSSkipVerify disables TLS certificate verification
	TLSSkipVerify bool

	// RandomizeChaos enables randomized chaos parameters per benchmark
	RandomizeChaos bool
}

// Chaos presets for randomization
var chaosPresets = []struct {
	name   string
	config benchmark.ChaosConfig
}{
	{
		name:   "clean",
		config: benchmark.ChaosConfig{},
	},
	{
		name: "subtle",
		config: benchmark.ChaosConfig{
			PacketLossPercent: 0.1,
			Latency:           2 * time.Millisecond,
			Jitter:            1 * time.Millisecond,
		},
	},
	{
		name: "lossy-wifi",
		config: benchmark.ChaosConfig{
			PacketLossPercent: 2.0,
			Latency:           5 * time.Millisecond,
			Jitter:            3 * time.Millisecond,
		},
	},
	{
		name: "mobile-3g",
		config: benchmark.ChaosConfig{
			PacketLossPercent: 1.0,
			Latency:           100 * time.Millisecond,
			Jitter:            50 * time.Millisecond,
			BandwidthBps:      5 * 1024 * 1024, // 5 Mbps
		},
	},
	{
		name: "mobile-4g",
		config: benchmark.ChaosConfig{
			PacketLossPercent: 0.5,
			Latency:           30 * time.Millisecond,
			Jitter:            15 * time.Millisecond,
			BandwidthBps:      25 * 1024 * 1024, // 25 Mbps
		},
	},
	{
		name: "satellite",
		config: benchmark.ChaosConfig{
			PacketLossPercent: 0.5,
			Latency:           300 * time.Millisecond,
			Jitter:            50 * time.Millisecond,
			BandwidthBps:      10 * 1024 * 1024, // 10 Mbps
		},
	},
	{
		name: "congested",
		config: benchmark.ChaosConfig{
			PacketLossPercent: 3.0,
			Latency:           20 * time.Millisecond,
			Jitter:            30 * time.Millisecond,
			BandwidthBps:      1 * 1024 * 1024, // 1 Mbps
		},
	},
	{
		name: "bandwidth-10mbps",
		config: benchmark.ChaosConfig{
			BandwidthBps: 10 * 1024 * 1024,
		},
	},
	{
		name: "bandwidth-50mbps",
		config: benchmark.ChaosConfig{
			BandwidthBps: 50 * 1024 * 1024,
		},
	},
	{
		name: "bandwidth-100mbps",
		config: benchmark.ChaosConfig{
			BandwidthBps: 100 * 1024 * 1024,
		},
	},
}

func main() {
	cfg := configFromEnv()

	fmt.Printf("Starting aggressive benchmarker\n")
	fmt.Printf("  Coord server:    %s\n", cfg.CoordURL)
	fmt.Printf("  Local peer:      %s\n", cfg.LocalPeer)
	fmt.Printf("  Interval:        %s\n", cfg.Interval)
	fmt.Printf("  Concurrency:     %d parallel transfers\n", cfg.Concurrency)
	fmt.Printf("  Size:            %s\n", bytesize.Format(cfg.Size))
	fmt.Printf("  Output dir:      %s\n", cfg.OutputDir)
	fmt.Printf("  Randomize chaos: %v\n", cfg.RandomizeChaos)
	if cfg.RandomizeChaos {
		fmt.Printf("  Chaos presets:   %d available\n", len(chaosPresets))
	}

	// Ensure output directory exists
	if err := os.MkdirAll(cfg.OutputDir, 0755); err != nil {
		fmt.Printf("Error creating output directory: %v\n", err)
		os.Exit(1)
	}

	// Wait a bit for mesh to stabilize before first run
	fmt.Println("Waiting 20s for mesh to stabilize...")
	time.Sleep(20 * time.Second)

	// Run continuous benchmark loop
	runContinuousBenchmarks(cfg)
}

func runContinuousBenchmarks(cfg Config) {
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	// Track active benchmarks
	var activeCount int
	var mu sync.Mutex

	// Run immediately on start
	go runBenchmarkBatch(cfg, &activeCount, &mu)

	for range ticker.C {
		mu.Lock()
		current := activeCount
		mu.Unlock()

		fmt.Printf("\n=== Starting new batch (currently %d active transfers) ===\n", current)
		go runBenchmarkBatch(cfg, &activeCount, &mu)
	}
}

func runBenchmarkBatch(cfg Config, activeCount *int, mu *sync.Mutex) {
	peers, err := fetchPeers(cfg)
	if err != nil {
		fmt.Printf("Error fetching peers: %v\n", err)
		return
	}

	if len(peers) == 0 {
		fmt.Println("No peers available for benchmark")
		return
	}

	// Select peers for this batch - use all available up to concurrency limit
	numBenchmarks := cfg.Concurrency
	if numBenchmarks > len(peers) {
		numBenchmarks = len(peers)
	}

	// Shuffle peers for variety
	shuffled := make([]peerInfo, len(peers))
	copy(shuffled, peers)
	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	selectedPeers := shuffled[:numBenchmarks]

	fmt.Printf("Starting %d concurrent benchmarks\n", len(selectedPeers))

	var wg sync.WaitGroup
	for _, peer := range selectedPeers {
		wg.Add(1)

		// Pick chaos config
		var chaos benchmark.ChaosConfig
		var chaosName string
		if cfg.RandomizeChaos {
			preset := chaosPresets[rand.Intn(len(chaosPresets))]
			chaos = preset.config
			chaosName = preset.name
		}

		// Pick direction randomly
		direction := benchmark.DirectionUpload
		if rand.Float32() < 0.3 { // 30% downloads
			direction = benchmark.DirectionDownload
		}

		go func(p peerInfo, ch benchmark.ChaosConfig, chName, dir string) {
			defer wg.Done()

			mu.Lock()
			*activeCount++
			current := *activeCount
			mu.Unlock()

			defer func() {
				mu.Lock()
				*activeCount--
				mu.Unlock()
			}()

			result := runBenchmark(cfg, p, ch, chName, dir, current)
			if result != nil {
				saveResult(cfg, result, chName)
			}
		}(peer, chaos, chaosName, direction)

		// Stagger starts slightly to avoid thundering herd
		time.Sleep(time.Duration(100+rand.Intn(400)) * time.Millisecond)
	}

	wg.Wait()
	fmt.Printf("Batch complete\n")
}

type peerInfo struct {
	Name   string `json:"name"`
	MeshIP string `json:"mesh_ip"`
}

type peersResponse struct {
	Peers []peerInfo `json:"peers"`
}

func fetchPeers(cfg Config) ([]peerInfo, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.TLSSkipVerify},
	}
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}

	req, err := http.NewRequest("GET", cfg.CoordURL+"/api/v1/peers", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var response peersResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	peers := response.Peers

	// Filter out self
	var filtered []peerInfo
	for _, p := range peers {
		if p.Name != cfg.LocalPeer {
			filtered = append(filtered, p)
		}
	}

	return filtered, nil
}

func runBenchmark(cfg Config, peer peerInfo, chaos benchmark.ChaosConfig, chaosName, direction string, activeNum int) *benchmark.Result {
	chaosDesc := "clean"
	if chaosName != "" {
		chaosDesc = chaosName
	}
	if chaos.IsEnabled() && chaosName == "" {
		chaosDesc = "custom"
	}

	fmt.Printf("  [%d] %s -> %s (%s, %s, %s)\n",
		activeNum, cfg.LocalPeer, peer.Name, direction, bytesize.Format(cfg.Size), chaosDesc)

	benchCfg := benchmark.Config{
		PeerName:  peer.Name,
		Size:      cfg.Size,
		Direction: direction,
		Timeout:   180 * time.Second, // Longer timeout for bandwidth-limited tests
		Port:      benchmark.DefaultPort,
		Chaos:     chaos,
	}

	client := benchmark.NewClient(cfg.LocalPeer, peer.MeshIP)
	ctx, cancel := context.WithTimeout(context.Background(), benchCfg.Timeout)
	defer cancel()

	result, err := client.Run(ctx, benchCfg)
	if err != nil {
		fmt.Printf("  [%d] %s: ERROR: %v\n", activeNum, peer.Name, err)
		return nil
	}

	fmt.Printf("  [%d] %s: %s @ %s (lat: %.1fms)\n",
		activeNum, peer.Name, direction,
		bytesize.FormatRate(int64(result.ThroughputBps)),
		result.LatencyAvgMs)

	return result
}

func saveResult(cfg Config, result *benchmark.Result, chaosName string) {
	// Create filename with timestamp and chaos preset
	suffix := ""
	if chaosName != "" {
		suffix = "_" + chaosName
	}
	filename := fmt.Sprintf("benchmark_%s_%s_%s%s.json",
		result.LocalPeer,
		result.RemotePeer,
		result.Timestamp.Format("20060102_150405"),
		suffix)
	path := filepath.Join(cfg.OutputDir, filename)

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Printf("    Error marshaling result: %v\n", err)
		return
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		fmt.Printf("    Error writing result: %v\n", err)
		return
	}
}

func configFromEnv() Config {
	cfg := Config{
		CoordURL:       "http://localhost:8080",
		LocalPeer:      "benchmarker",
		Interval:       30 * time.Second,  // Run batches every 30s
		Concurrency:    3,                 // 3 concurrent benchmarks
		Size:           100 * 1024 * 1024, // 100MB
		OutputDir:      "/results",
		RandomizeChaos: true, // Randomize chaos by default
	}

	if v := os.Getenv("COORD_SERVER_URL"); v != "" {
		cfg.CoordURL = v
	}
	if v := os.Getenv("AUTH_TOKEN"); v != "" {
		cfg.AuthToken = v
	}
	if v := os.Getenv("LOCAL_PEER"); v != "" {
		cfg.LocalPeer = v
	}
	if v := os.Getenv("BENCHMARK_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Interval = d
		}
	}
	if v := os.Getenv("BENCHMARK_CONCURRENCY"); v != "" {
		var c int
		if _, err := fmt.Sscanf(v, "%d", &c); err == nil && c > 0 {
			cfg.Concurrency = c
		}
	}
	if v := os.Getenv("BENCHMARK_SIZE"); v != "" {
		if size, err := bytesize.Parse(v); err == nil {
			cfg.Size = size
		}
	}
	if v := os.Getenv("OUTPUT_DIR"); v != "" {
		cfg.OutputDir = v
	}
	if os.Getenv("TLS_SKIP_VERIFY") == "true" {
		cfg.TLSSkipVerify = true
	}

	// Chaos randomization toggle
	if os.Getenv("RANDOMIZE_CHAOS") == "false" {
		cfg.RandomizeChaos = false
	}

	return cfg
}
