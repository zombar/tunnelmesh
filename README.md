![Admin Dashboard](docs/images/tunnelmesh_banner.webp)

> [!WARNING]
> **Expect breaking changes**: Parts of this early experimental project are in flux, expect some flake.

# TunnelMesh

[![CI](https://github.com/zombar/tunnelmesh/actions/workflows/ci.yml/badge.svg)](https://github.com/zombar/tunnelmesh/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/zombar/tunnelmesh/branch/main/graph/badge.svg)](https://codecov.io/gh/zombar/tunnelmesh)
[![Dependabot](https://img.shields.io/badge/dependabot-enabled-blue?logo=dependabot)](https://github.com/zombar/tunnelmesh/network/updates)
[![Dependencies](https://img.shields.io/badge/dependencies-up%20to%20date-brightgreen)](https://github.com/zombar/tunnelmesh/network/dependencies)

A peer-to-peer mesh networking tool that creates encrypted tunnels between nodes. TunnelMesh enables direct, secure
communication between peers in a distributed topology without requiring a traditional VPN or centralized traffic
routing.

## Features

- **P2P Encrypted Tunnels** - Direct connections between peers using pluggable transports
- **Pluggable Transport Layer** - Supports SSH, UDP (WireGuard-like), and WebSocket relay transports with automatic
  fallback
- **Coordinator Peers** - Admin peers that provide discovery, IP allocation, and NAT traversal coordination (not traffic routers)
- **Exit Peers** - Split-tunnel VPN routing: route internet traffic through designated peers while keeping mesh traffic
  direct
- **TUN Interface** - Virtual network interface for transparent IP routing
- **Built-in DNS** - Local resolver for mesh hostnames (e.g., `node.tunnelmesh`)
- **Network Monitoring** - Automatic detection of network changes with re-connection
- **NAT Traversal** - UDP hole-punching with STUN-like endpoint discovery, plus relay fallback
- **Multi-Platform** - Linux, macOS, and Windows support
- **Admin Dashboard** - Web interface showing mesh status, peers, traffic statistics, and per-peer transport controls
- **Node Location Map** - Optional geographic visualization of mesh peers (requires `--locations` flag)
- **Unified Architecture** - All nodes are peers; coordinators are peers with admin services enabled
- **High Performance** - Zero-copy packet forwarding with lock-free routing table
- **Internal Packet Filter** - Port-based firewall with per-peer rules, configurable via config, CLI, or admin UI

### What Requires Admin Access?

> [!NOTE]
> TunnelMesh separates **network functionality** (no admin required) from **configuration operations**
> (admin only). Most features work without admin access - you only need admin rights to configure
> and manage the mesh.

**Works Without Admin** (all peers):
- Join mesh and establish encrypted tunnels
- Route traffic through the mesh
- Use mesh DNS resolution
- Access services on other peers
- Speed test other peers
- Use exit peers for VPN
- Access S3 buckets (with RBAC permissions)

**Requires Admin Access**:
- View peer stats and connection details
- Configure packet filter rules
- Manage WireGuard clients
- Create/delete S3 buckets and file shares
- Manage users, groups, and RBAC bindings
- Control Docker containers (if enabled)

**Admin Configuration**:
```yaml
coordinator:
  admin_peers: ["alice", "a1b2c3d4e5f6g7h8"]  # Names or peer IDs (16 hex chars)
```

See [Admin Guide](docs/ADMIN.md) for complete details.

## Getting Started

For a complete step-by-step setup guide including downloading releases, configuring servers and peers, and installing as
a system service, see the **[Getting Started Guide](docs/GETTING_STARTED.md)**.

## Documentation

- **[Getting Started Guide](docs/GETTING_STARTED.md)** - Installation, configuration, contexts, and running as a service
- **[CLI Reference](docs/CLI.md)** - Complete command-line reference including context management
- **[WireGuard Integration](docs/WIREGUARD.md)** - Connect mobile devices and standard WireGuard clients
- **[S3 Storage](docs/S3_STORAGE.md)** - S3-compatible object storage and file shares
- **[NFS File Sharing](docs/NFS.md)** - Mount file shares as network drives
- **[Docker Deployment](docs/DOCKER.md)** - Running TunnelMesh in containers for development and production
- **[Cloud Deployment](docs/CLOUD_DEPLOYMENT.md)** - Deploy to DigitalOcean with Terraform (includes deployment scenarios)
- **[Benchmarking & Stress Testing](docs/BENCHMARKING.md)** - Measure throughput, latency, and chaos testing
- **[Internal Packet Filter](docs/INTERNAL_PACKET_FILTER.md)** - Port-based firewall with per-peer rules and metrics

## Architecture

```
┌─────────────────┐                      ┌─────────────────┐
│   Peer Node A   │                      │   Peer Node B   │
│   (10.42.0.1)   │                      │   (10.42.0.2)   │
│                 │  Encrypted Tunnel    │                 │
│  ┌───────────┐  │◄────────────────────►│  ┌───────────┐  │
│  │ TUN Device│  │  (SSH/UDP/Relay)     │  │ TUN Device│  │
│  │  Router   │  │                      │  │  Router   │  │
│  │ Forwarder │  │                      │  │ Forwarder │  │
│  │ Transport │  │                      │  │ Transport │  │
│  └───────────┘  │                      │  └───────────┘  │
└────────┬────────┘                      └────────┬────────┘
         │                                        │
         │  Register/Heartbeat/Discovery          │
         │  Hole-punch Coordination               │
         │                                        │
         └──────────────┬─────────────────────────┘
                        │
                        ▼
              ┌─────────────────┐
              │  Coordination   │
              │     Server      │
              │                 │
              │ • Peer Registry │
              │ • IP Allocation │
              │ • DNS Records   │
              │ • Hole-punch    │
              │ • Admin UI      │
              │ • WebSocket     │
              │   Relay         │
              └─────────────────┘
```

**Key points:**
- **Unified Architecture**: All nodes are peers; coordinators are admin peers with services enabled
- Traffic flows directly between peers via encrypted tunnels
- Multiple transport options: SSH, UDP (ChaCha20-Poly1305), or WebSocket relay
- Transport negotiation with automatic fallback (UDP → SSH → Relay)
- Coordinator peers handle discovery, registration, and NAT traversal coordination
- Each peer runs a TUN interface for transparent IP routing
- Peers behind NAT use hole-punching or relay as fallback

### Mobile Client Support (WireGuard)

Standard WireGuard clients on mobile devices can connect to the mesh via a WireGuard concentrator peer. Manage clients
through the admin panel with QR codes for easy mobile setup.

```
                                    ┌─────────────────────────────────┐
                                    │      Coordination Server        │
                                    │    (Stateless, App Platform)    │
                                    │                                 │
                                    │  - WG Client Management API     │
                                    │  - Admin Panel UI               │
                                    │  - Config sync endpoint         │
                                    └──────────────┬──────────────────┘
                                                   │
                         ┌─────────────────────────┼─────────────────────────┐
                         │                         │                         │
                    ┌────▼────┐              ┌─────▼─────┐             ┌─────▼─────┐
                    │ Peer A  │              │    WG     │             │  Peer B   │
                    │ Server  │◄────────────►│Concentrator◄───────────►│  Desktop  │
                    └─────────┘   Tunnel     │  (Peer)   │   Tunnel    └───────────┘
                                             │  wg0 intf │
                                             └─────▲─────┘
                                                   │ WireGuard Protocol
                                    ┌──────────────┼──────────────┐
                                    │              │              │
                               ┌────▼────┐   ┌────▼────┐   ┌─────▼────┐
                               │   📱    │   │   📱    │   │    💻    │
                               │ iPhone  │   │ Android │   │  Laptop  │
                               │   App   │   │   App   │   │  Client  │
                               └─────────┘   └─────────┘   └──────────┘
```

**Features:**
- Scan QR code in admin panel to configure mobile device
- Full mesh access from any WireGuard client
- Clients get mesh DNS names (e.g., `iphone.tunnelmesh`)
- Managed via coordination server admin panel

### Admin Interface

The admin interface is **only accessible from within the mesh** via HTTPS on the coordinator's mesh IP. Access it at
`https://this.tm/` from any mesh peer.

When you run `tunnelmesh join`, the mesh CA certificate is automatically fetched and you'll be prompted to install it in
your system trust store. This allows HTTPS connections to mesh services without browser warnings.

## Configuration

TunnelMesh uses a unified configuration model where all nodes are peers. Coordinators are simply peers with `coordinator.enabled: true` that provide discovery and admin services.

Generate configs with `tunnelmesh init --peer` or see the full [peer.yaml.example](peer.yaml.example) for all options.

### Coordinator Configuration

**Generate a secure token for your mesh:**
```bash
TOKEN=$(openssl rand -hex 32)
# Save token securely for peers:
echo "$TOKEN" > ~/.tunnelmesh/mesh-token.txt
chmod 600 ~/.tunnelmesh/mesh-token.txt
```

**Bootstrap coordinator (first node):**

When you run `tunnelmesh join` without a server URL, it automatically bootstraps as a coordinator:

```bash
export TUNNELMESH_TOKEN=$(openssl rand -hex 32)
tunnelmesh join
```

This creates a new mesh with default coordinator settings (admin panel on :443, S3 on :9000, relay auto-enabled).

For custom configuration:

```yaml
name: "coordinator"

# Coordinator services (auto-enabled when no server URL provided)
coordinator:
  listen: ":8443"  # Coordination API listen address (default: ":8443")
  data_dir: "/var/lib/tunnelmesh"  # Data directory for persistence

  monitoring:
    prometheus_url: "http://localhost:9090"
    grafana_url: "http://localhost:3000"
```

### Regular Peer Configuration

**Join existing mesh (use same token as coordinator):**
```bash
export TUNNELMESH_TOKEN="a4f8b2c9d3e7f1a2b5c8d4e9f2a3b6c7d8e1f4a5b2c9d6e3f7a1b4c8d5e2f9a6"
tunnelmesh join coord.example.com:8443 --config peer.yaml
```

```yaml
name: "mynode"

# DNS is always enabled. Optional configuration:
dns:
  listen: "127.0.0.53:5353"  # DNS listen address (default)
  aliases:  # Custom DNS aliases for this peer
    - "nas"
    - "homeserver"
```

### Transport Layer

TunnelMesh supports multiple transport types with automatic negotiation and fallback:

| Transport | Description | Use Case |
| ----------- | ------------- | ---------- |
| **UDP** | WireGuard-like encrypted UDP (default) | Lower latency, better throughput, NAT traversal |
| **SSH** | SSH-based tunnels | Reliable, works through most firewalls |
| **Relay** | WebSocket through coordination server | Fallback when direct connection fails |

The default transport order is: UDP → SSH → Relay. The system automatically negotiates the best available transport.

**Transport features:**
- Automatic fallback: If the preferred transport fails, the next one is tried
- Per-peer preferences: Configure different transports for specific peers via admin UI
- NAT traversal: Built-in STUN-like endpoint discovery and UDP hole-punching
- Zero-copy forwarding: Optimized packet path for high throughput

### Exit Peers (Split-Tunnel VPN)

Route internet traffic through a designated peer while keeping mesh-to-mesh traffic direct. This is useful for:

- Accessing geo-restricted content through a peer in another region
- Privacy: route external traffic through a trusted exit point
- Compliance: ensure internet traffic egresses from a specific location

```
┌─────────────────┐                      ┌─────────────────┐
│   Client Peer   │                      │   Exit Peer     │
│   (10.42.0.1)   │                      │   (10.42.0.2)   │
│                 │                      │                 │
│  Internet ──────┼──── Tunnel ─────────►│──► Internet     │
│  traffic        │  (encrypted)         │   (NAT)         │
│                 │                      │                 │
│  Mesh traffic ──┼──── Direct ─────────►│  Other peers    │
└─────────────────┘                      └─────────────────┘
```

**On the exit peer** (the peer that will forward internet traffic):

```bash
tunnelmesh join --allow-exit-traffic
```

Or in config:

```yaml
allow_exit_traffic: true
```

**On the client** (the peer that wants to route through the exit):

```bash
tunnelmesh join --exit-node exit-peer-name
```

Or in config:

```yaml
exit_peer: "exit-peer-name"
```

TunnelMesh automatically configures:

- **Exit node**: IP forwarding and NAT/masquerade rules
- **Client**: Default routes (0.0.0.0/1 and 128.0.0.0/1) through the TUN interface

This works on Linux and macOS. On Windows, manual route configuration may be required.

### Internal Packet Filter

Control which ports are accessible on each peer with a 4-layer rule system. Rules from the coordinator, peer config,
CLI/admin panel, and auto-generated service ports are merged, with the most restrictive rule winning.

```yaml
# peer.yaml - local filter rules
filter:
  rules:

    - port: 22
      protocol: tcp
      action: allow

    - port: 3306
      protocol: tcp
      action: deny
      source_peer: untrusted-peer  # Block specific peer
```

```bash
# CLI commands
tunnelmesh filter list
tunnelmesh filter add --port 80 --protocol tcp --action allow
tunnelmesh filter add --port 22 --action deny --source-peer badpeer
```

See **[Internal Packet Filter Guide](docs/INTERNAL_PACKET_FILTER.md)** for full documentation including coordinator
rules, metrics, and alerts.

### Config File Locations

The tool searches for config files in the following order:

`~/.tunnelmesh/config.yaml`, `tunnelmesh.yaml`, `peer.yaml`

## Security Best Practices

### Token Storage and Management

TunnelMesh authentication tokens are passed via the `TUNNELMESH_TOKEN` environment variable, never via CLI flags
or config files (for security). Follow these best practices for production deployments:

#### Environment Variables

Store tokens in environment variables instead of passing them directly:

```bash
# Set in shell session
export TUNNELMESH_TOKEN="a4f8b2c9d3e7f1a2b5c8d4e9f2a3b6c7d8e1f4a5b2c9d6e3f7a1b4c8d5e2f9a6"

# Join using environment variable
tunnelmesh join coord.example.com:8443
```

#### systemd Environment Files

For services managed by systemd, use environment files with restricted permissions:

```bash
# Create environment file
sudo mkdir -p /etc/tunnelmesh
sudo tee /etc/tunnelmesh/env.conf > /dev/null <<EOF
TUNNELMESH_TOKEN=a4f8b2c9d3e7f1a2b5c8d4e9f2a3b6c7d8e1f4a5b2c9d6e3f7a1b4c8d5e2f9a6
EOF
sudo chmod 600 /etc/tunnelmesh/env.conf
sudo chown root:root /etc/tunnelmesh/env.conf

# Use in systemd service (automatically configured when using `tunnelmesh service install`)
# The service will read the token from the environment file
```

The `tunnelmesh service install` command automatically configures the service to use environment files on systemd-based
systems.

#### Secret Management Systems

For larger deployments, integrate with centralized secret management:

**HashiCorp Vault:**

```bash
# Store token in Vault
vault kv put secret/tunnelmesh token="a4f8b2c9d3e7f1a2b5c8d4e9f2a3b6c7d8e1f4a5b2c9d6e3f7a1b4c8d5e2f9a6"

# Retrieve and use
export TUNNELMESH_TOKEN=$(vault kv get -field=token secret/tunnelmesh)
tunnelmesh join coord.example.com:8443
```

**AWS Secrets Manager:**

```bash
# Store token
aws secretsmanager create-secret \
  --name tunnelmesh/auth-token \
  --secret-string "a4f8b2c9d3e7f1a2b5c8d4e9f2a3b6c7d8e1f4a5b2c9d6e3f7a1b4c8d5e2f9a6"

# Retrieve and use
export TUNNELMESH_TOKEN=$(aws secretsmanager get-secret-value \
  --secret-id tunnelmesh/auth-token \
  --query SecretString \
  --output text)
tunnelmesh join coord.example.com:8443
```

#### Token Rotation

Rotate authentication tokens periodically to minimize exposure from compromised tokens.

**Note:** Token rotation currently requires brief downtime to update all nodes simultaneously. Multi-token
support for graceful rotation is planned (see issue tracker for roadmap).

**Rotation process:**

1. **Generate new token:**

   ```bash
   NEW_TOKEN=$(openssl rand -hex 32)
   ```

2. **Update all nodes** - During maintenance window, update coordinator and all peers with new token:

   ```bash
   export TUNNELMESH_TOKEN="$NEW_TOKEN"
   # Restart coordinator and all peer services
   ```

#### File Permissions

Always protect config files and token files with restrictive permissions:

```bash
chmod 600 ~/.tunnelmesh/config.yaml
chmod 600 ~/.tunnelmesh/mesh-token.txt
```

On multi-user systems, consider using system-level config directories with root ownership:

```bash
sudo mkdir -p /etc/tunnelmesh
sudo chmod 755 /etc/tunnelmesh
sudo chown root:root /etc/tunnelmesh
```

## CLI Quick Reference

```bash
# Coordinator setup
tunnelmesh init --peer              # Generate config with coordinator.enabled: true
tunnelmesh join                     # Start as coordinator + peer
# Admin access requires coordinator configuration

# Configure admin peers in coordinator.yaml:
# coordinator:
#   admin_peers: ["alice", "bob"]  # Match by peer name OR peer ID
#
# Peer IDs are preferred (more secure, immutable):
#   admin_peers: ["a1b2c3d4e5f6g7h8"]  # SHA256 of SSH key (first 8 bytes = 16 hex chars)
#
# View your peer ID: tunnelmesh status

# Peer setup (joining existing mesh)
export TUNNELMESH_TOKEN="your-token"
tunnelmesh join coord.example.com:8443 --context work
# User identity derived from SSH key - no separate registration needed

# Manage contexts
tunnelmesh context list            # List all contexts
tunnelmesh context use work        # Switch active context

# Check status
tunnelmesh status                  # Connection status
tunnelmesh peers                   # List all peers

# S3 bucket management
tunnelmesh buckets list            # List buckets
tunnelmesh buckets create my-data  # Create bucket
tunnelmesh buckets objects my-data # List objects

# System service
sudo tunnelmesh service install
sudo tunnelmesh service start
```

See **[CLI Reference](docs/CLI.md)** for complete documentation, all flags, and walkthroughs.

## Docker Deployment

Run TunnelMesh in containers for development, testing, or production. See the **[Docker Deployment
Guide](docs/DOCKER.md)** for complete documentation.

```bash
cd docker
docker compose up -d        # Start the full mesh stack
docker compose logs -f      # View logs
make docker-test            # Run connectivity tests
```

## Cloud Deployment

Deploy to DigitalOcean App Platform with Terraform. See the **[Cloud Deployment Guide](docs/CLOUD_DEPLOYMENT.md)** for
complete documentation.

```bash
cd terraform
cp terraform.tfvars.example terraform.tfvars
export TF_VAR_do_token="dop_v1_xxx"
terraform init && terraform apply
```

## Development

### Running Tests

```bash
make test           # Run tests
make test-verbose   # Verbose output
make test-coverage  # With coverage report
```

### Code Quality

```bash
make lint  # Run golangci-lint
make fmt   # Format code
```

### Development Servers

```bash
make dev-server  # Build and run server
make dev-peer    # Build and run peer (with sudo)
```

## License

MIT License - see [LICENSE](LICENSE) for details.
