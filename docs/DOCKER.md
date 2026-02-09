# Docker Deployment

This guide covers running TunnelMesh in Docker containers for development, testing, and production deployments.

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

The included `docker-compose.yml` sets up a complete mesh environment:

| Service | Description |
|---------|-------------|
| `server` | Coordination server with admin UI |
| `client` | Mesh peer (5 replicas by default) |
| `prometheus` | Metrics collection |
| `grafana` | Dashboards and visualization |
| `loki` | Log aggregation |
| `sd-generator` | Prometheus service discovery |
| `benchmarker` | Automated performance testing |

### Starting the Stack

```bash
cd docker

# Start all services
docker compose up -d

# Start specific services
docker compose up -d server client

# Scale clients
docker compose up -d --scale client=10
```

### Viewing Logs

```bash
# All services
docker compose logs -f

# Specific service
docker compose logs -f server

# Last 100 lines
docker compose logs --tail=100 server
```

### Accessing Services

| Service | URL | Notes |
|---------|-----|-------|
| Coordination API | http://localhost:8081 | Peers connect here |
| Admin Dashboard | https://server-peer.tunnelmesh/ | Mesh-only (requires joining the mesh) |
| Grafana | https://server-peer.tunnelmesh/grafana/ | Metrics dashboards (mesh-only) |
| Prometheus | https://server-peer.tunnelmesh/prometheus/ | Raw metrics (mesh-only) |

**Note:** The admin panel and monitoring tools are only accessible from within the mesh network for security.

## Container Requirements

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

> **Note:** Context management (`tunnelmesh context`) is designed for host-based installations where you run multiple meshes from one machine. In Docker deployments, each container is typically dedicated to a single mesh and receives its config directly via volume mount or environment variables.

### Server Configuration

Create `docker/config/server.yaml`:

```yaml
name: "server"
listen: ":8080"
auth_token: "your-secure-token"
admin:
  enabled: true
```

### Peer Configuration

Create `docker/config/peer.yaml`:

```yaml
name: "peer-1"
server: "http://server:8080"
auth_token: "your-secure-token"
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

Monitoring services share the server's network to access mesh IPs:

```yaml
services:
  prometheus:
    network_mode: "service:server"
```

## Volumes

### Persistent Data

```yaml
volumes:
  metrics-data:        # Stats history for dashboard
  prometheus-data:     # Prometheus TSDB
  grafana-data:        # Grafana configuration
  loki-data:           # Log storage
  benchmark-results:   # Benchmark JSON output
```

### Accessing Benchmark Results

```bash
# List results
docker compose exec server ls -la /results/

# Copy to host
docker cp tunnelmesh-server:/results ./benchmark-results/

# View latest result
docker compose exec server cat /results/benchmark_*.json | jq . | tail -50
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

# Manual ping test
docker compose exec server ping -c 3 client-1.tunnelmesh
```

### Benchmark Tests

```bash
# Run single benchmark
docker compose exec server tunnelmesh benchmark client-1 --size 50MB

# View automated benchmark results
docker compose logs benchmarker
```

## Troubleshooting

### TUN Device Issues

```
Error: cannot create TUN device
```

Ensure the container has proper privileges:

```bash
# Check capabilities
docker inspect tunnelmesh-server | jq '.[0].HostConfig.CapAdd'

# Should include: ["NET_ADMIN"]
```

### DNS Resolution

```
Error: cannot resolve peer
```

Check mesh DNS is working:

```bash
docker compose exec server dig peer-1.tunnelmesh @localhost
```

### Container Networking

```bash
# Check container IPs
docker compose exec server ip addr

# Check routes
docker compose exec server ip route

# Check mesh connectivity
docker compose exec server tunnelmesh status
```

### Logs

```bash
# Server logs with debug level
docker compose exec server tunnelmesh serve --log-level debug

# View peer discovery
docker compose logs server 2>&1 | grep -i peer
```

## Production Considerations

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
      - ./server.yaml:/etc/tunnelmesh/server.yaml:ro
    command: ["serve", "--config", "/etc/tunnelmesh/server.yaml"]
    healthcheck:
      test: ["CMD", "curl", "-sf", "http://localhost:8080/health"]
      interval: 30s
      timeout: 5s
      retries: 3
```
