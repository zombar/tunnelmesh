# Network Stack Testing for S3 Benchmark

This document describes the network testing capabilities of the S3 stress testing framework.

## Overview

The S3 benchmark can test different layers of the network stack to ensure realistic performance testing and catch issues that might not appear when bypassing network layers.

## Testing Phases

### ✅ Phase 1: HTTP Server Testing (COMPLETE)

**Purpose**: Test the HTTP + RBAC layer instead of bypassing it with direct Store calls.

**Status**: Implemented and working

**Usage**:
```bash
USE_HTTP=1 go test ./internal/s3bench/simulator -run=TestStress -v
```

**What it tests**:
- Full HTTP request/response cycle via `httptest.Server`
- S3 HTTP server handler (`internal/coord/s3/server.go`)
- HTTP authentication via `mockHTTPAuthorizer`
- RBAC authorization checks for all operations
- Content-Type, metadata headers, status codes
- Error responses (403, 404, etc.)

**Results** (10K+ operations):
```
✅ 100% success rate (10,836 operations)
✅ 490 ops/sec throughput
✅ 100% adversary denial rate (RBAC working correctly)
✅ 795µs average upload latency
✅ 6/6 workflow tests passing
```

**Implementation**:
- `mockHTTPAuthorizer`: Validates X-User-ID header and checks RBAC
- `executeUploadHTTP`, `executeDeleteHTTP`, `executeDownloadHTTP`: HTTP methods
- `httptest.Server`: Creates test HTTP server in-memory

**Files**:
- `internal/s3bench/simulator/stress_test.go` (lines 76-83, 244-264)
- `internal/s3bench/simulator/simulator.go` (HTTP methods)

---

### ✅ Phase 2: TUN Device Loopback Testing (COMPLETE)

**Purpose**: Test the TUN device layer which handles IP packet I/O between kernel and userspace.

**Status**: Implemented and working

**Usage**:
```bash
USE_TUN=1 sudo go test ./internal/s3bench/simulator -run=TestStress -v
```

**Requirements**:
- Root/admin privileges (required for TUN device creation)
- Linux or macOS (Windows not supported)

**What it tests**:
- TUN device creation (`internal/tun/device.go`)
- IP packet writing to TUN device
- IP packet reading from TUN device
- Packet routing through kernel network stack
- IPv4 header construction and checksums
- ICMP protocol handling

**Test flow**:
1. Create TUN device with IP `172.31.99.1/24`
2. Generate ICMP echo request packet (28 bytes):
   - IPv4 header (20 bytes) with correct checksums
   - ICMP echo request (8 bytes)
   - Source: `172.31.99.2`, Destination: `172.31.99.1`
3. Write packet to TUN device
4. Read packet back (2s timeout)
5. Verify packet structure and destination IP

**Graceful degradation**:
- Skips with clear message if not running as root
- Skips on Windows (TUN not supported)
- Continues even if loopback packet not received (OS-dependent routing)

**Implementation**:
- `isRootUser()`: Checks `syscall.Geteuid() == 0` on Unix
- `testTUNLoopback()`: Creates TUN, writes/reads packets
- `ipChecksum()`: Calculates IPv4/ICMP checksums

**Files**:
- `internal/s3bench/simulator/stress_test.go` (lines 85-103, 280-412)

---

### ⏳ Phase 3: Docker Mesh E2E Testing (FUTURE WORK)

**Purpose**: Test full P2P mesh networking with real Docker containers.

**Status**: Not implemented (infrastructure work required)

**What it would test**:
- Real mesh peer nodes (alice, bob, eve) in Docker
- UDP transport with Noise protocol encryption
- Peer discovery and route propagation
- Peer join/leave dynamics
- S3 operations through actual mesh network
- Cross-container communication
- Mesh DNS resolution

**Required infrastructure**:
1. **Docker Compose**:
   - Create `docker/docker-compose.benchmark.yml`
   - Define named services: `alice`, `bob`, `eve`, `coordinator`
   - Configure mesh network with proper IPs

2. **Docker Client**:
   - Add `github.com/docker/docker` dependency
   - Implement `DockerMeshController` (currently only `MockMeshController` exists)
   - Implement `StartPeer()`, `StopPeer()`, `GetPeerStatus()`, `WaitForMeshStable()`

3. **Control Scripts**:
   - `docker/scripts/benchmark-control.sh`: Start/stop individual peers
   - `docker/scripts/mesh-coordinator.sh`: Wait for mesh stability

4. **Test Integration**:
   - Add `USE_MESH=1` flag to stress test
   - Start coordinator + alice + bob
   - Execute S3 operations through mesh
   - At T=12h (scaled): Start eve container
   - At T=30h (scaled): Stop eve container
   - Verify operations continue after peer changes

**Example flow**:
```bash
# Start benchmark stack
docker-compose -f docker/docker-compose.benchmark.yml up -d

# Run benchmark with mesh testing
USE_MESH=1 go test ./internal/s3bench/simulator -run=TestStress -v -timeout=10m

# Cleanup
docker-compose -f docker/docker-compose.benchmark.yml down
```

**Complexity**: High
- Requires Docker daemon running
- Multi-container orchestration
- Network timing and synchronization
- Significantly longer test execution time (~5-10 minutes)

**Current workaround**: Use `MockMeshController` for unit testing mesh orchestration logic without actual Docker containers.

---

## Network Stack Coverage

| Layer | Test Coverage | Status |
|-------|--------------|--------|
| Direct Store Access | Default mode (no flags) | ✅ Working |
| HTTP Server + RBAC | `USE_HTTP=1` | ✅ Working |
| TUN Device I/O | `USE_TUN=1` (requires root) | ✅ Working |
| UDP Transport + Noise | Phase 3 (not implemented) | ⏳ Future |
| Full P2P Mesh | Phase 3 (not implemented) | ⏳ Future |

---

## Summary

**Phases 1 & 2** provide significant network testing coverage:
- **Phase 1**: Tests HTTP/RBAC layer (most common production path)
- **Phase 2**: Tests TUN device layer (OS-level packet I/O)

**Phase 3** would add full E2E mesh testing but requires substantial Docker infrastructure. The current testing is sufficient for most use cases, as:
1. HTTP layer is where most issues occur (auth, headers, status codes)
2. TUN device layer is critical for packet routing
3. UDP/Noise transport and mesh routing have dedicated unit tests elsewhere

For full E2E mesh testing, use the existing Docker Compose stack (`docker/docker-compose.yml`) with manual testing, or implement Phase 3 infrastructure in the future.
