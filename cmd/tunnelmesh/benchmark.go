package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/benchmark"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/pkg/bytesize"
)

var (
	// Benchmark flags
	benchSize      string
	benchDirection string
	benchOutput    string
	benchTimeout   time.Duration
	benchPort      int

	// Chaos flags
	chaosPacketLoss float64
	chaosLatency    time.Duration
	chaosJitter     time.Duration
	chaosBandwidth  string

	// lookupHost is the function used to resolve hostnames. It can be overridden in tests.
	lookupHost = net.DefaultResolver.LookupHost
)

func newBenchmarkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "benchmark <peer-name>",
		Short: "Run a speed test to a peer",
		Long: `Run a benchmark speed test to measure throughput and latency to a peer.

The benchmark transfers data through the actual mesh tunnel to give realistic
performance metrics for file transfers and gaming.

Examples:
  # Basic speed test with default 10MB transfer
  tunnelmesh benchmark peer-1

  # Upload 100MB to measure throughput
  tunnelmesh benchmark peer-1 --size 100MB --direction upload

  # Download test
  tunnelmesh benchmark peer-1 --size 50MB --direction download

  # Save results to JSON
  tunnelmesh benchmark peer-1 --output results.json

  # Chaos testing - simulate poor network
  tunnelmesh benchmark peer-1 --size 10MB --packet-loss 5 --latency 50ms`,
		Args: cobra.ExactArgs(1),
		RunE: runBenchmark,
	}

	// Transfer flags
	cmd.Flags().StringVar(&benchSize, "size", "10MB", "transfer size (e.g., 10MB, 100MB, 1GB)")
	cmd.Flags().StringVar(&benchDirection, "direction", "upload", "transfer direction (upload or download)")
	cmd.Flags().StringVarP(&benchOutput, "output", "o", "", "JSON output file path")
	cmd.Flags().DurationVar(&benchTimeout, "timeout", 120*time.Second, "benchmark timeout")
	cmd.Flags().IntVar(&benchPort, "port", benchmark.DefaultPort, "benchmark server port")

	// Chaos testing flags
	cmd.Flags().Float64Var(&chaosPacketLoss, "packet-loss", 0, "packet loss percentage (0-100)")
	cmd.Flags().DurationVar(&chaosLatency, "latency", 0, "additional latency to inject")
	cmd.Flags().DurationVar(&chaosJitter, "jitter", 0, "random latency variation (+/- jitter)")
	cmd.Flags().StringVar(&chaosBandwidth, "bandwidth", "", "bandwidth limit (e.g., 10mbps, 100MB/s)")

	return cmd
}

func runBenchmark(_ *cobra.Command, args []string) error {
	peerName := args[0]

	setupLogging()

	// Parse size
	size, err := bytesize.Parse(benchSize)
	if err != nil {
		return fmt.Errorf("invalid size: %w", err)
	}

	// Parse bandwidth limit
	var bandwidthBps int64
	if chaosBandwidth != "" {
		bandwidthBps, err = bytesize.ParseRate(chaosBandwidth)
		if err != nil {
			return fmt.Errorf("invalid bandwidth: %w", err)
		}
	}

	// Build config
	cfg := benchmark.Config{
		PeerName:  peerName,
		Size:      size,
		Direction: benchDirection,
		Timeout:   benchTimeout,
		Port:      benchPort,
		Chaos: benchmark.ChaosConfig{
			PacketLossPercent: chaosPacketLoss,
			Latency:           chaosLatency,
			Jitter:            chaosJitter,
			BandwidthBps:      bandwidthBps,
		},
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Load config to get local peer name
	meshCfg, err := config.LoadPeerConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to load config (is the mesh daemon running?): %w", err)
	}

	// Get peer's mesh IP via DNS
	peerIP, err := getPeerMeshIP(peerName)
	if err != nil {
		return err
	}

	// Print benchmark info
	fmt.Printf("Benchmark: %s -> %s\n", meshCfg.Name, peerName)
	fmt.Printf("  Size:      %s\n", bytesize.Format(size))
	fmt.Printf("  Direction: %s\n", benchDirection)
	fmt.Printf("  Peer IP:   %s\n", peerIP)

	if cfg.Chaos.IsEnabled() {
		fmt.Println("  Chaos testing enabled:")
		if chaosPacketLoss > 0 {
			fmt.Printf("    Packet loss: %.1f%%\n", chaosPacketLoss)
		}
		if chaosLatency > 0 {
			fmt.Printf("    Latency:     %v\n", chaosLatency)
		}
		if chaosJitter > 0 {
			fmt.Printf("    Jitter:      +/-%v\n", chaosJitter)
		}
		if bandwidthBps > 0 {
			fmt.Printf("    Bandwidth:   %s\n", bytesize.FormatRate(bandwidthBps))
		}
	}

	fmt.Println()
	fmt.Println("Running benchmark...")

	// Create client and run benchmark
	client := benchmark.NewClient(meshCfg.Name, peerIP)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	result, err := client.Run(ctx, cfg)
	if err != nil {
		return fmt.Errorf("benchmark failed: %w", err)
	}

	// Display results
	printBenchmarkResult(result)

	// Write JSON output if requested
	if benchOutput != "" {
		if err := writeBenchmarkJSON(result, benchOutput); err != nil {
			return fmt.Errorf("failed to write output: %w", err)
		}
		fmt.Printf("\nResults saved to: %s\n", benchOutput)
	}

	return nil
}

func getPeerMeshIP(peerName string) (string, error) {
	// Resolve the peer name using DNS
	// The mesh DNS should resolve peer-name.tunnelmesh to its mesh IP
	hostname := peerName + ".tunnelmesh"

	// Try to resolve using the system resolver (which should use mesh DNS)
	addrs, err := lookupHost(context.Background(), hostname)
	if err != nil {
		// Fallback: try direct hostname
		addrs, err = lookupHost(context.Background(), peerName)
		if err != nil {
			return "", fmt.Errorf("cannot resolve peer %q: is the mesh daemon running?", peerName)
		}
	}

	if len(addrs) == 0 {
		return "", fmt.Errorf("no address found for peer %q", peerName)
	}

	return addrs[0], nil
}

func printBenchmarkResult(r *benchmark.Result) {
	fmt.Println()
	if r.Success {
		fmt.Println("=== Benchmark Results ===")
	} else {
		fmt.Println("=== Benchmark Failed ===")
		fmt.Printf("Error: %s\n", r.Error)
		return
	}

	fmt.Printf("  Transfer:     %s / %s\n",
		bytesize.Format(r.TransferredSize),
		bytesize.Format(r.RequestedSize))
	fmt.Printf("  Duration:     %d ms\n", r.DurationMs)
	fmt.Printf("  Throughput:   %s (%s)\n",
		bytesize.FormatRate(int64(r.ThroughputBps)),
		bytesize.Format(int64(r.ThroughputBps))+"/s")

	fmt.Println()
	fmt.Println("  Latency:")
	fmt.Printf("    Min:        %.2f ms\n", r.LatencyMinMs)
	fmt.Printf("    Avg:        %.2f ms\n", r.LatencyAvgMs)
	fmt.Printf("    Max:        %.2f ms\n", r.LatencyMaxMs)
}

func writeBenchmarkJSON(r *benchmark.Result, path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
