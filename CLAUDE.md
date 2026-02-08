# TunnelMesh - Claude Code Instructions

## Project Overview

TunnelMesh is a P2P mesh networking tool written in Go that creates encrypted tunnels between nodes. It uses the Noise protocol (IKpsk2) with ChaCha20-Poly1305 encryption for secure communication.

**Key components:**
- **Coordinator**: Central server for peer discovery, IP allocation, DNS, and relay fallback
- **Peer**: Mesh node with TUN interface for transparent IP routing
- **Transports**: UDP (primary, low-latency), SSH (fallback), WebSocket relay (last resort)

## Common Commands

```bash
# Build
make build              # Build binary
make release-all        # Cross-platform release builds

# Test
make test               # Run all tests
make test-verbose       # Verbose test output
go test -race ./...     # Run with race detector

# Lint
golangci-lint run       # Run linter
make fmt                # Format code

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
  peer/                 # Peer node logic, connection FSM
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
- `all_admin_users`: peers, logs, wireguard, filter, dns, users, groups, bindings

### Built-in Panel IDs

| Tab  | Panels                                                |
|------|-------------------------------------------------------|
| mesh | visualizer, map, charts, peers, logs, wireguard, filter, dns |
| data | s3, shares, users, groups, bindings                   |

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

## Skills

Three specialized skill files are available in `.claude/skills/`:
- `senior-network-engineer.md` - NAT traversal, transports, crypto protocols
- `sre.md` - Terraform, Docker, Prometheus/Grafana/Loki
- `senior-developer.md` - Go patterns, testing, security, UI
