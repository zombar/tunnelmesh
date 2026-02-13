// SD Generator for Prometheus file_sd
// Polls the TunnelMesh coordination server and generates targets file for Prometheus.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/promsd"
)

func main() {
	cfg := configFromEnv()

	fmt.Printf("Starting SD generator\n")
	fmt.Printf("  Coord server: %s\n", cfg.CoordURL)
	fmt.Printf("  Poll interval: %s\n", cfg.PollInterval)
	fmt.Printf("  Output file: %s\n", cfg.OutputFile)
	fmt.Printf("  Coord output file: %s\n", cfg.CoordOutputFile)
	fmt.Printf("  Metrics port: %s\n", cfg.MetricsPort)
	fmt.Printf("  TLS skip verify: %t\n", cfg.TLSSkipVerify)

	generator := promsd.NewGenerator(cfg)

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	// Run immediately on start
	if count, err := generator.Generate(); err != nil {
		fmt.Printf("Error generating targets: %v\n", err)
	} else {
		fmt.Printf("Generated %d targets\n", count)
	}

	for range ticker.C {
		if count, err := generator.Generate(); err != nil {
			fmt.Printf("Error generating targets: %v\n", err)
		} else {
			fmt.Printf("Generated %d targets\n", count)
		}
	}
}

func configFromEnv() promsd.Config {
	cfg := promsd.DefaultConfig()

	if v := os.Getenv("COORD_SERVER_URL"); v != "" {
		cfg.CoordURL = v
	}
	if v := os.Getenv("AUTH_TOKEN"); v != "" {
		cfg.AuthToken = v
	}
	if v := os.Getenv("POLL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.PollInterval = d
		}
	}
	if v := os.Getenv("OUTPUT_FILE"); v != "" {
		cfg.OutputFile = v
	}
	if v := os.Getenv("COORD_OUTPUT_FILE"); v != "" {
		cfg.CoordOutputFile = v
	}
	if v := os.Getenv("METRICS_PORT"); v != "" {
		cfg.MetricsPort = v
	}
	if os.Getenv("TLS_SKIP_VERIFY") == "true" {
		cfg.TLSSkipVerify = true
	}

	return cfg
}
