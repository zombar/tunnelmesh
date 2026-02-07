# TunnelMesh

[![CI](https://github.com/zombar/tunnelmesh/actions/workflows/ci.yml/badge.svg)](https://github.com/zombar/tunnelmesh/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/zombar/tunnelmesh/branch/main/graph/badge.svg)](https://codecov.io/gh/zombar/tunnelmesh)

A peer-to-peer mesh networking tool that creates encrypted tunnels between nodes. TunnelMesh enables direct, secure communication between peers in a distributed topology without requiring a traditional VPN or centralized traffic routing.

## Features

- **P2P Encrypted Tunnels** - Direct connections between peers using pluggable transports
- **Pluggable Transport Layer** - Supports SSH, UDP (WireGuard-like), and WebSocket relay transports with automatic fallback
- **Coordination Server** - Central hub for peer discovery, IP allocation, and NAT traversal coordination (not a traffic router)
- **Exit Nodes** - Split-tunnel VPN routing: route internet traffic through designated peers while keeping mesh traffic direct
- **TUN Interface** - Virtual network interface for transparent IP routing
- **Built-in DNS** - Local resolver for mesh hostnames (e.g., `node.tunnelmesh`)
- **Network Monitoring** - Automatic detection of network changes with re-connection
- **NAT Traversal** - UDP hole-punching with STUN-like endpoint discovery, plus relay fallback
- **Multi-Platform** - Linux, macOS, and Windows support
- **Admin Dashboard** - Web interface showing mesh status, peers, traffic statistics, and per-peer transport controls
- **Node Location Map** - Optional geographic visualization of mesh nodes (requires `--locations` flag)
- **Server-as-Client** - Coordination server can also participate as a mesh node
- **High Performance** - Zero-copy packet forwarding with lock-free routing table

![Admin Dashboard](docs/images/admin-dashboard.webp)

## Getting Started

For a complete step-by-step setup guide including downloading releases, configuring servers and peers, and installing as a system service, see the **[Getting Started Guide](docs/GETTING_STARTED.md)**.

## Documentation

- **[Getting Started Guide](docs/GETTING_STARTED.md)** - Installation, configuration, and running as a service
- **[CLI Reference](docs/CLI.md)** - Complete command-line reference with examples and walkthroughs
- **[WireGuard Integration](docs/WIREGUARD.md)** - Connect mobile devices and standard WireGuard clients
- **[Docker Deployment](docs/DOCKER.md)** - Running TunnelMesh in containers for development and production
- **[Cloud Deployment](docs/CLOUD_DEPLOYMENT.md)** - Deploy to DigitalOcean with Terraform (includes deployment scenarios)
- **[Benchmarking & Stress Testing](docs/BENCHMARKING.md)** - Measure throughput, latency, and chaos testing

## Architecture

```
┌─────────────────┐                      ┌─────────────────┐
│   Peer Node A   │                      │   Peer Node B   │
│   (172.30.0.1)   │                      │   (172.30.0.2)   │
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
- Traffic flows directly between peers via encrypted tunnels
- Multiple transport options: SSH, UDP (ChaCha20-Poly1305), or WebSocket relay
- Transport negotiation with automatic fallback (UDP → SSH → Relay)
- The coordination server handles discovery, registration, and NAT traversal coordination
- Each peer runs a TUN interface for transparent IP routing
- Peers behind NAT use hole-punching or relay as fallback

### Mobile Client Support (WireGuard)

Standard WireGuard clients on mobile devices can connect to the mesh via a WireGuard concentrator peer. Manage clients through the admin panel with QR codes for easy mobile setup.

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

When the coordination server runs with `join_mesh` configured, the admin interface is accessible only from within the mesh network at `https://this.tunnelmesh/`. This provides secure access without exposing the admin panel to the internet.

**First-time setup - trust the CA certificate:**

```bash
# Install the mesh CA certificate (requires sudo)
tunnelmesh trust-ca --server https://your-coordinator:8443
```

This installs the TunnelMesh CA in your system trust store, allowing HTTPS connections to mesh services without browser warnings. After trusting the CA, access the admin panel at `https://this.tunnelmesh/` from any mesh peer.

#### Admin Security Model

The admin interface has **no built-in authentication** - access control is based on network exposure:

| Configuration | Exposure | Security |
|--------------|----------|----------|
| Default (no `join_mesh`) | HTTP on `127.0.0.1:8080` | Safe - localhost only |
| With `join_mesh` (default) | HTTPS on mesh IP only | Safe - only mesh peers can reach it |
| With `join_mesh` + `mesh_only_admin: false` | HTTP on `bind_address` | **Exposed** - use authenticated proxy |
| `bind_address: "0.0.0.0"` without `join_mesh` | HTTP on all interfaces | **Exposed** - use authenticated proxy |

**Recommendations:**
- For production, use `join_mesh` to serve admin via HTTPS on mesh IP only (the default)
- If external access is needed, place behind an authenticated reverse proxy (nginx, Caddy, etc.)
- Never set `bind_address: "0.0.0.0"` without additional access controls

## Configuration

### Server Configuration

```yaml
# HTTP server address
listen: ":8080"

# Token for peer authentication
auth_token: "your-secure-token"

# Mesh network CIDR for IP allocation
mesh_cidr: "172.30.0.0/16"

# Domain suffix for hostnames
domain_suffix: ".tunnelmesh"

# Enable node location tracking (optional, disabled by default)
# WARNING: Uses external IP geolocation API (ip-api.com)
locations: false

# Admin web interface
admin:
  enabled: true

# Optional: server participates as a mesh node
join_mesh:
  name: "server-node"
  private_key: "~/.tunnelmesh/id_ed25519"
  allow_exit_traffic: true  # Allow clients to route internet through this node
  tun:
    name: "tun-mesh0"
    mtu: 1400
  dns:
    enabled: true
    listen: "127.0.0.53:5353"
    cache_ttl: 300
    aliases:
      - "coordinator"       # coordinator.tunnelmesh -> this node's IP
```

### Peer Configuration

```yaml
# Unique node name
name: "mynode"

# Coordination server URL
server: "http://coord.example.com:8080"

# Must match server auth_token
auth_token: "your-secure-token"

# SSH server port for incoming peer connections
ssh_port: 2222

# Path to SSH private key
private_key: "~/.tunnelmesh/id_ed25519"

# Exit node settings (optional)
exit_node: "server-node"      # Route internet traffic through this peer
allow_exit_traffic: false     # Set true to allow others to use this node as exit

# Manual location (optional, overrides IP geolocation)
location:
  latitude: 52.3676
  longitude: 4.9041

# TUN interface settings
tun:
  name: "tun-mesh0"
  mtu: 1400

# Local DNS resolver
dns:
  enabled: true
  listen: "127.0.0.53:5353"
  cache_ttl: 300
  # Custom DNS aliases for this node (resolved mesh-wide)
  aliases:
    - "webserver"           # webserver.tunnelmesh -> this node's IP
    - "api.mynode"          # api.mynode.tunnelmesh -> this node's IP
```

### Transport Layer

TunnelMesh supports multiple transport types with automatic negotiation and fallback:

| Transport | Description | Use Case |
|-----------|-------------|----------|
| **UDP** | WireGuard-like encrypted UDP (default) | Lower latency, better throughput, NAT traversal |
| **SSH** | SSH-based tunnels | Reliable, works through most firewalls |
| **Relay** | WebSocket through coordination server | Fallback when direct connection fails |

The default transport order is: UDP → SSH → Relay. The system automatically negotiates the best available transport.

**Transport features:**
- Automatic fallback: If the preferred transport fails, the next one is tried
- Per-peer preferences: Configure different transports for specific peers via admin UI
- NAT traversal: Built-in STUN-like endpoint discovery and UDP hole-punching
- Zero-copy forwarding: Optimized packet path for high throughput

### Exit Nodes (Split-Tunnel VPN)

Route internet traffic through a designated peer while keeping mesh-to-mesh traffic direct. This is useful for:
- Accessing geo-restricted content through a peer in another region
- Privacy: route external traffic through a trusted exit point
- Compliance: ensure internet traffic egresses from a specific location

```
┌─────────────────┐                      ┌─────────────────┐
│   Client Peer   │                      │   Exit Node     │
│   (172.30.0.1)   │                      │   (172.30.0.2)   │
│                 │                      │                 │
│  Internet ──────┼──── Tunnel ─────────►│──► Internet     │
│  traffic        │  (encrypted)         │   (NAT)         │
│                 │                      │                 │
│  Mesh traffic ──┼──── Direct ─────────►│  Other peers    │
└─────────────────┘                      └─────────────────┘
```

**On the exit node** (the peer that will forward internet traffic):
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
exit_node: "exit-peer-name"
```

TunnelMesh automatically configures:
- **Exit node**: IP forwarding and NAT/masquerade rules
- **Client**: Default routes (0.0.0.0/1 and 128.0.0.0/1) through the TUN interface

This works on Linux and macOS. On Windows, manual route configuration may be required.

### Config File Locations

The tool searches for config files in the following order:

**Server:** `server.yaml`, `tunnelmesh-server.yaml`

**Peer:** `~/.tunnelmesh/config.yaml`, `tunnelmesh.yaml`, `peer.yaml`

## CLI Quick Reference

```bash
# First time setup
tunnelmesh init                    # Generate SSH keys

# Run coordination server
sudo tunnelmesh serve -c server.yaml

# Join mesh as peer
sudo tunnelmesh join -c peer.yaml

# Check status
tunnelmesh status                  # Connection status
tunnelmesh peers                   # List all peers

# System service
sudo tunnelmesh service install --mode join -c /etc/tunnelmesh/peer.yaml
sudo tunnelmesh service start
```

Most commands require a config file (`-c` flag) or one in a default location (`~/.tunnelmesh/config.yaml`, `peer.yaml`).

See **[CLI Reference](docs/CLI.md)** for complete documentation, all flags, and walkthroughs.

## Docker Deployment

Run TunnelMesh in containers for development, testing, or production. See the **[Docker Deployment Guide](docs/DOCKER.md)** for complete documentation.

```bash
cd docker
docker compose up -d        # Start the full mesh stack
docker compose logs -f      # View logs
make docker-test            # Run connectivity tests
```

## Cloud Deployment

Deploy to DigitalOcean App Platform with Terraform. See the **[Cloud Deployment Guide](docs/CLOUD_DEPLOYMENT.md)** for complete documentation.

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
