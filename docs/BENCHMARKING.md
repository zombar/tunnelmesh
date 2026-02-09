# Benchmarking and Stress Testing

TunnelMesh includes built-in benchmarking tools to measure throughput, latency, and test mesh performance under adverse
network conditions.

## Overview

The benchmark system consists of two components:

| Component | Purpose | Chaos Support |
| ----------- | --------- | --------------- |
| `tunnelmesh benchmark` | CLI command for on-demand tests | Full control |
| `tunnelmesh-benchmarker` | Docker service for automated periodic tests | Configurable via env vars |

Benchmark traffic flows through the **actual mesh tunnel** (TUN device → encrypted tunnel → peer), giving you realistic
performance metrics for file transfers and real-time applications.

## Quick Start

### Local CLI Benchmark

```bash
# Basic speed test (10MB upload)
tunnelmesh benchmark peer-name

# Larger transfer for more accurate throughput measurement
tunnelmesh benchmark peer-name --size 100MB

# Download test
tunnelmesh benchmark peer-name --size 50MB --direction download

# Save results to JSON
tunnelmesh benchmark peer-name --output results.json
```text

### Docker Automated Benchmarks

```bash
# Start the full stack including benchmarker
cd docker
docker compose up -d

# View benchmark logs
docker compose logs -f benchmarker

# Results are saved to the benchmark-results volume
docker compose exec server ls /results/
```text

## CLI Reference

```text
tunnelmesh benchmark <peer-name> [flags]

Flags:
  --size string        Transfer size (default "10MB")
                       Examples: 1MB, 100MB, 1GB

  --direction string   Transfer direction (default "upload")
                       Options: upload, download

  --output string      Save results to JSON file

  --timeout duration   Benchmark timeout (default 2m0s)

  --port int           Benchmark server port (default 9998)

Chaos Testing Flags:
  --packet-loss float  Packet loss percentage, 0-100 (default 0)

  --latency duration   Additional latency to inject (default 0)
                       Examples: 10ms, 100ms, 1s

  --jitter duration    Random latency variation ±jitter (default 0)
                       Examples: 5ms, 20ms

  --bandwidth string   Bandwidth limit (default unlimited)
                       Examples: 1mbps, 10mbps, 100mbps, 1gbps
```text

## Chaos Testing

Chaos testing simulates adverse network conditions to stress test your mesh and verify resilience.

### Use Cases

| Scenario | Flags | Simulates |
| ---------- | ------- | ----------- |
| Lossy WiFi | `--packet-loss 2` | Occasional packet drops |
| Mobile network | `--latency 100ms --jitter 30ms` | High, variable latency |
| Congested link | `--bandwidth 5mbps` | Bandwidth-constrained path |
| Worst case | `--packet-loss 5 --latency 200ms --jitter 50ms --bandwidth 1mbps` | Very poor connection |

### Examples

```bash
# Simulate flaky WiFi (2% packet loss)
tunnelmesh benchmark peer-1 --size 50MB --packet-loss 2

# Simulate mobile/satellite connection (high latency + jitter)
tunnelmesh benchmark peer-1 --size 10MB --latency 150ms --jitter 50ms

# Simulate bandwidth-constrained link
tunnelmesh benchmark peer-1 --size 100MB --bandwidth 10mbps

# Combined stress test
tunnelmesh benchmark peer-1 --size 20MB \
  --packet-loss 3 \
  --latency 50ms \
  --jitter 20ms \
  --bandwidth 20mbps

# Save results for comparison
tunnelmesh benchmark peer-1 --size 50MB --output baseline.json
tunnelmesh benchmark peer-1 --size 50MB --packet-loss 5 --output with-loss.json
```text

## Docker Benchmarker

The Docker benchmarker runs **aggressive continuous benchmarks** with multiple concurrent transfers and randomized chaos
settings. The mesh is always under load.

### Default Behavior

- **Interval:** New batch every 30 seconds
- **Concurrency:** 3 simultaneous transfers per batch
- **Size:** 100MB per transfer
- **Direction:** 70% uploads, 30% downloads (randomized)
- **Chaos:** Randomly selected preset per transfer

With overlapping batches, you'll typically have 3-6 active transfers at any time.

### Chaos Presets

Each transfer randomly picks from these network condition presets:

| Preset | Packet Loss | Latency | Jitter | Bandwidth |
| -------- | ------------- | --------- | -------- | ----------- |
| `clean` | 0% | 0ms | 0ms | unlimited |
| `subtle` | 0.1% | 2ms | ±1ms | unlimited |
| `lossy-wifi` | 2% | 5ms | ±3ms | unlimited |
| `mobile-3g` | 1% | 100ms | ±50ms | 5 Mbps |
| `mobile-4g` | 0.5% | 30ms | ±15ms | 25 Mbps |
| `satellite` | 0.5% | 300ms | ±50ms | 10 Mbps |
| `congested` | 3% | 20ms | ±30ms | 1 Mbps |
| `bandwidth-10mbps` | 0% | 0ms | 0ms | 10 Mbps |
| `bandwidth-50mbps` | 0% | 0ms | 0ms | 50 Mbps |
| `bandwidth-100mbps` | 0% | 0ms | 0ms | 100 Mbps |

### Environment Variables

```yaml
# Basic configuration
COORD_SERVER_URL: http://localhost:8080  # Coordination server
AUTH_TOKEN: your-token                    # Authentication token
LOCAL_PEER: benchmarker                   # This peer's name
BENCHMARK_INTERVAL: 30s                   # Time between batch starts
BENCHMARK_CONCURRENCY: 3                  # Simultaneous transfers per batch
BENCHMARK_SIZE: 100MB                     # Transfer size per test
OUTPUT_DIR: /results                      # Where to save JSON results

# Chaos randomization
RANDOMIZE_CHAOS: true                     # Random preset per transfer (default)
# Set RANDOMIZE_CHAOS=false for all clean benchmarks
```text

### Controlling the Benchmarker

```bash
# Start with default aggressive settings
docker compose up -d benchmarker

# Watch the chaos unfold
docker compose logs -f benchmarker

# Run with more concurrency
docker compose run -e BENCHMARK_CONCURRENCY=5 benchmarker

# Run all clean benchmarks (no chaos)
docker compose run -e RANDOMIZE_CHAOS=false benchmarker

# Faster interval (more overlap)
docker compose run -e BENCHMARK_INTERVAL=15s benchmarker
```text

### Viewing Results

```bash
# List benchmark results
docker compose exec server ls -la /results/

# View latest result
 docker compose exec server cat /results/benchmark_*.json | jq . 

# Copy results to host
docker cp tunnelmesh-server:/results ./benchmark-results/
```text

## Understanding Results

### JSON Output Format

```json
{
  "id": "bench-abc123",
  "local_peer": "server",
  "remote_peer": "client-1",
  "direction": "upload",
  "timestamp": "2024-01-15T10:30:00Z",
  "requested_size_bytes": 104857600,
  "transferred_size_bytes": 104857600,
  "duration_ms": 1250,
  "throughput_bps": 83886080,
  "throughput_mbps": 671.09,
  "latency_min_ms": 0.5,
  "latency_max_ms": 3.2,
  "latency_avg_ms": 1.1,
  "success": true,
  "chaos": {
    "packet_loss_percent": 0.1,
    "latency": 2000000,
    "jitter": 1000000
  }
}
```text

### Key Metrics

| Metric | Description | Good Values |
| -------- | ------------- | ------------- |
| `throughput_mbps` | Megabits per second | Depends on link speed |
| `latency_avg_ms` | Average round-trip time | <5ms LAN, <50ms WAN |
| `latency_max_ms` | Worst-case latency | Should be close to avg |
| `success` | Whether transfer completed | Should be `true` |

### Comparing Results

```bash
# Compare baseline vs chaos results
 jq -s '.[0].throughput_mbps as $base|
 .[1].throughput_mbps as $chaos|
       {baseline: $base, with_chaos: $chaos,
        degradation_pct: (($base - $chaos) / $base * 100)}' \
  baseline.json with-loss.json
```text

## Troubleshooting

### Benchmark Fails to Connect

```text
Error: cannot resolve peer "peer-1": is the mesh daemon running?
```text

- Ensure the mesh daemon is running: `tunnelmesh status`
- Check peer is online: `tunnelmesh peers`
- Verify DNS resolution: `dig peer-1.tunnelmesh`

### Low Throughput

1. Check for packet loss: run with `--packet-loss 0` to ensure clean baseline
2. Verify transport type: SSH is slower than UDP
3. Check CPU usage during benchmark
4. Try larger transfer size for more accurate measurement

### High Latency Variance

- High jitter may indicate network congestion
- Check for competing traffic on the mesh
- Verify both peers have stable connections

## Best Practices

1. **Baseline First**: Run without chaos to establish baseline performance
2. **Multiple Runs**: Run 3-5 benchmarks and average results
3. **Warm Up**: The first benchmark after mesh startup may be slower
4. **Size Matters**: Use larger transfers (100MB+) for accurate throughput measurement
5. **Monitor Both Ends**: Check CPU/memory on both peers during stress tests
6. **Save Results**: Always use `--output` for reproducible comparisons
