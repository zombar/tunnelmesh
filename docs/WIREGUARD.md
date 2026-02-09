# WireGuard Integration

TunnelMesh supports WireGuard clients through a **concentrator** architecture. This allows standard WireGuard apps on
phones, tablets, and laptops to connect to the mesh without running the full TunnelMesh client.

## How It Works

```text
                                   TunnelMesh Mesh Network
    ┌─────────────────────────────────────────────────────────────────────────┐
    │                                                                         │
    │   ┌─────────────┐         ┌─────────────┐         ┌─────────────┐      │
    │   │   Peer A    │◄───────►│ Coordinator │◄───────►│   Peer B    │      │
    │   │  (Server)   │  mesh   │             │  mesh   │  (Desktop)  │      │
    │   └─────────────┘ tunnel  └──────┬──────┘ tunnel  └─────────────┘      │
    │                                  │                                      │
    │                           ┌──────┴──────┐                               │
    │                           │  WireGuard  │                               │
    │                           │Concentrator │                               │
    │                           │   (wg0)     │                               │
    │                           └──────┬──────┘                               │
    │                                  │                                      │
    └──────────────────────────────────┼──────────────────────────────────────┘
                                       │
                          WireGuard Protocol (UDP)
                                       │
              ┌────────────────────────┼────────────────────────┐
              │                        │                        │
         ┌────▼────┐              ┌────▼────┐              ┌────▼────┐
         │   📱    │              │   📱    │              │   💻    │
         │ iPhone  │              │ Android │              │ Laptop  │
         │   App   │              │   App   │              │  Client │
         └─────────┘              └─────────┘              └─────────┘
```text

**Key concepts:**

- **Concentrator**: A TunnelMesh peer that terminates WireGuard connections
- **WireGuard Clients**: Standard WireGuard apps connecting to the concentrator
- **Admin Panel**: Web UI for managing WireGuard clients (add/remove/enable/disable)
- **QR Codes**: Instant mobile device configuration

---

## Quick Start

### 1. Enable WireGuard on a Peer

Add `--wireguard` when joining:

```bash
sudo tunnelmesh join \
  --server https://tunnelmesh.example.com \
  --token your-token \
  --name wg-gateway \
  --wireguard \
  --context wg-gateway
```text

This creates a context named "wg-gateway" that you can manage with `tunnelmesh context` commands.

Or in config:

```yaml
name: "wg-gateway"

# Coordination server (HTTPS) - where this peer registers with the mesh
server: "https://coord.example.com"
auth_token: "your-token"

wireguard:
  enabled: true
  listen_port: 51820

  # Public endpoint (UDP) - where WireGuard clients connect
  # This is YOUR public IP or hostname, not the coordinator!
  # If empty, clients will use this peer's detected public IP
  endpoint: "203.0.113.50:51820"  # or "wg.example.com:51820"
```text

**Important distinction:**

- `server` = Coordination server URL (HTTPS, port 443/8443) - mesh registration
- `endpoint` = This peer's WireGuard endpoint (UDP, port 51820) - where mobile clients connect

### 2. Add a Client via Admin Panel

1. Access admin dashboard: `https://this.tm/`
2. Navigate to **WireGuard** section
3. Click **Add Client**
4. Enter client name (e.g., "iPhone", "Work-Laptop")
5. Scan QR code with WireGuard app

### 3. Connect from WireGuard App

**iOS/Android:**

1. Open WireGuard app
2. Tap **+** > **Create from QR code**
3. Scan the QR code from admin panel
4. Activate the tunnel

**Desktop (macOS/Windows/Linux):**

1. Copy config from admin panel
2. Import into WireGuard app
3. Activate

---

## Configuration Reference

### Peer Configuration

```yaml
# Full peer config with WireGuard enabled
name: "wg-gateway"
server: "https://coord.example.com"    # Coordination server (HTTPS)
auth_token: "your-64-char-hex-token"

wireguard:
  # Enable WireGuard concentrator on this peer
  enabled: true

  # UDP port to listen on (ensure firewall allows this)
  listen_port: 51820

  # Public endpoint for WireGuard clients to connect to
  # This is THIS PEER's public address, not the coordinator!
  # Format: "ip:port" or "hostname:port"
  # If empty/omitted, uses this peer's auto-detected public IP
  endpoint: "203.0.113.50:51820"

  # Persistent storage for client keys and assignments
  # Default: ~/.tunnelmesh/wireguard/
  data_dir: "/var/lib/tunnelmesh/wireguard"
```text

### Terraform/Cloud Configuration

```hcl
nodes = {
  "wg-gateway" = {
    peer      = true
    wireguard = true
    wg_port   = 51820
    region    = "nyc3"
  }
}
```text

---

## Client Management

### Via Admin Panel

The admin panel provides a visual interface for client management:

**Add Client:**

1. Go to WireGuard section
2. Click "Add Client"
3. Enter name and optional description
4. QR code and config are generated automatically

**Manage Clients:**

- **Enable/Disable**: Toggle client access without deleting
- **Delete**: Permanently remove client
- **View Config**: See client configuration
- **Download Config**: Get .conf file for desktop clients

### Via API

Clients can also be managed programmatically:

```bash
# List clients
curl -H "Authorization: Bearer $TOKEN" \
  https://this.tm/api/v1/wireguard/clients

# Add client
curl -X POST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name": "my-phone"}' \
  https://this.tm/api/v1/wireguard/clients

# Delete client
curl -X DELETE -H "Authorization: Bearer $TOKEN" \
  https://this.tm/api/v1/wireguard/clients/CLIENT_ID
```text

---

## Network Topology

### IP Allocation

WireGuard clients receive IPs from a dedicated subnet within the mesh CIDR:

| Network | Range | Purpose |
| --------- | ------- | --------- |
| Mesh CIDR | 172.30.0.0/16 | Full mesh network |
| Peer IPs | 172.30.0.1 - 172.30.99.255 | Native TunnelMesh peers |
| WG Clients | 172.30.100.0 - 172.30.255.255 | WireGuard clients |

**Example allocation:**

- Coordinator: 172.30.0.1
- Desktop peer: 172.30.0.5
- Server peer: 172.30.0.10
- WG Concentrator interface: 172.30.100.1
- iPhone (WG client): 172.30.100.2
- Android (WG client): 172.30.100.3

### DNS Names

WireGuard clients get mesh DNS names:

```text
iphone.tunnelmesh    -> 172.30.100.2
android.tunnelmesh   -> 172.30.100.3
laptop.tunnelmesh    -> 172.30.100.4
```text

---

## Deployment Scenarios

### Scenario 1: All-in-One (Simplest)

Single peer acts as coordinator, peer, and WireGuard gateway:

```yaml
# server.yaml
listen: ":8080"
auth_token: "your-token"

join_mesh:
  name: "all-in-one"
  wireguard:
    enabled: true
    listen_port: 51820
```text

```bash
sudo tunnelmesh serve --config server.yaml
```text

**Pros:** Simplest setup, single server to maintain
**Cons:** Single point of failure, all traffic through one node

### Scenario 2: Dedicated WireGuard Gateway

Separate the WireGuard concentrator from the coordinator:

```text
┌────────────────────┐          ┌────────────────────┐
│    Coordinator     │          │   WireGuard Peer   │
│   (no WireGuard)   │◄────────►│   (concentrator)   │
│   tunnelmesh.com   │   mesh   │   wg.tunnelmesh    │
└────────────────────┘          └─────────┬──────────┘
                                          │
                                     WireGuard
                                          │
                                    ┌─────▼─────┐
                                    │   📱📱💻  │
                                    │  Clients  │
                                    └───────────┘
```text

**Coordinator:**

```yaml
listen: ":8080"
auth_token: "your-token"
join_mesh:
  name: "coordinator"
  # No wireguard here
```text

**WireGuard Peer:**

```yaml
name: "wg-gateway"
server: "https://tunnelmesh.example.com"
auth_token: "your-token"
wireguard:
  enabled: true
  listen_port: 51820
  endpoint: "wg.example.com:51820"
```text

**Pros:** Scale WireGuard independently, better isolation
**Cons:** More infrastructure to manage

### Scenario 3: Multi-Region WireGuard

WireGuard gateways in multiple regions for low-latency access:

```text
                         ┌─────────────────────┐
                         │     Coordinator     │
                         │      Amsterdam      │
                         └──────────┬──────────┘
                                    │
        ┌───────────────────────────┼───────────────────────────┐
        │                           │                           │
┌───────▼───────┐          ┌────────▼────────┐         ┌────────▼────────┐
│  WG Gateway   │          │   WG Gateway    │         │   WG Gateway    │
│   New York    │          │   Frankfurt     │         │   Singapore     │
│ wg-us.example │          │ wg-eu.example   │         │ wg-asia.example │
└───────┬───────┘          └────────┬────────┘         └────────┬────────┘
        │                           │                           │
   US Clients               EU Clients                 Asia Clients
```text

**Terraform configuration:**

```hcl
nodes = {
  "coordinator" = {
    coordinator = true
    peer        = true
    region      = "ams3"
  }
  "wg-us" = {
    peer      = true
    wireguard = true
    region    = "nyc3"
  }
  "wg-eu" = {
    peer      = true
    wireguard = true
    region    = "fra1"
  }
  "wg-asia" = {
    peer      = true
    wireguard = true
    region    = "sgp1"
  }
}
```text

**Client configuration:**
Users connect to their nearest gateway for lowest latency. All gateways provide access to the full mesh.

---

## Client Configuration Examples

### iOS/Android

1. Install WireGuard app from App Store / Play Store
2. Scan QR code from admin panel
3. Toggle on to connect

The generated config looks like:

```ini
[Interface]
PrivateKey = <auto-generated>
Address = 172.30.100.5/16
DNS = 172.30.0.1

[Peer]
PublicKey = <concentrator-public-key>
AllowedIPs = 172.30.0.0/16
Endpoint = wg.example.com:51820
PersistentKeepalive = 25
```text

### macOS

**Using WireGuard app:**

1. Download from App Store
2. File > Import Tunnel(s) from File
3. Paste config from admin panel
4. Click Activate

**Using command line:**

```bash
# Install wireguard-tools
brew install wireguard-tools

# Create config
sudo mkdir -p /etc/wireguard
sudo nano /etc/wireguard/tunnelmesh.conf
# Paste config from admin panel

# Connect
sudo wg-quick up tunnelmesh

# Disconnect
sudo wg-quick down tunnelmesh
```text

### Linux

```bash
# Install WireGuard
sudo apt install wireguard  # Debian/Ubuntu
sudo dnf install wireguard-tools  # Fedora

# Create config
sudo nano /etc/wireguard/tunnelmesh.conf
# Paste config from admin panel

# Connect
sudo wg-quick up tunnelmesh

# Auto-start on boot
sudo systemctl enable wg-quick@tunnelmesh

# Disconnect
sudo wg-quick down tunnelmesh
```text

### Windows

1. Download WireGuard from <https://wireguard.com/install/>
2. Click "Add Tunnel" > "Add empty tunnel..."
3. Paste config from admin panel
4. Click "Activate"

---

## Split Tunneling

By default, WireGuard clients only route mesh traffic (172.30.0.0/16) through the tunnel. Internet traffic goes
directly.

### Full Tunnel (Route All Traffic)

To route all internet traffic through the mesh:

1. Configure an exit peer in the mesh
2. Modify client config:

```ini
[Peer]
AllowedIPs = 0.0.0.0/0, ::/0
```text

This sends all traffic through the WireGuard tunnel, then through the exit peer.

### Custom Split Tunnel

Route specific subnets through the tunnel:

```ini
[Peer]
AllowedIPs = 172.30.0.0/16, 10.0.0.0/8, 192.168.1.0/24
```text

---

## Troubleshooting

### Client Can't Connect

**Check concentrator is running:**

```bash
tunnelmesh status
# Look for "WireGuard: enabled"
```text

**Check UDP port is open:**

```bash
# On concentrator
 sudo ss -ulnp | grep 51820 

# Test from client network
nc -zvu wg.example.com 51820
```text

**Check firewall:**

```bash
# Linux
sudo ufw allow 51820/udp

# DigitalOcean/AWS - check security groups
```text

### Client Connected But No Traffic

**Verify IP allocation:**

```bash
# On client
wg show

# Should show handshake and transfer stats
```text

**Test DNS:**

```bash
# From WireGuard client
ping 172.30.0.1  # Coordinator IP
dig @172.30.0.1 peer-name.tunnelmesh
```text

**Check routing:**

```bash
# On client
 ip route | grep 172.30 

# Should show route via WireGuard interface
```text

### Handshake Fails

**Check keys match:**

- Admin panel shows concentrator public key
- Client config must have matching peer public key

**Check endpoint:**

- Ensure `Endpoint` in client config is reachable
- Try IP instead of hostname

**Check time sync:**

- WireGuard uses timestamps in handshake
- Ensure both sides have correct time

### High Latency

**Choose closest gateway:**
If using multi-region deployment, connect to nearest WireGuard gateway.

**Check MTU:**

```bash
# On client, try smaller MTU
ping -s 1400 172.30.0.1
```text

If packets fragment, reduce MTU in client config:

```ini
[Interface]
MTU = 1380
```text

---

## Security Considerations

### Key Management

- Private keys are generated locally, never transmitted
- Each client has unique key pair
- Revoke access by deleting client from admin panel

### Network Isolation

- WireGuard clients can only reach the mesh network
- No direct internet access through concentrator (unless exit peer configured)
- Clients cannot communicate with each other directly (goes through mesh)

### Access Control

- Disable clients temporarily without deleting keys
- Delete clients to permanently revoke access
- Monitor client activity in admin panel

### Best Practices

1. **Use unique clients per device** - Don't share configs between devices
2. **Name clients clearly** - "iPhone-John" not "phone1"
3. **Review client list regularly** - Remove unused clients
4. **Rotate keys periodically** - Delete and recreate clients yearly
5. **Monitor handshakes** - Stale handshakes may indicate compromised keys

---

## Advanced Configuration

### Custom DNS

Override DNS servers for WireGuard clients:

```ini
[Interface]
DNS = 172.30.0.1, 1.1.1.1
```text

### Persistent Keepalive

Required for NAT traversal. Default is 25 seconds:

```ini
[Peer]
PersistentKeepalive = 25
```text

Increase for very stable connections, decrease for battery savings on mobile.

### Multiple Peers

WireGuard clients can only connect to one concentrator at a time. For redundancy, configure multiple tunnels and switch
manually.

---

## Integration with Mobile MDM

For enterprise deployments, WireGuard configs can be distributed via MDM:

**Apple (iOS/macOS):**

- Use Configuration Profile with VPN payload
- Deploy via Jamf, Mosyle, or Apple Business Manager

**Android:**

- Use managed configurations
- Deploy via Google Workspace or Intune

**Config template:**

```xml
<dict>
  <key>VPN</key>
  <dict>
    <key>WireGuard</key>
    <dict>
      <key>Interface</key>
      <dict>
        <key>PrivateKey</key>
        <string>{{PRIVATE_KEY}}</string>
        <key>Address</key>
        <string>{{MESH_IP}}/16</string>
      </dict>
      <key>Peers</key>
      <array>
        <dict>
          <key>PublicKey</key>
          <string>{{CONCENTRATOR_PUBKEY}}</string>
          <key>Endpoint</key>
          <string>{{ENDPOINT}}</string>
          <key>AllowedIPs</key>
          <string>172.30.0.0/16</string>
        </dict>
      </array>
    </dict>
  </dict>
</dict>
```text
