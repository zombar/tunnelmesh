# TunnelMesh CLI Reference

Complete command-line reference for TunnelMesh with examples and walkthroughs.

## TL;DR - Most Common Commands

```bash
# Generate SSH keys (first time only)
tunnelmesh init

# Run a coordination server
sudo tunnelmesh serve -c server.yaml

# Trust the mesh CA for HTTPS access (run on peers, requires running coordinator)
sudo tunnelmesh trust-ca -s https://coord.example.com

# Join the mesh as a peer
sudo tunnelmesh join -c peer.yaml

# Check your connection status
tunnelmesh status -c peer.yaml

# List all peers in the mesh
tunnelmesh peers -c peer.yaml

# Speed test to another peer
tunnelmesh benchmark other-peer --size 100MB

# Install as system service (starts on boot)
sudo tunnelmesh service install --mode join -c /etc/tunnelmesh/peer.yaml
sudo tunnelmesh service start
```

> **Config file required:** Almost all commands need a config file to know which server to connect to and authenticate with. Use `-c path/to/config.yaml` explicitly, or place your config in a default location:
> - `~/.tunnelmesh/config.yaml` (recommended for personal use)
> - `peer.yaml` or `tunnelmesh.yaml` in current directory
>
> Commands that work without a config: `init`, `version`, `trust-ca` (uses `-s` flag), and `service` subcommands.

---

## Installation

Download the latest release for your platform:

```bash
# Linux (amd64)
curl -LO https://github.com/zombar/tunnelmesh/releases/latest/download/tunnelmesh-linux-amd64
chmod +x tunnelmesh-linux-amd64
sudo mv tunnelmesh-linux-amd64 /usr/local/bin/tunnelmesh

# macOS (Apple Silicon)
curl -LO https://github.com/zombar/tunnelmesh/releases/latest/download/tunnelmesh-darwin-arm64
chmod +x tunnelmesh-darwin-arm64
sudo mv tunnelmesh-darwin-arm64 /usr/local/bin/tunnelmesh

# macOS (Intel)
curl -LO https://github.com/zombar/tunnelmesh/releases/latest/download/tunnelmesh-darwin-amd64
chmod +x tunnelmesh-darwin-amd64
sudo mv tunnelmesh-darwin-amd64 /usr/local/bin/tunnelmesh

# Windows (PowerShell as Administrator)
Invoke-WebRequest -Uri "https://github.com/zombar/tunnelmesh/releases/latest/download/tunnelmesh-windows-amd64.exe" -OutFile "tunnelmesh.exe"
Move-Item tunnelmesh.exe C:\Windows\System32\
```

Or build from source:
```bash
git clone https://github.com/zombar/tunnelmesh.git
cd tunnelmesh
make build
sudo mv tunnelmesh /usr/local/bin/
```

---

## Quick Reference

| Command | Description |
|---------|-------------|
| `tunnelmesh serve` | Run coordination server |
| `tunnelmesh join` | Connect peer to mesh |
| `tunnelmesh status` | Show connection status |
| `tunnelmesh peers` | List mesh peers |
| `tunnelmesh resolve <name>` | Resolve mesh hostname |
| `tunnelmesh leave` | Deregister from mesh |
| `tunnelmesh init` | Generate SSH keys |
| `tunnelmesh benchmark <peer>` | Speed test to peer |
| `tunnelmesh service` | Manage system service |
| `tunnelmesh update` | Self-update binary |
| `tunnelmesh trust-ca` | Install mesh CA cert |
| `tunnelmesh version` | Show version info |

---

## Global Flags

These flags work with all commands:

| Flag | Short | Description |
|------|-------|-------------|
| `--config` | `-c` | Path to config file |
| `--log-level` | `-l` | Log level: debug, info, warn, error |

---

## Commands in Detail

### tunnelmesh init

Initialize TunnelMesh by generating SSH keys.

```bash
tunnelmesh init
```

**What it does:**
1. Creates `~/.tunnelmesh/` directory
2. Generates ED25519 key pair
3. Saves private key to `~/.tunnelmesh/id_ed25519`
4. Saves public key to `~/.tunnelmesh/id_ed25519.pub`

**Example output:**
```
INF keys generated path=~/.tunnelmesh/id_ed25519
INF public key path=~/.tunnelmesh/id_ed25519.pub
```

---

### tunnelmesh serve

Run the coordination server.

```bash
tunnelmesh serve [flags]
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--locations` | Enable node location tracking (uses ip-api.com) |
| `--enable-tracing` | Enable runtime tracing (/debug/trace endpoint) |

**Example - Basic server:**
```bash
tunnelmesh serve --config server.yaml
```

**Example - With location tracking:**
```bash
tunnelmesh serve --config server.yaml --locations
```

**Example server.yaml:**
```yaml
listen: ":8080"
auth_token: "your-secure-token"
mesh_cidr: "172.30.0.0/16"
domain_suffix: ".tunnelmesh"

admin:
  enabled: true

# Optional: server also joins as a peer
join_mesh:
  name: "coordinator"
  private_key: "~/.tunnelmesh/id_ed25519"
  allow_exit_traffic: true
  tun:
    name: "tun-mesh0"
    mtu: 1400
  dns:
    enabled: true
    listen: "127.0.0.53:5353"
```

---

### tunnelmesh join

Connect to the mesh as a peer.

```bash
tunnelmesh join [flags]
```

**Flags:**

| Flag | Short | Description |
|------|-------|-------------|
| `--server` | `-s` | Coordination server URL |
| `--token` | `-t` | Authentication token |
| `--name` | `-n` | Peer name |
| `--wireguard` | | Enable WireGuard concentrator |
| `--exit-node` | | Route internet through specified peer |
| `--allow-exit-traffic` | | Allow this node as exit for others |
| `--latitude` | | Manual latitude (-90 to 90) |
| `--longitude` | | Manual longitude (-180 to 180) |
| `--city` | | City name for admin UI display |
| `--trust-ca` | | Install mesh CA cert (requires sudo) |
| `--enable-tracing` | | Enable runtime tracing |

**Example - Basic join:**
```bash
sudo tunnelmesh join \
  --server https://tunnelmesh.example.com \
  --token your-secure-token \
  --name my-laptop
```

**Example - Join with exit node:**
```bash
sudo tunnelmesh join \
  --server https://tunnelmesh.example.com \
  --token your-secure-token \
  --name my-laptop \
  --exit-node server-node
```

**Example - Join as exit node:**
```bash
sudo tunnelmesh join \
  --server https://tunnelmesh.example.com \
  --token your-secure-token \
  --name exit-singapore \
  --allow-exit-traffic \
  --latitude 1.3521 \
  --longitude 103.8198 \
  --city Singapore
```

**Example - Join with WireGuard concentrator:**
```bash
sudo tunnelmesh join \
  --server https://tunnelmesh.example.com \
  --token your-secure-token \
  --name wg-gateway \
  --wireguard
```

**Example peer.yaml:**
```yaml
name: "my-laptop"
server: "https://tunnelmesh.example.com"
auth_token: "your-secure-token"
ssh_port: 2222
private_key: "~/.tunnelmesh/id_ed25519"

# Exit node configuration
exit_node: "server-node"        # Route internet through this peer
allow_exit_traffic: false       # Don't allow others to use us as exit

# Manual location (optional)
location:
  latitude: 52.3676
  longitude: 4.9041
  city: "Amsterdam"

# TUN interface
tun:
  name: "tun-mesh0"
  mtu: 1400

# Local DNS resolver
dns:
  enabled: true
  listen: "127.0.0.53:5353"
  cache_ttl: 300
  aliases:
    - "laptop"              # laptop.tunnelmesh -> this peer
    - "dev.myname"          # dev.myname.tunnelmesh -> this peer

# WireGuard concentrator (optional)
wireguard:
  enabled: false
  listen_port: 51820
  endpoint: ""              # Public endpoint for clients

# Metrics
metrics_port: 9090

# Loki log shipping (optional)
loki:
  enabled: false
  url: "http://loki:3100"
```

---

### tunnelmesh status

Show current mesh status.

```bash
tunnelmesh status
```

**Example output:**
```
TunnelMesh Status
=================

Node:
  Name:        my-laptop
  SSH Port:    2222
  Private Key: /home/user/.tunnelmesh/id_ed25519
  Key FP:      SHA256:abc123...

Server:
  URL:         https://tunnelmesh.example.com
  Status:      connected

Mesh:
  Mesh IP:     172.30.0.5
  Last Seen:   2024-01-15 10:30:45
  Connectable: yes
  Total Peers: 5
  Online:      4

TUN Device:
  Name:        tun-mesh0
  MTU:         1400

DNS:
  Enabled:     yes
  Listen:      127.0.0.53:5353
  Cache TTL:   300s
```

---

### tunnelmesh peers

List all peers in the mesh.

```bash
tunnelmesh peers
```

**Example output:**
```
NAME                 MESH IP         PUBLIC IP            LAST SEEN
-------------------- --------------- -------------------- --------------------
coordinator          172.30.0.1      203.0.113.10         2024-01-15 10:30:45
my-laptop            172.30.0.5      198.51.100.20        2024-01-15 10:30:42
server-eu            172.30.0.3      192.0.2.50           2024-01-15 10:30:40
mobile-client        172.30.0.10     -                    2024-01-15 10:25:00
```

---

### tunnelmesh resolve

Resolve a mesh hostname to its IP address.

```bash
tunnelmesh resolve <hostname>
```

**Examples:**
```bash
# Resolve a peer name
tunnelmesh resolve coordinator
# Output: coordinator -> 172.30.0.1

# Resolve with domain suffix
tunnelmesh resolve coordinator.tunnelmesh
# Output: coordinator.tunnelmesh -> 172.30.0.1

# Resolve a DNS alias
tunnelmesh resolve nas
# Output: nas -> 172.30.0.2
```

---

### tunnelmesh leave

Deregister from the mesh network.

```bash
tunnelmesh leave
```

**Note:** This removes your peer record from the coordinator. Your mesh IP will be released and may be assigned to another peer. Use this when permanently leaving the mesh, not for temporary disconnects.

---

### tunnelmesh benchmark

Run speed tests between peers.

```bash
tunnelmesh benchmark <peer-name> [flags]
```

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--size` | Transfer size | 10MB |
| `--direction` | upload or download | upload |
| `--output`, `-o` | JSON output file | |
| `--timeout` | Benchmark timeout | 2m |
| `--port` | Benchmark server port | 9998 |
| `--packet-loss` | Packet loss % (0-100) | 0 |
| `--latency` | Added latency | 0 |
| `--jitter` | Latency variation | 0 |
| `--bandwidth` | Bandwidth limit | unlimited |

**Example - Basic speed test:**
```bash
tunnelmesh benchmark server-node
```

**Example output:**
```
Benchmarking server-node (172.30.0.1)...
  Direction:  upload
  Size:       10 MB
  Duration:   125 ms
  Throughput: 640.00 Mbps
  Latency:    1.23 ms (avg), 0.95 ms (min), 2.10 ms (max)
```

**Example - Large transfer with JSON output:**
```bash
tunnelmesh benchmark server-node --size 100MB --output results.json
```

**Example - Chaos testing (simulate poor network):**
```bash
# Simulate flaky WiFi
tunnelmesh benchmark server-node --size 50MB --packet-loss 2

# Simulate mobile connection
tunnelmesh benchmark server-node --size 10MB --latency 150ms --jitter 50ms

# Simulate bandwidth-constrained link
tunnelmesh benchmark server-node --size 100MB --bandwidth 10mbps

# Combined stress test
tunnelmesh benchmark server-node --size 20MB \
  --packet-loss 3 \
  --latency 50ms \
  --jitter 20ms \
  --bandwidth 20mbps
```

See [Benchmarking Guide](BENCHMARKING.md) for detailed documentation.

---

### tunnelmesh service

Manage TunnelMesh as a system service.

```bash
tunnelmesh service <subcommand> [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `install` | Install as system service |
| `uninstall` | Remove system service |
| `start` | Start the service |
| `stop` | Stop the service |
| `restart` | Restart the service |
| `status` | Show service status |
| `logs` | View service logs |

**Install flags:**

| Flag | Short | Description |
|------|-------|-------------|
| `--mode` | | `serve` or `join` |
| `--config` | `-c` | Config file path |
| `--name` | `-n` | Custom service name |
| `--user` | | Run as user (Linux/macOS) |
| `--force` | `-f` | Force reinstall |

**Logs flags:**

| Flag | Description |
|------|-------------|
| `--follow` | Follow logs in real-time |
| `--lines` | Number of lines to show |

**Example - Install peer service:**
```bash
# Create config directory
sudo mkdir -p /etc/tunnelmesh
sudo cp peer.yaml /etc/tunnelmesh/peer.yaml

# Install service
sudo tunnelmesh service install --mode join --config /etc/tunnelmesh/peer.yaml

# Start service
sudo tunnelmesh service start

# Check status
tunnelmesh service status

# View logs
tunnelmesh service logs --follow
```

**Example - Install server service:**
```bash
sudo tunnelmesh service install --mode serve --config /etc/tunnelmesh/server.yaml
sudo tunnelmesh service start
```

**Example - Multiple instances:**
```bash
# Install two peer services with different names
sudo tunnelmesh service install --mode join --name tunnelmesh-home --config /etc/tunnelmesh/home.yaml
sudo tunnelmesh service install --mode join --name tunnelmesh-work --config /etc/tunnelmesh/work.yaml

# Control specific instance
sudo tunnelmesh service start --name tunnelmesh-home
sudo tunnelmesh service status --name tunnelmesh-work
```

**Platform-specific paths:**

| Platform | Service Manager | Config Path | Log Command |
|----------|-----------------|-------------|-------------|
| Linux | systemd | `/etc/tunnelmesh/` | `journalctl -u tunnelmesh` |
| macOS | launchd | `/etc/tunnelmesh/` | `tunnelmesh service logs` |
| Windows | SCM | `C:\ProgramData\TunnelMesh\` | Event Viewer |

---

### tunnelmesh update

Self-update to the latest version.

```bash
tunnelmesh update [flags]
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--version` | Update to specific version |
| `--force` | Force update even if current |
| `--check` | Check for updates only |

**Example - Check for updates:**
```bash
tunnelmesh update --check
```

**Example - Update to latest:**
```bash
sudo tunnelmesh update
```

**Example - Update to specific version:**
```bash
sudo tunnelmesh update --version v1.2.3
```

---

### tunnelmesh trust-ca

Install the mesh CA certificate in system trust store.

```bash
sudo tunnelmesh trust-ca [flags]
```

**Flags:**

| Flag | Short | Description |
|------|-------|-------------|
| `--server` | `-s` | Coordinator server URL |

**Example:**
```bash
sudo tunnelmesh trust-ca --server https://tunnelmesh.example.com
```

**What it does:**
1. Downloads the mesh CA certificate from the coordinator
2. Installs it in the system trust store
3. Enables HTTPS connections to mesh services without browser warnings

After trusting the CA, you can access:
- `https://this.tunnelmesh/` - Admin dashboard
- `https://peer-name.tunnelmesh/` - Peer services

---

### tunnelmesh version

Show version information.

```bash
tunnelmesh version
```

**Example output:**
```
tunnelmesh v1.5.0
  Commit:     abc123def
  Build Time: 2024-01-15T10:00:00Z
```

---

## Configuration Files

### Config File Locations

TunnelMesh searches for config files in order:

**Server mode:**
1. Path specified by `--config` flag
2. `server.yaml` in current directory
3. `tunnelmesh-server.yaml` in current directory

**Peer mode:**
1. Path specified by `--config` flag
2. `~/.tunnelmesh/config.yaml`
3. `tunnelmesh.yaml` in current directory
4. `peer.yaml` in current directory

### Complete Server Configuration

```yaml
# Network configuration
listen: ":8080"                    # HTTP listen address
mesh_cidr: "172.30.0.0/16"        # Mesh network range
domain_suffix: ".tunnelmesh"       # DNS suffix

# Authentication
auth_token: "your-64-char-hex-token"

# Data storage
data_dir: "/var/lib/tunnelmesh"

# Logging
log_level: "info"                  # debug, info, warn, error

# Feature flags
locations: false                   # Enable IP geolocation

# Admin interface
admin:
  enabled: true
  port: 443                        # HTTPS port for admin (when join_mesh enabled)

# Optional: Server also joins as mesh peer
join_mesh:
  name: "coordinator"
  private_key: "~/.tunnelmesh/id_ed25519"
  allow_exit_traffic: true

  tun:
    name: "tun-mesh0"
    mtu: 1400

  dns:
    enabled: true
    listen: "127.0.0.53:5353"
    cache_ttl: 300
    aliases:
      - "coord"
      - "server"
```

### Complete Peer Configuration

```yaml
# Identity
name: "my-peer"
private_key: "~/.tunnelmesh/id_ed25519"

# Server connection
server: "https://tunnelmesh.example.com"
auth_token: "your-64-char-hex-token"

# Transport
ssh_port: 2222

# Exit node settings
exit_node: ""                      # Peer to route internet through
allow_exit_traffic: false          # Allow others to use as exit

# Manual geolocation
location:
  latitude: 0.0
  longitude: 0.0
  city: ""

# TUN interface
tun:
  name: "tun-mesh0"
  mtu: 1400

# DNS
dns:
  enabled: true
  listen: "127.0.0.53:5353"
  cache_ttl: 300
  aliases: []

# WireGuard concentrator
wireguard:
  enabled: false
  listen_port: 51820
  endpoint: ""
  data_dir: ""

# Metrics
metrics_port: 9090

# Logging
log_level: "info"

# Loki integration
loki:
  enabled: false
  url: ""
  batch_size: 100
  flush_interval: "5s"
```

---

## Walkthroughs

### Walkthrough 1: Personal VPN Setup

Set up TunnelMesh for personal use with a cloud server and laptop.

**Step 1: Deploy coordinator (cloud server)**
```bash
# On your cloud server
sudo mkdir -p /etc/tunnelmesh

cat > /etc/tunnelmesh/server.yaml << 'EOF'
listen: ":8080"
auth_token: "$(openssl rand -hex 32)"
mesh_cidr: "172.30.0.0/16"
admin:
  enabled: true
join_mesh:
  name: "cloud-server"
  private_key: "/root/.tunnelmesh/id_ed25519"
  allow_exit_traffic: true
  tun:
    name: "tun-mesh0"
    mtu: 1400
  dns:
    enabled: true
    listen: "127.0.0.53:5353"
EOF

tunnelmesh init
sudo tunnelmesh service install --mode serve --config /etc/tunnelmesh/server.yaml
sudo tunnelmesh service start
```

**Step 2: Connect laptop**
```bash
# On your laptop
tunnelmesh init

cat > ~/.tunnelmesh/config.yaml << 'EOF'
name: "my-laptop"
server: "http://your-server-ip:8080"
auth_token: "same-token-from-server"
exit_node: "cloud-server"
tun:
  name: "tun-mesh0"
  mtu: 1400
dns:
  enabled: true
  listen: "127.0.0.53:5353"
EOF

sudo tunnelmesh join
```

**Step 3: Verify connection**
```bash
# Check status
tunnelmesh status

# Ping the server
ping cloud-server.tunnelmesh

# Your internet now routes through the cloud server
curl ifconfig.me
```

### Walkthrough 2: Team Development Mesh

Connect a development team for direct machine access.

**Step 1: Deploy minimal coordinator**
```bash
# On a small cloud instance
cat > server.yaml << 'EOF'
listen: ":8080"
auth_token: "team-secret-token"
mesh_cidr: "172.30.0.0/16"
admin:
  enabled: true
EOF

sudo tunnelmesh serve --config server.yaml
```

**Step 2: Each developer joins**
```bash
# Each team member runs:
tunnelmesh init

sudo tunnelmesh join \
  --server http://coordinator-ip:8080 \
  --token team-secret-token \
  --name $(whoami)
```

**Step 3: Team collaboration**
```bash
# SSH to teammate
ssh alice.tunnelmesh

# Access teammate's dev server
curl http://bob.tunnelmesh:3000

# Connect to teammate's database
psql -h charlie.tunnelmesh mydb
```

### Walkthrough 3: Home Lab Remote Access

Access home network from anywhere.

**Step 1: Cloud coordinator with WireGuard**
```bash
# Deploy to cloud (see terraform docs)
# Or manually:
cat > server.yaml << 'EOF'
listen: ":8080"
auth_token: "your-token"
mesh_cidr: "172.30.0.0/16"
admin:
  enabled: true
join_mesh:
  name: "cloud"
  wireguard:
    enabled: true
    listen_port: 51820
EOF

sudo tunnelmesh serve --config server.yaml
```

**Step 2: Home server joins**
```bash
# On your home server (Raspberry Pi, NAS, etc.)
cat > peer.yaml << 'EOF'
name: "homelab"
server: "https://cloud.example.com"
auth_token: "your-token"
dns:
  aliases:
    - "nas"
    - "plex"
    - "homeassistant"
EOF

sudo tunnelmesh service install --mode join --config peer.yaml
sudo tunnelmesh service start
```

**Step 3: Mobile access via WireGuard**
1. Open admin dashboard: `https://cloud.example.com/`
2. Go to WireGuard > Add Client
3. Scan QR code with WireGuard app
4. Connect and access `nas.tunnelmesh`, `plex.tunnelmesh`, etc.

---

## Troubleshooting

### "Config file required"
```bash
# Create config or specify path
tunnelmesh join --config /path/to/config.yaml

# Or create in default location
mkdir -p ~/.tunnelmesh
cp peer.yaml ~/.tunnelmesh/config.yaml
```

### "Failed to create TUN device"
```bash
# Requires root/admin privileges
sudo tunnelmesh join

# Or check TUN device availability
ls -la /dev/net/tun
```

### "Connection refused" to coordinator
```bash
# Check coordinator is running
curl http://coordinator:8080/health

# Check firewall
sudo ufw allow 8080/tcp  # Linux
```

### DNS not resolving mesh names
```bash
# Check resolver is running
dig @127.0.0.53 -p 5353 peer-name.tunnelmesh

# Check system resolver config
# macOS:
cat /etc/resolver/tunnelmesh

# Linux:
resolvectl status
```

### Slow or unreliable connections
```bash
# Check transport type
tunnelmesh peers  # Shows connection info

# Run benchmark to diagnose
tunnelmesh benchmark peer-name --size 10MB

# Check if using relay (slower)
# Look for "relay" in peers output
```

---

## Environment Variables

| Variable | Description |
|----------|-------------|
| `TUNNELMESH_CONFIG` | Default config file path |
| `TUNNELMESH_LOG_LEVEL` | Log level override |
| `TUNNELMESH_SERVER` | Default coordinator URL |
| `TUNNELMESH_TOKEN` | Default auth token |
