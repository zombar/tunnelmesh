# Cloud Deployment with Terraform

Deploy TunnelMesh infrastructure to DigitalOcean using Terraform. This guide covers various deployment scenarios from
simple single-node setups to multi-region mesh networks.

## Prerequisites

- [Terraform](https://developer.hashicorp.com/terraform/install) installed
- DigitalOcean account with API token
- Domain managed in DigitalOcean DNS
- SSH key uploaded to DigitalOcean

## Quick Start

```bash
cd terraform
cp terraform.tfvars.example terraform.tfvars

# Set your DO token
export TF_VAR_do_token="dop_v1_xxx"

# Generate auth token
openssl rand -hex 32  # For auth_token

# Edit terraform.tfvars with your domain and tokens

# Deploy
terraform init
terraform apply
```

---

## Deployment Scenarios

TunnelMesh is flexible. Whether you need a simple personal VPN, a global team mesh, or a sophisticated multi-region
network with exit peers, there's a configuration for you.

### Scenario 1: All-in-One (Starter)

**The simplest deployment.** A single $4/month droplet runs everything: coordinator, mesh peer, and WireGuard
concentrator. Perfect for personal use, small teams, or testing.

```text
                    ┌─────────────────────────────────────┐
                    │         tunnelmesh.example.com      │
                    │         ━━━━━━━━━━━━━━━━━━━━━       │
                    │                                     │
                    │   ┌─────────────────────────────┐   │
                    │   │    Coordinator + Peer       │   │
                    │   │    + WireGuard Gateway      │   │
                    │   │    + Exit Peer              │   │
                    │   └─────────────────────────────┘   │
                    │              10.42.0.1              │
                    └──────────────────┬──────────────────┘
                                       │
           ┌───────────────────────────┼───────────────────────────┐
           │                           │                           │
      ┌────▼────┐                ┌─────▼─────┐              ┌──────▼──────┐
      │  📱     │                │  💻       │              │  🏠         │
      │ iPhone  │                │  Laptop   │              │  Home PC    │
      │ (WG)    │                │  (native) │              │  (native)   │
      └─────────┘                └───────────┘              └─────────────┘
```

**Use cases:**

- Personal VPN for travel
- Small team (2-5 people) secure communication
- Home lab remote access
- Learning and experimentation

**Configuration:**

```text
nodes = {
  "tunnelmesh" = {
    coordinator        = true
    peer               = true
    wireguard          = true
    allow_exit_traffic = true
  }
}
```

**Cost:** ~$4/month

---

### Scenario 2: Coordinator + WireGuard Gateway

**Separate concerns.** The coordinator handles orchestration while a dedicated peer provides WireGuard access for mobile
devices. Better isolation and the ability to place the WireGuard endpoint closer to your users.

```text
┌─────────────────────────────┐          ┌─────────────────────────────┐
│     tunnelmesh.example.com  │          │       wg.example.com        │
│     ━━━━━━━━━━━━━━━━━━━━━   │          │       ━━━━━━━━━━━━━━        │
│                             │          │                             │
│   ┌─────────────────────┐   │   mesh   │   ┌─────────────────────┐   │
│   │    Coordinator      │◄──┼──────────┼──►│    WireGuard Peer   │   │
│   │    (no peer)        │   │  tunnel  │   │                     │   │
│   └─────────────────────┘   │          │   └─────────────────────┘   │
│                             │          │          10.42.0.2          │
└─────────────────────────────┘          └──────────────┬──────────────┘
                                                        │
                                              ┌─────────┼─────────┐
                                              │         │         │
                                         ┌────▼───┐ ┌───▼───┐ ┌───▼───┐
                                         │   📱   │ │  📱   │ │  💻   │
                                         │ Phone  │ │ Tablet│ │Laptop │
                                         └────────┘ └───────┘ └───────┘
```

**Use cases:**

- Place WireGuard endpoint in a region closer to mobile users
- Reduce attack surface on the coordinator
- Scale WireGuard capacity independently

**Configuration:**

```hcl
nodes = {
  "tunnelmesh" = {
    coordinator = true
    peer        = true
  }
  "tm-wg" = {
    peer      = true
    wireguard = true
    region    = "nyc3"  # Closer to US users
  }
}
```

**Cost:** ~$8/month (2 droplets)

---

### Scenario 3: Exit Peer (Split-Tunnel VPN)

**Route internet traffic through a specific location.** Your traffic exits from a peer in another region while
mesh-to-mesh communication stays direct. Great for privacy, accessing geo-restricted content, or compliance
requirements.

```text
┌──────────────────┐                               ┌──────────────────┐
│   Your Laptop    │                               │   Exit Peer      │
│   London, UK     │                               │   Singapore      │
│                  │        Encrypted Mesh         │                  │
│  ┌────────────┐  │◄──────────────────────────────│  ┌────────────┐  │
│  │ TUN Device │  │          Tunnel               │  │ NAT/Egress │──┼──► Internet
│  └────────────┘  │                               │  └────────────┘  │   (appears as
│                  │                               │                  │    Singapore IP)
└──────────────────┘                               └──────────────────┘

         │ Mesh traffic stays direct
         ▼
┌──────────────────┐
│   Other Peer     │
│   Amsterdam      │
└──────────────────┘
```

**Use cases:**

- Access geo-restricted streaming services
- Privacy: your ISP sees encrypted tunnel traffic, not destinations
- Compliance: ensure traffic exits from a specific jurisdiction
- Bypass censorship in restrictive networks

**Configuration:**

```hcl
nodes = {
  "tunnelmesh" = {
    coordinator = true
    peer        = true
    wireguard   = true
    region      = "ams3"
  }
  "tm-exit-sgp" = {
    peer               = true
    region             = "sgp1"
    allow_exit_traffic = true  # Accept traffic from other peers
    location = {
      latitude  = 1.3521
      longitude = 103.8198
      city      = "Singapore"
      country   = "Singapore"
    }
  }
}
```

On your local machine:

```bash
sudo tunnelmesh join --config peer.yaml --exit-node tm-exit-sgp --context work
```

---

### Scenario 4: Multi-Region Mesh

**Global presence.** WireGuard entry points in multiple regions provide low-latency access for a distributed team. Users
connect to their nearest gateway and gain access to the entire mesh.

```text
                              ┌─────────────────────────────────┐
                              │         Coordinator             │
                              │         Amsterdam               │
                              │                                 │
                              │   • Peer registry               │
                              │   • IP allocation               │
                              │   • Admin dashboard             │
                              └────────────────┬────────────────┘
                                               │
              ┌────────────────────────────────┼────────────────────────────────┐
              │                                │                                │
    ┌─────────▼─────────┐          ┌──────────▼──────────┐          ┌──────────▼──────────┐
    │   WireGuard Peer  │          │   WireGuard Peer    │          │   WireGuard Peer    │
    │   New York        │◄─────────►   Frankfurt         │◄─────────►   Singapore         │
    │                   │   mesh   │                     │   mesh   │                     │
    │   10.42.0.2       │          │   10.42.0.3         │          │   10.42.0.4         │
    └─────────┬─────────┘          └──────────┬──────────┘          └──────────┬──────────┘
              │                               │                                │
       ┌──────┴──────┐                 ┌──────┴──────┐                 ┌───────┴──────┐
       │   US Team   │                 │   EU Team   │                 │   APAC Team  │
       │   📱💻💻     │                 │   📱📱💻     │                 │   💻📱        │
       └─────────────┘                 └─────────────┘                 └──────────────┘
```

**Use cases:**

- Distributed development teams
- Global gaming groups wanting low-latency connections
- International organizations with regional offices
- Content creators collaborating across time zones

**Configuration:**

```hcl
nodes = {
  "tunnelmesh" = {
    coordinator = true
    peer        = true
    wireguard   = true
    region      = "ams3"
  }
  "tm-us" = {
    peer      = true
    wireguard = true
    region    = "nyc3"
  }
  "tm-asia" = {
    peer      = true
    wireguard = true
    region    = "sgp1"
  }
}
```

**Cost:** ~$12/month (3 droplets)

---

### Scenario 5: Home Lab Gateway

**Access your home network from anywhere.** Run a cloud coordinator and connect your home server as a peer. Mobile
devices connect via WireGuard and can reach everything on your home LAN.

```text
                          Cloud (DigitalOcean)
        ┌─────────────────────────────────────────────────┐
        │                                                 │
        │   ┌─────────────────────────────────────────┐   │
        │   │         Coordinator + WireGuard         │   │
        │   │         tunnelmesh.example.com          │   │
        │   └─────────────────────────────────────────┘   │
        │                        │                        │
        └────────────────────────┼────────────────────────┘
                                 │
                    Encrypted Mesh Tunnel
                                 │
                                 ▼
        ┌─────────────────────────────────────────────────┐
        │                  Your Home                      │
        │                                                 │
        │   ┌─────────────┐      ┌───────────────────┐    │
        │   │ Home Server │──────│  Home LAN         │    │
        │   │ (TunnelMesh │      │  • NAS            │    │
        │   │  Peer)      │      │  • Cameras        │    │
        │   │ 10.42.0.2   │      │  • Smart Home     │    │
        │   └─────────────┘      │  • Printers       │    │
        │                        └───────────────────┘    │
        └─────────────────────────────────────────────────┘
                                 ▲
                                 │
                    ┌────────────┴────────────┐
                    │                         │
               ┌────▼────┐              ┌─────▼─────┐
               │   📱    │              │    💻     │
               │ Phone   │              │  Laptop   │
               │(coffee  │              │ (hotel)   │
               │ shop)   │              │           │
               └─────────┘              └───────────┘
```

**Use cases:**

- Access home NAS and media server while traveling
- Check security cameras remotely
- SSH into home machines
- Run home automation from anywhere

**Configuration:**

```hcl
# Cloud
nodes = {
  "tunnelmesh" = {
    coordinator = true
    peer        = true
    wireguard   = true
  }
}
```

```yaml
# Home server peer config
name: "homelab"

# DNS is always enabled
dns:
  aliases:
    - "nas"
    - "plex"
    - "homeassistant"
```

On the home server:

```bash
sudo tunnelmesh join tunnelmesh.example.com --token your-mesh-token --config peer.yaml --context homelab
sudo tunnelmesh service install
sudo tunnelmesh service start
```

---

### Scenario 6: Development Team Secure Mesh

**Connect developer machines directly.** No VPN concentrator bottleneck. Developers can SSH into each other's machines,
share local development servers, and collaborate as if on the same LAN.

```text
                              ┌─────────────────────────────────┐
                              │         Coordinator             │
                              │    (minimal cloud instance)     │
                              └────────────────┬────────────────┘
                                               │
       ┌───────────────────────────────────────┼───────────────────────────────────────┐
       │                       │               │               │                       │
       │                       │               │               │                       │
┌──────▼──────┐         ┌──────▼──────┐ ┌──────▼──────┐ ┌──────▼──────┐         ┌──────▼──────┐
│   alice     │◄───────►│    bob      │ │   charlie   │ │   david     │◄───────►│   eve       │
│  (MacBook)  │  direct │  (Linux)    │ │  (Windows)  │ │  (MacBook)  │  direct │  (Linux)    │
│             │  tunnel │             │ │             │ │             │  tunnel │             │
│ 10.42.0.2   │         │ 10.42.0.3   │ │ 10.42.0.4   │ │ 10.42.0.5   │         │ 10.42.0.6   │
└─────────────┘         └─────────────┘ └─────────────┘ └─────────────┘         └─────────────┘
       │                                                                               │
       │  alice$ ssh bob.tunnelmesh                                                    │
       │  alice$ curl http://eve.tunnelmesh:3000   # Access Eve's dev server           │
       │  eve$ psql -h alice.tunnelmesh            # Connect to Alice's local Postgres │
```

**Use cases:**

- Pair programming with remote colleagues
- Share local development servers without ngrok
- Access team members' databases for debugging
- Collaborative CTF/security research

**Configuration:**

```hcl
# Just the coordinator in the cloud
nodes = {
  "tunnelmesh" = {
    coordinator = true
  }
}
```

Each developer runs:

```bash
sudo tunnelmesh join tunnelmesh.example.com --token team-token --context team
```

---

### Scenario 7: Gaming Group Low-Latency Mesh

**Direct connections for multiplayer gaming.** Skip the public internet. Peers connect directly via UDP for minimal
latency. Host game servers on any peer's machine.

```text
                              ┌─────────────────────────────────┐
                              │         Coordinator             │
                              │    (handles discovery only)     │
                              └────────────────┬────────────────┘
                                               │
                                               │ (control plane only)
       ┌───────────────────────────────────────┼───────────────────────────────────────┐
       │                                       │                                       │
       │                                       │                                       │
┌──────▼──────┐                         ┌──────▼──────┐                         ┌──────▼──────┐
│   Player 1  │◄═══════════════════════►│  Player 2   │◄═══════════════════════►│  Player 3   │
│  California │      UDP Tunnel         │   Texas     │      UDP Tunnel         │  Florida    │
│             │      (low latency)      │             │      (low latency)      │             │
│ 10.42.0.2   │                         │ 10.42.0.3   │                         │ 10.42.0.4   │
│             │                         │             │                         │             │
│ Game Server │◄════════════════════════│ ═══════════ │════════════════════════►│             │
│ 192.168.x.x │      Direct Connect     │             │      Direct Connect     │             │
└─────────────┘                         └─────────────┘                         └─────────────┘

                    ═════  UDP tunnel (game traffic, ~10-30ms between peers)
                    ─────  Control plane (HTTPS to coordinator)
```

**Use cases:**

- Minecraft servers with friends
- LAN party games over the internet
- Competitive gaming with minimal latency
- Game streaming between peers

**Configuration:**

```hcl
nodes = {
  "tunnelmesh" = {
    coordinator = true  # Just coordination, no game traffic
  }
}
```

Players join from their gaming PCs:

```bash
# Automatic UDP hole-punching for lowest latency
sudo tunnelmesh join tunnelmesh.example.com \
  --token game-token \
  --name player1 \
  --context gaming
```

---

## Node Configuration Reference

Each peer in the `nodes` map supports these options:

### Core Options

| Option | Type | Description |
| -------- | ------ | ------------- |
| `coordinator` | bool | Enable coordinator services on this peer (coordinators discover each other via P2P) |
| `peer` | bool | Join mesh as a peer |
| `wireguard` | bool | Enable WireGuard concentrator for mobile clients |

### Exit Peer Options

| Option | Type | Description |
| -------- | ------ | ------------- |
| `allow_exit_traffic` | bool | Allow other peers to route internet through this peer |
| `exit_peer` | string | Route this node's internet through specified peer |

### Infrastructure Options

| Option | Type | Default | Description |
| -------- | ------ | --------- | ------------- |
| `region` | string | `ams3` | DigitalOcean region |
| `size` | string | `s-1vcpu-512mb-10gb` | Droplet size |
| `wg_port` | number | `51820` | WireGuard UDP port |
| `ssh_port` | number | `2222` | SSH tunnel port |
| `tags` | list | `[]` | Additional droplet tags |

### Location Options

| Option | Type | Description |
| -------- | ------ | ------------- |
| `location.latitude` | number | Manual GPS latitude |
| `location.longitude` | number | Manual GPS longitude |
| `location.city` | string | City name for display |
| `location.country` | string | Country name/code |

### DNS Options

| Option | Type | Description |
| -------- | ------ | ------------- |
| `dns_aliases` | list | Additional DNS names for this peer |

---

## Global Configuration Reference

### Required Variables

| Variable | Description |
| ---------- | ------------- |
| `domain` | Your domain (must be in DigitalOcean DNS) |
| `auth_token` | Mesh authentication token (`openssl rand -hex 32`) |

Set your DigitalOcean API token via environment:

```bash
export TF_VAR_do_token="dop_v1_xxx"
```

### Default Settings

| Variable | Default | Description |
| ---------- | --------- | ------------- |
| `default_region` | `ams3` | Default droplet region |
| `default_droplet_size` | `s-1vcpu-512mb-10gb` | Default size ($4/mo) |
| `default_wg_port` | `51820` | Default WireGuard port |
| `default_ssh_port` | `2222` | Default SSH tunnel port |
| `external_api_port` | `8443` | HTTPS port for peer connections |

### Feature Flags

| Variable | Default | Description |
| ---------- | --------- | ------------- |
| `locations_enabled` | `false` | Geographic peer visualization (uses ip-api.com) |
| `monitoring_enabled` | `false` | Prometheus/Grafana/Loki stack |
| `auto_update_enabled` | `true` | Automatic binary updates |
| `auto_update_schedule` | `hourly` | Update check frequency |

### Monitoring Settings

| Variable | Default | Description |
| ---------- | --------- | ------------- |
| `prometheus_retention_days` | `3` | Metrics retention |
| `loki_retention_days` | `3` | Log retention |

---

## Monitoring Stack

Enable observability with `monitoring_enabled = true`:

```hcl
monitoring_enabled        = true
prometheus_retention_days = 7
loki_retention_days       = 7
```

### Included Services

| Service | Purpose | Access |
| --------- | --------- | -------- |
| Prometheus | Metrics collection | `/prometheus/` |
| Grafana | Dashboards | `/grafana/` (admin/admin) |
| Loki | Log aggregation | Internal |
| SD Generator | Auto-discovers peers | Internal |

### Pre-configured Alerts

- Peer disconnections
- Packet drops and error rates
- WireGuard status
- Resource utilization

Access Grafana from within the mesh:

```text
https://tunnelmesh.example.com/grafana/
```

---

## Node Location Tracking

The `locations_enabled` flag enables a world map visualization showing where your mesh peers are located.

**Disabled by default** because it:

1. Uses external API (ip-api.com) for geolocation
2. Sends peer public IPs to external service
3. Requires coordinator internet access

### Manual Coordinates

Override IP geolocation with precise coordinates:

```hcl
nodes = {
  "datacenter-1" = {
    peer = true
    location = {
      latitude  = 52.3676
      longitude = 4.9041
      city      = "Amsterdam"
      country   = "NL"
    }
  }
}
```

---

## Outputs

After `terraform apply`:

```bash
terraform output
```

| Output | Description |
| -------- | ------------- |
| `coord_url` | Coordinator URL |
| `admin_url` | Admin dashboard URL |
| `peer_config_example` | Example peer configuration |
| `node_ips` | Map of peer names to IPs |

---

## Managing the Deployment

### Update Configuration

```bash
vim terraform.tfvars
terraform apply
```

### View Logs

```bash
ssh root@<node-ip> journalctl -u tunnelmesh -f
```

### Destroy

```bash
terraform destroy
```

---

## Troubleshooting

### Peers Can't Connect

1. Check auth tokens match
2. Verify firewall allows ports 8443 (HTTPS), 2222 (SSH), 51820 (WireGuard)
3. Check coordinator logs: `journalctl -u tunnelmesh`

### WireGuard Clients Timeout

1. Verify UDP port 51820 is open
2. Check WireGuard peer is running: `tunnelmesh status`
3. Regenerate client config from admin panel

### High Latency

1. Check transport type (UDP is fastest): `tunnelmesh peers`
2. Verify direct connectivity (not relaying)
3. Consider adding regional nodes

---

## Cost Reference

| Configuration | Droplets | Monthly Cost |
| --------------- | ---------- | -------------- |
| All-in-One | 1 | ~$4 |
| Coord + WG Peer | 2 | ~$8 |
| Multi-Region (3) | 3 | ~$12 |
| Full Production | 4+ | ~$16+ |

All estimates use `s-1vcpu-512mb-10gb` ($4/mo) droplets. Monitoring adds minimal overhead as it runs on existing nodes.

---

## Security Best Practices

1. **Strong tokens**: `openssl rand -hex 32` for auth token
2. **Rotate periodically**: Update tokens and redeploy
3. **Mesh-only admin**: Admin dashboard only accessible from within the mesh network
4. **Enable monitoring**: Visibility into access patterns
5. **Auto-updates**: Keep peers patched

---

## Example: Complete terraform.tfvars

```hcl
# Required
domain      = "example.com"
auth_token  = "your-64-char-hex-auth-token"

# Peers - Multi-region with exit peer
nodes = {
  "tunnelmesh" = {
    coordinator        = true
    peer               = true
    wireguard          = true
    allow_exit_traffic = true
    region             = "ams3"
  }
  "tm-exit-us" = {
    peer               = true
    allow_exit_traffic = true
    region             = "nyc3"
    location = {
      latitude  = 40.7128
      longitude = -74.0060
      city      = "New York"
      country   = "US"
    }
  }
  "tm-wg-asia" = {
    peer      = true
    wireguard = true
    region    = "sgp1"
  }
}

# Settings
ssh_key_name       = "my-key"
locations_enabled  = true
monitoring_enabled = true

# Retention
prometheus_retention_days = 14
loki_retention_days       = 7
```
