# TunnelMesh

A peer-to-peer mesh networking tool that creates encrypted tunnels between nodes using SSH. TunnelMesh enables direct, secure communication between peers in a distributed topology without requiring a traditional VPN or centralized traffic routing.

## Features

- **P2P Encrypted Tunnels** - Direct SSH-based connections between peers
- **Coordination Server** - Central hub for peer discovery and IP allocation (not a traffic router)
- **TUN Interface** - Virtual network interface for transparent IP routing
- **Built-in DNS** - Local resolver for mesh hostnames (e.g., `node.mesh`)
- **Network Monitoring** - Automatic detection of network changes with re-connection
- **NAT Traversal** - Supports both direct and reverse connections for peers behind NAT
- **Multi-Platform** - Linux, macOS, and Windows support
- **Admin Dashboard** - Web interface showing mesh status, peers, and traffic statistics
- **Server-as-Client** - Coordination server can also participate as a mesh node

![Admin Dashboard](docs/images/admin-dashboard.png)

## Installation

### From Source

Requires Go 1.24+

```bash
git clone https://github.com/zombar/tunnelmesh.git
cd tunnelmesh
make build
```

### Cross-Platform Builds

```bash
make release-all
```

Outputs binaries for Linux, macOS, and Windows (amd64/arm64) in `bin/`.

## Quick Start

### 1. Generate SSH Keys

```bash
tunnelmesh init
```

This creates `~/.tunnelmesh/id_ed25519` and `~/.tunnelmesh/id_ed25519.pub`.

### 2. Start the Coordination Server

Create `server.yaml`:

```yaml
listen: ":8080"
auth_token: "your-secure-token"
mesh_cidr: "10.99.0.0/16"
domain_suffix: ".mesh"
admin:
  enabled: true
```

Run the server:

```bash
tunnelmesh serve -c server.yaml
```

The admin interface is available at `http://localhost:8080/`.

### 3. Join a Peer to the Mesh

Create `peer.yaml`:

```yaml
name: "mynode"
server: "http://coord.example.com:8080"
auth_token: "your-secure-token"
ssh_port: 2222
private_key: "~/.tunnelmesh/id_ed25519"
tun:
  name: "tun-mesh0"
  mtu: 1400
dns:
  enabled: true
  listen: "127.0.0.53:5353"
```

Join the mesh (requires root/admin for TUN interface):

```bash
sudo tunnelmesh join -c peer.yaml
```

### 4. Verify Connectivity

```bash
# List connected peers
tunnelmesh peers

# Check node status
tunnelmesh status

# Resolve a mesh hostname
tunnelmesh resolve othernode.mesh

# Ping another node
ping othernode.mesh
```

## CLI Reference

| Command | Description |
|---------|-------------|
| `tunnelmesh serve` | Run the coordination server |
| `tunnelmesh join` | Connect a peer to the mesh |
| `tunnelmesh status` | Show node status and connectivity |
| `tunnelmesh peers` | List all connected peers |
| `tunnelmesh resolve <hostname>` | Resolve mesh hostname to IP |
| `tunnelmesh leave` | Deregister from the mesh |
| `tunnelmesh init` | Generate SSH keys |
| `tunnelmesh version` | Show version information |

### Global Flags

| Flag | Description |
|------|-------------|
| `-c, --config` | Config file path |
| `-l, --log-level` | Logging level (debug, info, warn, error) |
| `-s, --server` | Coordination server URL |
| `-t, --token` | Authentication token |
| `-n, --name` | Node name |

## Configuration

### Server Configuration

```yaml
# HTTP server address
listen: ":8080"

# Token for peer authentication
auth_token: "your-secure-token"

# Mesh network CIDR for IP allocation
mesh_cidr: "10.99.0.0/16"

# Domain suffix for hostnames
domain_suffix: ".mesh"

# Admin web interface
admin:
  enabled: true

# Optional: server participates as a mesh node
join_mesh:
  name: "server-node"
  private_key: "~/.tunnelmesh/id_ed25519"
  tun:
    name: "tun-mesh0"
    mtu: 1400
  dns:
    enabled: true
    listen: "127.0.0.53:5353"
    cache_ttl: 300
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

# TUN interface settings
tun:
  name: "tun-mesh0"
  mtu: 1400

# Local DNS resolver
dns:
  enabled: true
  listen: "127.0.0.53:5353"
  cache_ttl: 300
```

### Config File Locations

The tool searches for config files in the following order:

**Server:** `server.yaml`, `tunnelmesh-server.yaml`

**Peer:** `~/.tunnelmesh/config.yaml`, `tunnelmesh.yaml`, `peer.yaml`

## Architecture

```
┌─────────────────┐                      ┌─────────────────┐
│   Peer Node A   │                      │   Peer Node B   │
│   (10.99.0.1)   │                      │   (10.99.0.2)   │
│                 │    SSH Tunnel        │                 │
│  ┌───────────┐  │◄────────────────────►│  ┌───────────┐  │
│  │ TUN Device│  │     (Encrypted)      │  │ TUN Device│  │
│  │  Router   │  │                      │  │  Router   │  │
│  │ Forwarder │  │                      │  │ Forwarder │  │
│  └───────────┘  │                      │  └───────────┘  │
└────────┬────────┘                      └────────┬────────┘
         │                                        │
         │  Register/Heartbeat/Discovery          │
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
              │ • Admin UI      │
              └─────────────────┘
```

**Key points:**
- Traffic flows directly between peers via SSH tunnels
- The coordination server only handles discovery and registration
- Each peer runs a TUN interface for transparent IP routing
- Peers establish connections using negotiated strategies (direct or reverse)

## Docker Deployment

### Build the Image

```bash
make docker-build
```

### Run with Docker Compose

The included `docker-compose.yml` sets up a server with multiple client replicas:

```bash
# Start the mesh
docker compose -f docker/docker-compose.yml up -d

# View logs
docker compose -f docker/docker-compose.yml logs -f

# Run connectivity tests
make docker-test

# Stop
docker compose -f docker/docker-compose.yml down
```

### Docker Requirements

Containers need `NET_ADMIN` capability for TUN interface creation:

```yaml
cap_add:
  - NET_ADMIN
devices:
  - /dev/net/tun
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
