# TunnelMesh CLI Reference

Complete command-line reference for TunnelMesh with examples and walkthroughs.

## TL;DR - Most Common Commands

```bash
# Generate SSH keys (first time only)
tunnelmesh init

# Run a coordination server
sudo tunnelmesh serve -c server.yaml

# Join the mesh as a peer (--context saves it for future use)
sudo tunnelmesh join -c peer.yaml --context home

# List your contexts
tunnelmesh context list

# Switch active context (changes DNS resolution)
tunnelmesh context use work

# Check your connection status
tunnelmesh status

# List all peers in the mesh
tunnelmesh peers

# Speed test to another peer
tunnelmesh benchmark other-peer --size 100MB

# Install as system service (uses active context)
sudo tunnelmesh service install
sudo tunnelmesh service start
```

> **Contexts simplify management:** After joining with `--context`, TunnelMesh remembers your configuration. Subsequent commands use the active context automatically—no need to specify `-c` every time.
>
> Commands that work without a context: `init`, `version`, and `context` subcommands.

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
| --------- | ------------- |
| `tunnelmesh serve` | Run coordination server |
| `tunnelmesh join` | Connect peer to mesh |
| `tunnelmesh context` | Manage mesh contexts |
| `tunnelmesh status` | Show connection status |
| `tunnelmesh peers` | List mesh peers |
| `tunnelmesh resolve <name>` | Resolve mesh hostname |
| `tunnelmesh leave` | Deregister from mesh |
| `tunnelmesh init` | Generate SSH keys |
| `tunnelmesh benchmark <peer>` | Speed test to peer |
| `tunnelmesh service` | Manage system service |
| `tunnelmesh update` | Self-update binary |
| `tunnelmesh version` | Show version info |

---

## Global Flags

These flags work with all commands:

| Flag | Short | Description |
| ------ | ------- | ------------- |
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
| ------ | ------------- |
| `--locations` | Enable peer location tracking (uses ip-api.com) |
| `--enable-tracing` | Enable runtime tracing (/debug/trace endpoint) |

**Example - Basic server:**

```bash
tunnelmesh serve --config server.yaml
```

**Example - With location tracking:**

```bash
tunnelmesh serve --config server.yaml --locations
```

**Generate a config:**

```bash
tunnelmesh init --server --peer --output server.yaml
```

See [server.yaml.example](../server.yaml.example) for all configuration options.

---

### tunnelmesh join

Connect to the mesh as a peer.

```bash
tunnelmesh join [flags]
```

**Flags:**

| Flag | Short | Description |
| ------ | ------- | ------------- |
| `--server` | `-s` | Coordination server URL |
| `--token` | `-t` | Authentication token |
| `--name` | `-n` | Peer name (defaults to hostname) |
| `--context` |  | Save/update as named context |
| `--wireguard` |  | Enable WireGuard concentrator |
| `--exit-node` |  | Route internet through specified peer |
| `--allow-exit-traffic` |  | Allow this peer as exit for others |
| `--latitude` |  | Manual latitude (-90 to 90) |
| `--longitude` |  | Manual longitude (-180 to 180) |
| `--city` |  | City name for admin UI display |
| `--enable-tracing` |  | Enable runtime tracing |

**Example - Basic join:**

```bash
sudo tunnelmesh join \
  --server https://tunnelmesh.example.com \
  --token your-secure-token
```

**Example - Join with exit peer:**

```bash
sudo tunnelmesh join \
  --server https://tunnelmesh.example.com \
  --token your-secure-token \
  --exit-node server-peer
```

**Example - Join as exit peer:**

```bash
sudo tunnelmesh join \
  --server https://tunnelmesh.example.com \
  --token your-secure-token \
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
  --wireguard
```

**Generate a config:**

```bash
tunnelmesh init --peer --output peer.yaml
```

See [peer.yaml.example](../peer.yaml.example) for all configuration options.

---

### tunnelmesh context

Manage mesh contexts. Contexts store configuration paths, allocated IPs, and DNS settings for easy switching between
multiple meshes.

```bash
tunnelmesh context <subcommand>
```

**Subcommands:**

| Subcommand | Description |
| ------------ | ------------- |
| `create` | Create a new context from a config file |
| `list` | List all contexts |
| `use` | Switch active context (updates DNS) |
| `delete` | Delete a context |
| `show` | Show context details |

---

#### tunnelmesh context create

Create a new context from a configuration file.

```bash
 tunnelmesh context create <name> --config <path> [--mode serve | join] 
```

**Flags:**

| Flag | Description |
| ------ | ------------- |
| `--config`, `-c` | Path to config file (required) |
| `--mode` | Mode: `serve` or `join` (default: `join`) |

**Example:**

```bash
# Create a peer context
tunnelmesh context create home --config ~/.tunnelmesh/home.yaml

# Create a server context
tunnelmesh context create coordinator --config /etc/tunnelmesh/server.yaml --mode serve
```

---

#### tunnelmesh context list

List all contexts with their status.

```bash
tunnelmesh context list
```

**Example output:**

```
NAME         SERVER                      STATUS     ACTIVE
home         http://home-server:8080     running    *
work         https://work.mesh.io        stopped
dev          http://192.168.1.10:8080    -
```

- `STATUS`: Service status (`running`, `stopped`, or `-` if no service installed)
- `ACTIVE`: `*` marks the currently active context

---

#### tunnelmesh context use

Switch the active context. This changes which mesh's DNS resolver handles `.tunnelmesh` domains.

```bash
tunnelmesh context use <name>
```

**Example:**

```bash
tunnelmesh context use work
```

**What happens:**

1. Updates active context in `~/.tunnelmesh/.context`
2. Reconfigures system DNS resolver for the new mesh's domain
3. CLI commands now target the new context by default

**Note:** Switching contexts doesn't stop running tunnels. Multiple meshes can run simultaneously—only the DNS "focus"
changes.

---

#### tunnelmesh context delete

Delete a context. Prompts to stop/uninstall service if running.

```bash
tunnelmesh context delete <name>
```

**Example:**

```bash
tunnelmesh context delete dev
```

**Prompts:**

- If service is running: "Service is running. Stop and uninstall?"
- "Remove config file at /path/to/config.yaml?"

---

#### tunnelmesh context show

Show details of a specific context.

```bash
tunnelmesh context show [name]
```

If no name is provided, shows the active context.

**Example output:**

```
Context: home
  Config:     /home/user/.tunnelmesh/home.yaml
  Server:     http://home-server:8080
  Mesh IP:    172.30.0.5
  Domain:     .tunnelmesh
  DNS Listen: 127.0.0.53:5353
  Service:    tunnelmesh-home (running)
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

**Note:** This removes your peer record from the coordinator. Your mesh IP will be released and may be assigned to
another peer. Use this when permanently leaving the mesh, not for temporary disconnects.

---

### tunnelmesh benchmark

Run speed tests between peers.

```bash
tunnelmesh benchmark <peer-name> [flags]
```

**Flags:**

| Flag | Description | Default |
| ------ | ------------- | --------- |
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
tunnelmesh benchmark server-peer
```

**Example output:**

```
Benchmarking server-peer (172.30.0.1)...
  Direction:  upload
  Size:       10 MB
  Duration:   125 ms
  Throughput: 640.00 Mbps
  Latency:    1.23 ms (avg), 0.95 ms (min), 2.10 ms (max)
```

**Example - Large transfer with JSON output:**

```bash
tunnelmesh benchmark server-peer --size 100MB --output results.json
```

**Example - Chaos testing (simulate poor network):**

```bash
# Simulate flaky WiFi
tunnelmesh benchmark server-peer --size 50MB --packet-loss 2

# Simulate mobile connection
tunnelmesh benchmark server-peer --size 10MB --latency 150ms --jitter 50ms

# Simulate bandwidth-constrained link
tunnelmesh benchmark server-peer --size 100MB --bandwidth 10mbps

# Combined stress test
tunnelmesh benchmark server-peer --size 20MB \
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
| ------------ | ------------- |
| `install` | Install as system service |
| `uninstall` | Remove system service |
| `start` | Start the service |
| `stop` | Stop the service |
| `restart` | Restart the service |
| `status` | Show service status |
| `logs` | View service logs |

**Common flags (all subcommands):**

| Flag | Description |
| ------ | ------------- |
| `--context` | Target specific context (default: active context) |

**Install flags:**

| Flag | Short | Description |
| ------ | ------- | ------------- |
| `--user` |  | Run as user (Linux/macOS) |
| `--force` | `-f` | Force reinstall |

**Logs flags:**

| Flag | Description |
| ------ | ------------- |
| `--follow` | Follow logs in real-time |
| `--lines` | Number of lines to show |

**Example - Install peer service (context-based):**

```bash
# First, join and create a context
sudo tunnelmesh join --config /etc/tunnelmesh/peer.yaml --context home

# Install service for the active context
sudo tunnelmesh service install

# Start service
sudo tunnelmesh service start

# Check status
tunnelmesh service status

# View logs
tunnelmesh service logs --follow
```

**Example - Install server service:**

```bash
# Create context and install
tunnelmesh context create coordinator --config /etc/tunnelmesh/server.yaml --mode serve
sudo tunnelmesh service install
sudo tunnelmesh service start
```

**Example - Multiple meshes:**

```bash
# Join two different meshes
sudo tunnelmesh join --config ~/.tunnelmesh/home.yaml --context home
sudo tunnelmesh join --config ~/.tunnelmesh/work.yaml --context work

# Install services for both
sudo tunnelmesh service install --context home
sudo tunnelmesh service install --context work

# Start both
sudo tunnelmesh service start --context home
sudo tunnelmesh service start --context work

# Control specific context
tunnelmesh service status --context work
tunnelmesh service logs --context home --follow
```

**Service naming:** Service names are derived from context names:

- Context `home` → Service `tunnelmesh-home`
- Context `work` → Service `tunnelmesh-work`
- Context `default` → Service `tunnelmesh`

**Platform-specific paths:**

| Platform | Service Manager | Config Path | Log Command |
| ---------- | ----------------- | ------------- | ------------- |
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
| ------ | ------------- |
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

### Configuration Reference

For complete configuration options with documentation:

- **Server:** See [server.yaml.example](../server.yaml.example)
- **Peer:** See [peer.yaml.example](../peer.yaml.example)

Generate config files with:

```bash
tunnelmesh init --server --peer    # Both configs
tunnelmesh init --server           # Server only
tunnelmesh init --peer             # Peer only
```

---

## Walkthroughs

### Walkthrough 1: Personal VPN Setup

Set up TunnelMesh for personal use with a cloud server and laptop.

**Step 1: Deploy coordinator (cloud server)**

```bash
# On your cloud server
sudo mkdir -p /etc/tunnelmesh

# Generate config (server + peer mode for exit peer capability)
tunnelmesh init --server --peer --output /etc/tunnelmesh/server.yaml

# Edit: set auth_token, enable allow_exit_traffic in join_mesh section
sudo nano /etc/tunnelmesh/server.yaml

tunnelmesh context create vpn --config /etc/tunnelmesh/server.yaml --mode serve
sudo tunnelmesh service install
sudo tunnelmesh service start
```

**Step 2: Connect laptop**

```bash
# On your laptop
tunnelmesh init --peer --output ~/.tunnelmesh/vpn.yaml

# Edit: set server, auth_token, exit_node: "cloud-server"
nano ~/.tunnelmesh/vpn.yaml

# Join and save as context
sudo tunnelmesh join --config ~/.tunnelmesh/vpn.yaml --context vpn
```

**Step 3: Verify connection**

```bash
# Check status (uses active context)
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
tunnelmesh init --server --output server.yaml

# Edit: set auth_token
nano server.yaml

tunnelmesh context create team --config server.yaml --mode serve
sudo tunnelmesh service install
sudo tunnelmesh service start
```

**Step 2: Each developer joins**

```bash
# Each team member runs:
tunnelmesh init

sudo tunnelmesh join \
  --server http://coordinator-ip:8080 \
  --token team-secret-token \
  --context team
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
tunnelmesh init --server --peer --output server.yaml

# Edit: set auth_token, enable wireguard in join_mesh section
nano server.yaml

sudo tunnelmesh serve --config server.yaml
```

**Step 2: Home server joins**

```bash
# On your home server (Raspberry Pi, NAS, etc.)
tunnelmesh init --peer --output peer.yaml

# Edit: set name, server, auth_token, add dns.aliases for services
nano peer.yaml

sudo tunnelmesh join --config peer.yaml --context homelab
sudo tunnelmesh service install
sudo tunnelmesh service start
```

**Step 3: Mobile access via WireGuard**

1. Open admin dashboard: `https://cloud.example.com/`
2. Go to WireGuard > Add Client
3. Scan QR code with WireGuard app
4. Connect and access `nas.tunnelmesh`, `plex.tunnelmesh`, etc.

---

## Troubleshooting

### "No active context"

```bash
# List available contexts
tunnelmesh context list

# Set an active context
tunnelmesh context use home

# Or specify context for commands
tunnelmesh status --context home
```

### "Config file required"

```bash
# Join with --context to save configuration
sudo tunnelmesh join --config /path/to/config.yaml --context mycontext

# Or create context manually
tunnelmesh context create mycontext --config /path/to/config.yaml
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
| ---------- | ------------- |
| `TUNNELMESH_CONFIG` | Default config file path |
| `TUNNELMESH_LOG_LEVEL` | Log level override |
| `TUNNELMESH_SERVER` | Default coordinator URL |
| `TUNNELMESH_TOKEN` | Default auth token |
