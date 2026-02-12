# Docker Deployment

> [!NOTE]
> This guide covers running TunnelMesh in Docker containers for development, testing, and production
> deployments. Containers need elevated privileges (NET_ADMIN) to create TUN interfaces.

## Quick Start

```bash
# Start the full mesh stack
cd docker
docker compose up -d

# View logs
docker compose logs -f

# Run connectivity tests
make docker-test

# Stop
docker compose down
```

## Building the Image

### Using Make

```bash
make docker-build
```

### Manual Build

```bash
docker build -t tunnelmesh:latest -f docker/Dockerfile .
```

## Docker Compose Stack

The included `docker-compose.yml` sets up a complete mesh environment with scalable coordinators for testing
chunk-level replication:

| Service | Description |
| --------- | ------------- |
| `coordinator` | Coordinator peer with admin UI (2 replicas by default) |
| `client` | Mesh peer (5 replicas by default) |
| `prometheus` | Metrics collection (one per coordinator) |
| `grafana` | Dashboards and visualization (one per coordinator) |
| `loki` | Log aggregation (one per coordinator) |
| `sd-generator` | Prometheus service discovery |
| `benchmarker` | Automated performance testing |

> [!NOTE]
> **Multi-coordinator architecture**: Each coordinator replica runs its own monitoring stack and uses tmpfs for
> ephemeral storage. Coordinators discover each other via peer registration and replicate S3 chunks peer-to-peer.
> All coordinators are equal peers with no primary/replica distinction.

### Starting the Stack

```bash
cd docker

# Start all services (2 coordinators by default)
docker compose up -d

# Start with custom number of coordinators
docker compose up -d --scale coordinator=3

# Scale coordinators after startup
make docker-scale-coords

# Scale clients
docker compose up -d --scale client=10
```

### Viewing Logs

```bash
# All services
docker compose logs -f

# Coordinator logs only
make docker-logs-coords

# Specific coordinator
docker compose logs -f coordinator

# View replication activity
docker compose logs coordinator | grep -i replication
```

### Accessing Services

| Service | URL | Notes |
| --------- | ----- | ------- |
| Coordination API | <http://localhost:8081> | Load-balanced across all coordinators |
| Admin Dashboard | <https://coordinator-node.tunnelmesh/> | Mesh-only (any coordinator) |
| Grafana | <http://localhost:3000> | Metrics dashboards (first coordinator) |
| Prometheus | <http://localhost:9090> | Raw metrics (first coordinator) |

**Note:** The admin panel is accessible from any coordinator node within the mesh. Monitoring stacks
(Grafana/Prometheus) are exposed from the first coordinator replica via port mapping. Each coordinator runs its own
isolated monitoring stack with tmpfs storage.

## Container Requirements

> [!WARNING]
> **Elevated privileges required**: TunnelMesh containers need NET_ADMIN capability and access to
> /dev/net/tun to create network interfaces. This is unavoidable for VPN/tunnel software.

TunnelMesh containers need elevated privileges for TUN interface creation:

```yaml
cap_add:
  - NET_ADMIN
devices:
  - /dev/net/tun:/dev/net/tun
```

### Minimal Container Configuration

```yaml
services:
  tunnelmesh:
    image: ghcr.io/zombar/tunnelmesh:latest
    cap_add:
      - NET_ADMIN
    devices:
      - /dev/net/tun:/dev/net/tun
    volumes:
      - ./config.yaml:/etc/tunnelmesh/config.yaml:ro
    command: ["join", "--config", "/etc/tunnelmesh/config.yaml"]
```

## Configuration

> **Note:** Context management (`tunnelmesh context`) is designed for host-based installations
> where you run multiple meshes from one machine. In Docker deployments, each container is
> typically dedicated to a single mesh and receives its config directly via volume mount or
> environment variables.

### Coordinator Configuration

**Start coordinator (automatically bootstraps when no server URL):**

```bash
docker run tunnelmesh join --token your-secure-token
```

Optional `coordinator.yaml` for custom settings:

```yaml
name: "coordinator"

# Coordinator services (auto-enabled when no server URL provided)
# Admin panel, relay, and S3 are always enabled (ports: 443, 9000)
coordinator:
  listen: ":8080"  # Coordination API (default: ":8443")
  data_dir: "/var/lib/tunnelmesh"
```

### Peer Configuration

Create `docker/config/peer.yaml`:

```yaml
name: "peer-1"

# DNS is always enabled
dns:
  listen: "127.0.0.53:5353"
```

Start peer:

```bash
docker run tunnelmesh join coordinator:8080 --token your-secure-token --config /etc/tunnelmesh/peer.yaml
```

## Network Modes

### Bridge Network (Default)

Services communicate over a Docker bridge network:

```yaml
networks:
  mesh-control:
    driver: bridge
    ipam:
      config:
        - subnet: 172.28.0.0/24
```

### Host Network

For production deployments requiring full network access:

```yaml
services:
  tunnelmesh:
    network_mode: host
    # Note: No port mapping needed with host network
```

### Shared Network Namespace

Monitoring services share the coordinator's network to access mesh IPs:

```yaml
services:
  prometheus:
    network_mode: "service:coordinator"
```

**Note:** Each coordinator replica runs its own monitoring stack. All monitoring containers share their respective
coordinator's network namespace.

## Volumes

### Coordinator Storage

Coordinators use **tmpfs** (ephemeral RAM-based storage) for testing replication:

```yaml
volumes:
  - type: tmpfs
    target: /var/lib/tunnelmesh
    tmpfs:
      size: 268435456  # 256MB
  - type: tmpfs
    target: /root/.tunnelmesh
```

> [!WARNING]
> **Ephemeral storage**: All coordinator data (S3 chunks, SSH keys, metrics) is lost on container restart. This is
> intentional for replication testing where you want a clean slate. For production deployments, use named volumes or
> host mounts instead of tmpfs.

### Monitoring Stack Volumes

Each monitoring service has persistent storage:

```yaml
volumes:
  prometheus-data:     # Prometheus TSDB
  grafana-data:        # Grafana configuration
  loki-data:           # Log storage
  benchmark-results:   # Benchmark JSON output
```

### Accessing Benchmark Results

```bash
# List results
docker compose exec benchmarker ls -la /results/

# Copy to host (find the benchmarker container ID first)
docker ps | grep benchmarker
docker cp <container-id>:/results ./benchmark-results/

# View latest result
docker compose exec benchmarker cat /results/benchmark_*.json | jq . | tail -50
```

## Health Checks

All services include health checks:

```yaml
healthcheck:
  test: ["CMD", "curl", "-sf", "http://localhost:8080/health"]
  interval: 5s
  timeout: 3s
  retries: 5
  start_period: 3s
```

Check service health:

```bash
docker compose ps
```

## Running Tests

### Connectivity Tests

```bash
# From host
make docker-test

# Manual ping test (pick any coordinator)
docker compose exec coordinator ping -c 3 client-1.tunnelmesh
```

### Benchmark Tests

```bash
# Run single benchmark (use benchmarker container)
docker compose exec benchmarker tunnelmesh benchmark client-1 --size 50MB

# View automated benchmark results
docker compose logs benchmarker
```

### Replication Tests

Test chunk-level replication between coordinators:

```bash
# 1. Create bucket with replication factor 2
export TUNNELMESH_TOKEN=$(openssl rand -hex 32)
curl -X PUT http://localhost:8081/test-bucket \
  -H "Authorization: Bearer $TUNNELMESH_TOKEN" \
  -H "X-Replication-Factor: 2"

# 2. Upload test file (will be chunked and replicated)
echo "test data" > test.txt
aws s3 cp test.txt s3://test-bucket/test.txt \
  --endpoint-url http://localhost:9000

# 3. Check replication in coordinator logs
docker compose logs coordinator | grep -i "replication\|chunk"

# 4. Verify chunk distribution
curl -H "Authorization: Bearer $TUNNELMESH_TOKEN" \
  http://localhost:8081/api/admin/buckets/test-bucket

# 5. Test distributed reads (stop one coordinator)
docker ps | grep coordinator  # Note one coordinator ID
docker stop <coordinator-id>
aws s3 cp s3://test-bucket/test.txt - --endpoint-url http://localhost:9000
# Should succeed if replication worked
```

## Troubleshooting

### TUN Device Issues

```text
Error: cannot create TUN device
```

Ensure the container has proper privileges:

```bash
# Check capabilities (pick any coordinator container)
docker ps | grep coordinator
docker inspect <coordinator-container-id> | jq '.[0].HostConfig.CapAdd'

# Should include: ["NET_ADMIN"]
```

### DNS Resolution

```text
Error: cannot resolve peer
```

Check mesh DNS is working:

```bash
docker compose exec coordinator dig peer-1.tunnelmesh @localhost
```

### Container Networking

```bash
# Check container IPs (pick any coordinator)
docker compose exec coordinator ip addr

# Check routes
docker compose exec coordinator ip route

# Check mesh connectivity
docker compose exec coordinator tunnelmesh status
```

### Coordinator Discovery

Verify coordinators can discover each other:

```bash
# View coordinator registration logs
docker compose logs coordinator | grep -i "coordinator.*registered"

# Should show each coordinator discovering the others
```

### Logs

```bash
# View coordinator logs
make docker-logs-coords

# View peer discovery
docker compose logs coordinator 2>&1 | grep -i peer

# View replication activity
docker compose logs coordinator 2>&1 | grep -i replication
```

## Production Considerations

> [!TIP]
> **Production checklist**: Use resource limits, restart policies, log rotation, secrets management,
> and health checks. Don't run containers as root where possible (though NET_ADMIN requires root).

### Resource Limits

```yaml
services:
  server:
    deploy:
      resources:
        limits:
          cpus: '2'
          memory: 1G
        reservations:
          cpus: '0.5'
          memory: 256M
```

### Restart Policy

```yaml
services:
  server:
    restart: unless-stopped
```

### Logging

```yaml
services:
  server:
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "3"
```

> [!CAUTION]
> **Security best practices**: Never hardcode auth tokens in compose files. Use Docker secrets or
> environment files with restricted permissions. Limit port exposure to only what's necessary.

### Security

- Use secrets management for `auth_token`
- Run containers as non-root where possible
- Use read-only root filesystem for static configs
- Limit network exposure with explicit port mappings

## Example: Minimal Production Setup

```yaml
version: "3.8"

services:
  coordinator:
    image: ghcr.io/zombar/tunnelmesh:latest
    restart: unless-stopped
    cap_add:
      - NET_ADMIN
    devices:
      - /dev/net/tun:/dev/net/tun
    ports:
      - "8080:8080"
    volumes:
      - ./coordinator.yaml:/etc/tunnelmesh/coordinator.yaml:ro
    command: ["join", "--config", "/etc/tunnelmesh/coordinator.yaml"]
    healthcheck:
      test: ["CMD", "curl", "-sf", "http://localhost:8080/health"]
      interval: 30s
      timeout: 5s
      retries: 3
```
