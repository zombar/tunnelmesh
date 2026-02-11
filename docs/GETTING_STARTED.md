# Getting Started with TunnelMesh

This guide walks you through setting up a TunnelMesh network from scratch. You'll need:

1. **One coordinator peer** - Admin peer that manages peer discovery and IP allocation
2. **One or more regular peers** - Peers that connect to form the mesh network

## Part 1: Setting Up the Coordinator Peer

The coordinator is an admin peer with coordination services enabled
(`coordinator.enabled: true`). It provides discovery and admin services but
doesn't route traffic—peers connect directly to each other.

### Linux (amd64)

```bash
# Download the latest release
wget https://github.com/zombar/tunnelmesh/releases/latest/download/tunnelmesh-linux-amd64 -O tunnelmesh

# Make executable and move to PATH
chmod +x tunnelmesh
sudo mv tunnelmesh /usr/local/bin/

# Verify installation
tunnelmesh version
```

### Linux (arm64)

```bash
wget https://github.com/zombar/tunnelmesh/releases/latest/download/tunnelmesh-linux-arm64 -O tunnelmesh
chmod +x tunnelmesh
sudo mv tunnelmesh /usr/local/bin/
```

### macOS (Apple Silicon)

```bash
curl -L https://github.com/zombar/tunnelmesh/releases/latest/download/tunnelmesh-darwin-arm64 -o tunnelmesh
chmod +x tunnelmesh
sudo mv tunnelmesh /usr/local/bin/
```

### macOS (Intel)

```bash
curl -L https://github.com/zombar/tunnelmesh/releases/latest/download/tunnelmesh-darwin-amd64 -o tunnelmesh
chmod +x tunnelmesh
sudo mv tunnelmesh /usr/local/bin/
```

### Windows (PowerShell as Administrator)

```powershell
# Create installation directory
New-Item -ItemType Directory -Force -Path "C:\Program Files\TunnelMesh"

# Download the binary
Invoke-WebRequest -Uri "https://github.com/zombar/tunnelmesh/releases/latest/download/tunnelmesh-windows-amd64.exe" -OutFile "C:\Program Files\TunnelMesh\tunnelmesh.exe"

# Add to PATH (requires restart of terminal)
[Environment]::SetEnvironmentVariable("Path", $env:Path + ";C:\Program Files\TunnelMesh", "Machine")

# Verify installation (in new terminal)
tunnelmesh version
```

---

### Create the Coordinator Configuration

#### Coordinator Config - Linux/macOS

```bash
# Create config directory
sudo mkdir -p /etc/tunnelmesh

# Generate coordinator configuration
tunnelmesh init --peer --output /etc/tunnelmesh/coordinator.yaml

# Edit: set a secure auth_token and enable coordinator.enabled: true
sudo nano /etc/tunnelmesh/coordinator.yaml
```

#### Coordinator Config - Windows

```powershell
# Create config directory
New-Item -ItemType Directory -Force -Path "C:\ProgramData\TunnelMesh"

# Generate coordinator configuration
tunnelmesh init --peer --output "C:\ProgramData\TunnelMesh\coordinator.yaml"

# Edit: set a secure auth_token and enable coordinator.enabled: true
notepad "C:\ProgramData\TunnelMesh\coordinator.yaml"
```

---

### Create a Context and Install the Coordinator Service

TunnelMesh uses **contexts** to manage multiple mesh configurations. Each context tracks a config file, allocated IP,
and service status.

#### Install Coordinator Service - Linux/macOS

```bash
# Create a context for the coordinator
tunnelmesh context create coordinator --config /etc/tunnelmesh/coordinator.yaml

# Install and start as a system service
sudo tunnelmesh service install
sudo tunnelmesh service start

# Check status
tunnelmesh service status

# View logs
tunnelmesh service logs --follow
```

#### Install Coordinator Service - Windows

```powershell
# Create a context for the coordinator
tunnelmesh context create coordinator --config "C:\ProgramData\TunnelMesh\coordinator.yaml"

# Install as a Windows service
tunnelmesh service install

# Start the service
tunnelmesh service start

# Check status
tunnelmesh service status
```

**Note:** The service name is derived from the context name. The "coordinator" context creates a service named
"tunnelmesh-coordinator".

---

### Verify the Coordinator

The coordinator is now running and will self-register as a peer. The admin
dashboard is accessible via HTTPS from within the mesh at `https://this.tm/`
once you join other peers. Verify it's running by checking the service status
and logs.

---

## Part 2: Setting Up Peers

Peers are the peers that form the mesh network. Each peer:

- Registers with the coordination server
- Gets assigned a mesh IP address
- Establishes direct SSH tunnels with other peers

### Install TunnelMesh on Each Peer

Follow the same download steps as the server (see Part 1) for your platform.

---

### Generate SSH Keys

Each peer needs an SSH keypair for secure connections:

```bash
tunnelmesh init
```

This creates:

- `~/.tunnelmesh/id_ed25519` (private key)
- `~/.tunnelmesh/id_ed25519.pub` (public key)

---

### Create the Peer Configuration

#### Peer Config - Linux/macOS

```bash
# Create config directory
sudo mkdir -p /etc/tunnelmesh

# Generate peer configuration
tunnelmesh init --peer --output /etc/tunnelmesh/peer.yaml

# Edit config if needed (most settings are optional)
# Server URL and auth token are passed via CLI, not config
sudo nano /etc/tunnelmesh/peer.yaml

#### Peer Config - Windows

```powershell
# Create config directory
New-Item -ItemType Directory -Force -Path "C:\ProgramData\TunnelMesh"

# Generate peer configuration
tunnelmesh init --peer --output "C:\ProgramData\TunnelMesh\peer.yaml"

# Edit config if needed (most settings are optional)
# Server URL and auth token are passed via CLI, not config
notepad "C:\ProgramData\TunnelMesh\peer.yaml"
```

---

### Create a Context and Install the Peer Service

Each peer should have its own context. This allows you to manage multiple mesh memberships and switch between them easily.

#### Install Peer Service - Linux/macOS

```bash
# Join the mesh and create a context (this also prompts to install CA cert if missing)
sudo tunnelmesh join --config /etc/tunnelmesh/peer.yaml --context home

# The context is now active. Install as a system service:
sudo tunnelmesh service install
sudo tunnelmesh service start

# Check status
tunnelmesh service status

# View logs
tunnelmesh service logs --follow
```

#### Install Peer Service - Windows

```powershell
# Join the mesh and create a context
tunnelmesh join --config "C:\ProgramData\TunnelMesh\peer.yaml" --context home

# Install as a Windows service
tunnelmesh service install

# Start the service
tunnelmesh service start

# Check status
tunnelmesh service status
```

**Tip:** When joining, if the mesh CA certificate isn't installed, TunnelMesh will prompt you
to install it for HTTPS access to mesh services.

---

### Verify Peer Connectivity

```bash
# Check peer status
tunnelmesh status

# List connected peers
tunnelmesh peers

# Once other peers are connected, ping them by name
ping otherpeer.tunnelmesh
```

Check the admin dashboard on the server—your peer should now appear in the list.

---

## Firewall Configuration

### Coordination Server

Open port **8080** (or your configured port) for:

- Peer registration (HTTP)
- Admin dashboard access

### Peers

Open port **2222** (or your configured `ssh_port`) for:

- Incoming SSH tunnel connections from other peers

If a peer is behind NAT and cannot receive incoming connections, TunnelMesh will
automatically use reverse connections through peers that are reachable.

---

## Managing Multiple Contexts

You can be a member of multiple meshes simultaneously. Each mesh runs as a separate service with its own TUN interface.

### List all contexts

```bash
tunnelmesh context list
```

Example output:

```text
NAME      SERVER                      STATUS     ACTIVE
home      http://home-server:8080     running    *
work      https://work.mesh.io        stopped
dev       http://192.168.1.10:8080    -
```

### Switch active context

When you switch contexts, system DNS resolution switches to that mesh:

```bash
tunnelmesh context use work
```

This changes which mesh's DNS resolver handles `.tunnelmesh` domains. The previous mesh's
tunnels remain active—only the "focus" changes.

### Start/stop individual contexts

```bash
# Control specific context's service
tunnelmesh service start --context work
tunnelmesh service stop --context home

# Status of specific context
tunnelmesh service status --context work
```

### Delete a context

```bash
# This prompts to stop/uninstall the service and optionally remove the config
tunnelmesh context delete dev
```

---

## Troubleshooting

### Service won't start

```bash
# Check logs for errors
tunnelmesh service logs --lines 100

# Verify config file syntax
cat /etc/tunnelmesh/peer.yaml
```

### Peer not appearing on server

1. Verify the `server` URL is correct and reachable
2. Confirm `auth_token` matches the server configuration
3. Check firewall allows outbound connections to port 8080

### Peers can't connect to each other

1. Ensure `ssh_port` is open on at least one peer
2. Check that SSH keys were generated (`tunnelmesh init`)
3. Verify peers are registered (check admin dashboard)

### DNS resolution not working

1. Confirm `dns.enabled: true` in config
2. Check if DNS is listening: `netstat -ln | grep 5353`
3. Configure system to use the local resolver (see main README)

---

## Next Steps

- **Add more peers**: Repeat Part 2 on additional machines
- **Enable server as peer**: Add `join_mesh` section to server config (see main README)
- **Secure the server**: Put behind a reverse proxy with TLS
- **Set up exit peers**: Route internet traffic through specific peers (see below)

For full configuration options, see the [main README](../README.md).

---

## Part 3: Exit Peer Setup (Optional)

Exit peers allow you to route internet traffic through a specific peer while keeping mesh
traffic direct. This is useful for geo-unblocking or routing through a trusted exit point.

### Configure the Exit Peer

On the peer that will serve as the exit peer:

```bash
# Edit the peer config
sudo nano /etc/tunnelmesh/peer.yaml
```

Add:

```yaml
allow_exit_traffic: true
```

Restart the service:

```bash
tunnelmesh service restart
```

TunnelMesh automatically configures IP forwarding and NAT when `allow_exit_traffic` is enabled.

### Configure the Client

On peers that should route through the exit peer:

```bash
sudo nano /etc/tunnelmesh/peer.yaml
```

Add:

```yaml
exit_peer: "exit-peer-name"  # Name of your exit peer
```

Restart:

```bash
sudo tunnelmesh service restart
```

Internet traffic will now route through the exit peer, while mesh traffic stays direct.
