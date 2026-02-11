# TunnelMesh - Claude Code Instructions

## Project Overview

TunnelMesh is a P2P mesh networking tool written in Go that creates encrypted tunnels between nodes. It uses the Noise protocol (IKpsk2) with ChaCha20-Poly1305 encryption for secure communication.

**Key components:**
- **Coordinator**: Central server for peer discovery, IP allocation, DNS, and relay fallback
- **Peer**: Mesh peer with TUN interface for transparent IP routing
- **Transports**: UDP (primary, low-latency), SSH (fallback), WebSocket relay (last resort)

## Common Commands

```bash
# Build
make build              # Build binary
make release-all        # Cross-platform release builds

# Test
make test               # Run all tests (fast, no race detector - use for normal development)
make test-verbose       # Verbose test output
make test-race          # Run with race detector (ONLY use if debugging concurrency issues - slow)

# Lint
golangci-lint run       # Run linter
make fmt                # Format code

# Frontend Linting (requires Node.js 20+)
npm install             # Install dependencies (stylelint, biome)
npx stylelint "internal/coord/web/css/**/*.css"  # Lint CSS
npx biome check internal/coord/web/js/           # Lint JavaScript

# Docker
make docker-up          # Start full stack (server + clients + monitoring)
make docker-down        # Stop containers
make docker-logs        # Follow logs

# Deploy (Terraform/DigitalOcean)
make deploy-plan        # Preview changes
make deploy             # Apply infrastructure
make deploy-update      # Update binaries on nodes
```

## Directory Structure

```
cmd/                    # CLI entrypoints
  tunnelmesh/           # Main binary
internal/               # Core packages
  transport/            # SSH, UDP, relay transports
    udp/                # Noise protocol, encryption, handshake
  routing/              # Packet router, filter
  peer/                 # Peer peer logic, connection FSM
  coord/                # Coordinator server, API, relay
  tun/                  # TUN device (platform-specific)
  dns/                  # Mesh DNS resolver
  portmap/              # NAT-PMP, PCP, UPnP
  metrics/              # Prometheus metrics
pkg/proto/              # Protocol message definitions
terraform/              # DigitalOcean infrastructure
docker/                 # Container deployment
monitoring/             # Prometheus, Grafana, Loki configs
```

## Coding Conventions

### Go Patterns
- **Error wrapping**: Use `fmt.Errorf("context: %w", err)` for wrapped errors
- **Context propagation**: Pass `context.Context` through call chains, never store in structs
- **Resource cleanup**: Always use `defer` for cleanup; ensure goroutines have termination conditions
- **Interfaces**: Keep small, define at point of use (consumer-defined)

### Concurrency
- **Lock-free hot paths**: Use `atomic.Pointer` with copy-on-write for high-frequency reads (see `routing/router.go`)
- **Synchronization**: Prefer `sync.RWMutex` for read-heavy access, channels for coordination
- **Goroutine lifecycle**: Track with `sync.WaitGroup`, cancel via `context.Context`

### Testing
- **Coverage threshold**: 40% minimum (enforced in CI)
- **Race detection**: Always run `go test -race` before committing
- **Table-driven tests**: Preferred for exhaustive case coverage
- **Mocks**: Use interface-based mocks, not concrete type mocking

### Crypto (in transport/udp/)
- **Noise IKpsk2**: Initiator sends e, es, s, ss; responder sends e, ee, se
- **Nonce management**: 12-byte nonce from 64-bit counter, must never repeat
- **Constant-time ops**: Use `hmac.Equal` for MAC comparison
- **Key zeroing**: Zero ephemeral private keys after handshake

## Workflow

After implementing a feature or fix:

1. **Run tests**: `make test` (ensure all pass)
2. **Run linter**: `golangci-lint run` (fix any issues)
3. **Commit**: Create a commit with descriptive message
4. **Create branch**: Push to a feature branch
5. **Create PR**: Open pull request against `main`

## UI Panel System

The web dashboard uses an extensible panel system with RBAC-based visibility control.

### Panel Architecture

**Backend (`internal/auth/panel.go`)**:
- `PanelRegistry`: Dynamic panel registration with 13 built-in panels
- `PanelDefinition`: Panel metadata (id, name, tab, category, public, external)
- Panels can be marked as **public** (visible without authentication)
- External plugins can register panels via the API

**Frontend (`internal/coord/web/js/lib/panel.js`)**:
- UMD module for browser + Bun test compatibility
- Permission loading via `/api/user/permissions`
- Fail-secure: API errors result in no panel access
- External panel support via iframe + postMessage protocol

### Panel Permissions

Access control via existing RBAC system:
1. **Public flag**: If `panel.Public == true`, anyone can view
2. **Admin role**: Full access to all panels
3. **RoleBinding**: `role=panel-viewer, panel_scope=<panelID>`
4. **GroupBinding**: Same pattern, applied to groups

**Default groups**:
- `everyone`: visualizer, map, charts, s3, shares
- `all_admin_users`: peers, logs, wireguard, filter, dns, users, groups, bindings, docker

### Built-in Panel IDs

| Tab  | Panels                                                |
|------|-------------------------------------------------------|
| mesh | visualizer, map, charts, peers, logs, wireguard, filter, dns |
| data | s3, shares, users, groups, bindings, docker           |

### CSS Design Tokens

All styling uses CSS variables in `internal/coord/web/css/style.css`:

```css
--panel-border-radius: 0px;   /* Sharp corners throughout */
--btn-border-radius: 0px;
--modal-border-radius: 0px;
--input-border-radius: 0px;

/* Visualizer colors (for canvas rendering) */
--viz-node-online, --viz-node-offline, --viz-node-coordinator
--viz-edge-online, --viz-edge-offline, --viz-label-text
```

### API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/user/permissions` | Current user's accessible panels |
| GET | `/api/panels` | List all panels (filter: `?external=true`) |
| POST | `/api/panels` | Register external panel (admin only) |
| PATCH | `/api/panels/{id}` | Update panel metadata (admin only) |
| DELETE | `/api/panels/{id}` | Unregister external panel (admin only) |

### External Panel Development

External panels are loaded via iframe with postMessage:

```javascript
// Plugin -> Dashboard
window.parent.postMessage({
    type: 'tunnelmesh:panel:ready',
    panelId: 'my-plugin'
}, '*');

// Dashboard -> Plugin (on load)
{
    type: 'tunnelmesh:init',
    panelId: 'my-plugin',
    theme: 'dark',
    user: { id: '...', isAdmin: false }
}
```

## Docker Orchestration

TunnelMesh coordinators automatically detect and integrate with Docker when the Docker socket is available. No explicit configuration required.

### Configuration

Docker integration is **automatically enabled** when:
- The coordinator joins the mesh (`join_mesh` configured)
- Docker socket exists at `/var/run/docker.sock` (default)

**Optional configuration:**

```yaml
# coordinator.yaml - only if you need non-default settings
docker:
  socket: "unix:///var/run/docker.sock"  # Custom socket path
  auto_port_forward: false                # Disable automatic port forwarding
```

Join coordinator with token (server URL and auth token are CLI-only):
```bash
tunnelmesh join --token your-token --config coordinator.yaml
```

**Security Note**: Docker socket access grants root-equivalent privileges. Use with caution. Consider Docker rootless mode for enhanced security.

### Automatic Port Forwarding

When a container starts with published ports on a bridge network:
1. TunnelMesh creates temporary filter rules allowing access to those ports
2. Rules expire after 24 hours (container lifetime-based)
3. Rules are shown as "Temporary" in the filter panel
4. Host network containers are skipped (already have direct access)
5. **Event-driven**: Automatically detects containers started via any method (docker run, docker-compose, restart policies)

**Example:**
```bash
# Container with published port
docker run -d -p 8080:80 nginx

# TunnelMesh automatically creates:
# Port: 8080, Protocol: TCP, Action: Allow, Expires: <24h from now>
# This happens automatically via Docker events API - no manual refresh needed
```

### Docker Panel

The Docker panel (data tab) displays:
- **Container list**: Name, image, status, uptime, ports, network mode
- **Status badges**: Running (green), exited (red), other states
- **Control actions**: Start, stop, restart buttons (admin-only)
- **Port mappings**: Shows published host:container ports

**Panel visibility:**
- Hidden if Docker socket not found
- Admin-only by default
- Grantable via: `role=panel-viewer, panel_scope=docker`

### API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/docker/containers` | List all containers (query: `?status=running`) |
| GET | `/api/docker/containers/{id}` | Inspect specific container |
| POST | `/api/docker/containers/{id}/control` | Control container (`action: start\|stop\|restart`) |

**Control request:**
```json
POST /api/docker/containers/abc123/control
{
  "action": "restart"
}
```

### Prometheus Metrics

Docker container metrics are automatically exposed for Prometheus scraping:

```
docker_container_cpu_percent{peer, container_id, container_name, image}
docker_container_memory_bytes{peer, container_id, container_name, image}
docker_container_memory_percent{peer, container_id, container_name, image}
docker_container_disk_bytes{peer, container_id, container_name, image}
docker_container_status{peer, container_id, container_name, image}  # 1=running, 0=stopped
docker_container_info{peer, container_id, container_name, image, status, network_mode}
```

### Implementation Details

**Files:**
- `internal/docker/` - Docker manager, events watcher, port forwarding, metrics, stats persistence
- `internal/coord/docker.go` - Coordinator API handlers
- `internal/coord/web/js/docker.js` - Frontend panel implementation
- `internal/coord/s3/system.go` - S3 persistence for Docker stats

**Architecture:**
- Each coordinator/peer monitors its own local Docker daemon (no cross-peer aggregation)
- Event watcher streams Docker events API in real-time
- Automatically syncs port forwards on container start events (bridge networks only)
- Debounces rapid events (100ms window) to prevent duplicate processing
- Filter rules expire naturally via TTL mechanism (24h)
- **Stats persistence**: Collects comprehensive Docker stats every 30 seconds to S3

**Stats Persistence:**
- **Collection interval**:
  - Docker stats: 30 seconds
  - Network stats: 10 seconds (via heartbeat)
- **Storage location**: S3 system bucket at `stats/{peer_name}.{function}.json`
- **Naming convention**:
  - `stats/{peer_name}.docker.json` - Docker container stats (peer-side collection)
  - `stats/{peer_name}.network.json` - Network transmission stats (coordinator-side collection)
- **Data collected**:
  - **Docker stats**: Full `docker inspect` output, runtime stats (CPU%, memory, disk), Docker networks
  - **Network stats**: Bytes/packets sent/received rates, active tunnels, dropped packets
- **Format**: JSON with checksum validation for corruption detection
- **Architecture**:
  - Docker stats: Collected by each peer with Docker installed
  - Network stats: Collected by coordinator from all peer heartbeats

**Testing:**
- 20+ unit tests with mock Docker client
- TDD approach: tests written before implementation
- 100% coverage on core Docker manager functionality

## Skills

Three specialized skill files are available in `.claude/skills/`:
- `senior-network-engineer.md` - NAT traversal, transports, crypto protocols
- `sre.md` - Terraform, Docker, Prometheus/Grafana/Loki
- `senior-developer.md` - Go patterns, testing, security, UI
