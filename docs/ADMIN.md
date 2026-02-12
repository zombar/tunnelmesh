# TunnelMesh Admin Guide

This guide explains how admin authorization works in TunnelMesh, what functionality requires admin access,
and security best practices.

## Overview

> [!IMPORTANT]
> TunnelMesh uses a **configuration-based admin model** where admin privileges are granted via the
> coordinator's `admin_peers` field. There is **no "first user becomes admin" behavior** - admins must be
> explicitly configured.

**Key points:**

- Admin peers are configured in the coordinator's config file
- Matching is done by **peer name** OR **peer ID** (16 hex characters)
- Peer IDs are preferred for security (immutable, tied to SSH key)
- Network functionality works without admin access
- Admin access enables mesh/data plane configuration

## Configuring Admin Peers

Admin peers are specified in the coordinator's configuration file:

```yaml
# coordinator.yaml
coordinator:
  enabled: true
  listen: ":8443"

  # Admin peers - match by name or peer ID
  admin_peers:
    - "coordinator"        # Peer name (convenient but mutable)
    - "alice"              # Peer name
    - "a1b2c3d4e5f6g7h8"   # Peer ID (preferred - 16 hex chars)
```

### Peer Name vs Peer ID Matching

TunnelMesh supports two methods for identifying admin peers:

#### Peer Name Matching

**Format**: Any valid peer name (hostname-style, e.g., "alice", "server-1")

**Pros**:

- Easy to configure before peers join
- Human-readable

**Cons**:

- Users can change their peer name
- Less secure
- Coordinator logs a warning when using name matching

**Example**:

```yaml
admin_peers: ["alice", "bob", "coordinator"]
```

#### Peer ID Matching (Recommended)

> [!TIP]
> **Recommended**: Use peer IDs for security. They're immutable and tied to SSH keys.

**Format**: 16 hexadecimal characters (SHA256 of SSH public key, first 8 bytes)

**Example peer ID**: `a1b2c3d4e5f6g7h8`

**Pros**:

- Immutable (tied to SSH key)
- Cannot be changed by user
- More secure
- Coordinator logs "matched by peer ID (secure)"

**Cons**:

- Must obtain peer ID first (requires peer to join once, or copy from `tunnelmesh status`)

**Example**:

```yaml
admin_peers: ["a1b2c3d4e5f6g7h8", "i9j0k1l2m3n4o5p6"]
```

### How to Get Peer IDs

There are two methods to obtain a peer's ID:

#### Method 1: After First Join

1. Have the peer join the mesh (with or without admin rights)
2. Run `tunnelmesh status` on the peer
3. Copy the peer ID from the output (16 hex characters)
4. Update coordinator config with the peer ID

```bash
# On the peer
tunnelmesh status

# Output includes:
# Peer ID: a1b2c3d4e5f6g7h8
```

#### Method 2: Calculate from SSH Key

If you have access to the peer's SSH public key:

```bash
# Extract peer ID from public key
ssh-keygen -l -f ~/.tunnelmesh/id_ed25519.pub | awk '{print $2}' | \
  base64 -d | xxd -p | head -c 16
```

This gives you the 16-character peer ID before the peer joins.

## How Admin Authorization Works

When a peer joins the mesh, the coordinator performs the following steps:

1. **Peer Registers**: Peer sends join request with public key
2. **Generate Peer ID**: Coordinator computes `SHA256(public_key)[:8]` as hex (16 chars)
3. **Check admin_peers**: Coordinator checks if peer name OR peer ID is in admin_peers list
4. **Grant Admin Access**: If matched:
   - Peer is added to "admins" group
   - Full admin RBAC role is granted
   - Admin-only panels become accessible
   - Logs show match reason: "matched by peer ID (secure)" or "matched by name (warning: mutable)"
5. **Regular Access**: If not matched:
   - Peer is added to "everyone" group only
   - Network functionality works normally
   - Admin features are denied

**Implementation location**: `internal/coord/server.go:1547-1573`

## What Works Without Admin Access

All core mesh networking features work for non-admin peers:

### Network Layer (Always Available)

- ✅ Join the mesh and register with coordinator
- ✅ Establish encrypted tunnels (SSH, UDP, or relay)
- ✅ Route IP traffic through the mesh
- ✅ Access services on other peers
- ✅ Use mesh DNS resolution (`peer.tunnelmesh`)
- ✅ NAT traversal (UDP hole-punching)
- ✅ Automatic transport fallback
- ✅ Network monitoring and reconnection

### Performance & Diagnostics

- ✅ Speed test to other peers (`tunnelmesh benchmark`)
- ✅ Check own connection status (`tunnelmesh status`)
- ✅ Resolve mesh hostnames (`tunnelmesh resolve`)

### VPN Features

- ✅ Use exit peers for split-tunnel VPN
- ✅ Serve as exit peer (if `allow_exit_traffic: true`)

### Storage (With RBAC Permissions)

- ✅ Access S3 buckets (if granted via RBAC bindings)
- ✅ List/read/write objects (based on role)
- ✅ Use file shares (if granted permissions)

### Dashboard (Limited)

- ✅ View non-admin panels: visualizer, map, s3, shares
- ✅ View mesh topology
- ✅ See geographic map (if location data available)

> [!NOTE]
> **Summary**: Non-admin peers can fully participate in the mesh network and access services.
> They just cannot configure or manage mesh-wide settings.

## What Requires Admin Access

Admin access is required for mesh/data plane **configuration and management**:

### Peer Management

- ❌ View detailed peer stats (bandwidth, latency, connection types)
- ❌ Force transport changes for peers
- ❌ View peer logs via admin panel
- ❌ View peer connection history

### Network Configuration

- ❌ Configure packet filter rules (port-based firewall)
- ❌ Create/modify/delete filter rules
- ❌ Set global filter policies
- ❌ View filter metrics

### WireGuard Management

- ❌ Manage WireGuard clients
- ❌ Generate client configs and QR codes
- ❌ Enable/disable WireGuard concentrator

### Storage Management

- ❌ Create S3 buckets
- ❌ Delete S3 buckets
- ❌ Create file shares
- ❌ Delete file shares
- ❌ Configure bucket policies

### User & Access Management

- ❌ View users and groups
- ❌ Create/delete users
- ❌ Manage group memberships
- ❌ Create RBAC role bindings
- ❌ Grant/revoke permissions

### Docker Orchestration

- ❌ View Docker containers
- ❌ Start/stop/restart containers
- ❌ View container stats and logs

### DNS Management

- ❌ Configure mesh DNS records
- ❌ View DNS resolver stats

### Dashboard Access

- ❌ Access admin panels: peers, logs, wireguard, filter, dns, users, groups, bindings, docker

> [!NOTE]
> **Summary**: Admin access is for **configuration**, not for basic mesh usage. If you just need to
> use the mesh network and access services, you don't need admin rights.

## Admin Panel Access

The admin dashboard is accessible at `https://this.tm/` from within the mesh.

### Panel Visibility

Panels are controlled by the RBAC system:

| Panel | Tab | Admin Only by Default | Grantable to Non-Admins |
| ------- | ----- | ---------------------- | ----------------------- |
| visualizer | mesh | No | N/A (public) |
| map | mesh | No | N/A (public) |
| peers | mesh | Yes | Yes (role binding) |
| logs | mesh | Yes | Yes (role binding) |
| wireguard | mesh | Yes | Yes (role binding) |
| filter | mesh | Yes | Yes (role binding) |
| dns | mesh | Yes | Yes (role binding) |
| s3 | data | No | N/A (public) |
| shares | data | No | N/A (public) |
| users | data | Yes | Yes (role binding) |
| groups | data | Yes | Yes (role binding) |
| bindings | data | Yes | Yes (role binding) |
| docker | data | Yes | Yes (role binding) |

### Granting Panel Access to Non-Admins

You can grant access to specific admin panels without full admin rights:

```bash
# Grant access to Docker panel only
tunnelmesh role bind alice panel-viewer --panel-scope docker

# Grant access to filter panel
tunnelmesh role bind bob panel-viewer --panel-scope filter

# Grant multiple panel access via group
tunnelmesh group create monitoring-users
tunnelmesh group add-member monitoring-users alice
tunnelmesh group bind monitoring-users panel-viewer --panel-scope peers
tunnelmesh group bind monitoring-users panel-viewer --panel-scope logs
```

See the [User Identity and RBAC Guide](USER_IDENTITY.md) for complete RBAC documentation.

## Security Considerations

### Use Peer IDs, Not Names

> [!WARNING]
> **Security Best Practice**: Always use peer IDs, not names, for admin_peers in production.
> Peer names can be changed by users, while peer IDs are immutable and tied to SSH keys.

**Recommended configuration**:

```yaml
coordinator:
  admin_peers: ["a1b2c3d4e5f6g7h8"]  # Peer ID - immutable
```

**Avoid** (except for initial setup):

```yaml
coordinator:
  admin_peers: ["alice"]  # Name - can be changed by user
```

**Why?**

- Peer names can be changed by users (via config or CLI)
- Peer IDs are derived from SSH keys (immutable)
- An attacker with SSH key access can change their peer name to match admin_peers
- Peer ID matching ensures only the correct SSH key holder gets admin access

### Initial Setup Pattern

For convenience during initial setup, you can use names temporarily:

1. **Bootstrap coordinator with name**:

   ```yaml
   admin_peers: ["coordinator"]
   ```

2. **Start coordinator and join**:

   ```bash
   tunnelmesh join
   ```

3. **Get peer ID**:

   ```bash
   tunnelmesh status  # Note the peer ID (16 hex chars)
   ```

4. **Update config with peer ID**:

   ```yaml
   admin_peers: ["a1b2c3d4e5f6g7h8"]  # Replace name with ID
   ```

5. **Restart coordinator**:

   ```bash
   sudo systemctl restart tunnelmesh
   ```

Now admin access is tied to the SSH key, not the mutable peer name.

### Protect Coordinator Config

> [!CAUTION]
> **Protect your coordinator config file**: The admin_peers list controls who gets admin access.
> Set restrictive permissions to prevent unauthorized modifications.

```bash
# Set restrictive permissions
sudo chown root:root /etc/tunnelmesh/coordinator.yaml
sudo chmod 600 /etc/tunnelmesh/coordinator.yaml
```

Only root (or the coordinator service user) should be able to read/write this file.

### Audit Admin Actions

Admin actions are logged by the coordinator. Monitor logs for suspicious activity:

```bash
# View coordinator logs
sudo journalctl -u tunnelmesh -f

# Grep for admin matches
sudo journalctl -u tunnelmesh | grep "matched by"
```

Look for:

- `matched by peer ID (secure)` - Normal, secure matching
- `matched by name (warning: mutable)` - Name matching (consider switching to peer ID)

### Revoke Admin Access

To revoke admin access:

1. **Remove from admin_peers list**:

   ```yaml
   admin_peers:
     # - "alice"  # Removed
     - "a1b2c3d4e5f6g7h8"
   ```

2. **Restart coordinator**:

   ```bash
   sudo systemctl restart tunnelmesh
   ```

3. **Peer must re-join** to lose admin privileges:
   - Peer's existing session retains admin until reconnect
   - On next heartbeat/reconnect, coordinator will not grant admin
   - Peer is moved from "admins" group to "everyone" group only

To force immediate revocation, use the admin panel to disconnect the peer.

## Examples

### Example 1: Single Admin (Peer ID)

**Scenario**: Coordinator peer is the only admin.

```yaml
# coordinator.yaml
name: "coordinator"

coordinator:
  enabled: true
  listen: ":8443"
  admin_peers:
    - "a1b2c3d4e5f6g7h8"  # Coordinator's peer ID
```

**Steps**:

1. Bootstrap coordinator: `tunnelmesh join`
2. Get peer ID: `tunnelmesh status`
3. Update config with peer ID
4. Restart: `sudo systemctl restart tunnelmesh`

**Result**: Only the coordinator peer (with specific SSH key) has admin access. All other peers have network access only.

### Example 2: Multiple Admins (Mixed Mode)

**Scenario**: Coordinator and two trusted peers are admins.

```yaml
# coordinator.yaml
coordinator:
  enabled: true
  listen: ":8443"
  admin_peers:
    - "a1b2c3d4e5f6g7h8"  # Coordinator peer ID
    - "i9j0k1l2m3n4o5p6"  # Alice's peer ID
    - "bob"              # Bob's name (temporary)
```

**Steps**:

1. Configure coordinator with coordinator's peer ID
2. Add Alice's peer ID (obtained after first join or from SSH key)
3. Temporarily add Bob's name until you get his peer ID
4. After Bob joins, get his peer ID: `tunnelmesh status` (run by Bob)
5. Replace "bob" with Bob's peer ID in config
6. Restart coordinator

**Result**: Three peers with admin access, all using immutable peer IDs.

### Example 3: Name-Based (Development Only)

**Scenario**: Local development mesh where security is not critical.

```yaml
# coordinator.yaml
coordinator:
  enabled: true
  listen: ":8443"
  admin_peers:
    - "coordinator"
    - "laptop"
    - "desktop"
```

**Warning**: Name matching is convenient but less secure. Only use in development/testing environments.

### Example 4: No Admins (Public Mesh)

**Scenario**: Public mesh where no one has admin access (monitoring only).

> [!WARNING]
> **Advanced use case**: With an empty admin_peers list, you won't be able to manage the mesh via UI.
> All configuration must be done via config files.

```yaml
# coordinator.yaml
coordinator:
  enabled: true
  listen: ":8443"
  admin_peers: []  # Empty list
```

**Result**:

- All peers have network functionality only
- No one can access admin panels or configure mesh settings
- Useful for read-only/monitoring deployments

## Troubleshooting

### "Access Denied" in Admin Panel

**Symptoms**: Can access visualizer/map panels, but admin panels show "Access Denied".

**Cause**: Your peer is not in the admin_peers list.

**Solution**:

1. Check if you're in admin_peers:

   ```bash
   # On coordinator
   grep admin_peers /etc/tunnelmesh/coordinator.yaml
   ```

2. Get your peer ID:

   ```bash
   tunnelmesh status  # Look for "Peer ID: abc123..."
   ```

3. Add your peer ID to admin_peers:

   ```yaml
   admin_peers:
     - "your-peer-id-here"
   ```

4. Restart coordinator:

   ```bash
   sudo systemctl restart tunnelmesh
   ```

5. Reconnect your peer (or wait for next heartbeat)

### Admin Access Works, Then Stops

**Symptoms**: Had admin access, now it's gone after restarting peer.

**Cause**: Coordinator config changed, or peer name changed.

**Solution**:

1. Check coordinator logs:

   ```bash
   sudo journalctl -u tunnelmesh | grep "matched by"
   ```

2. Look for:
   - No match logged = not in admin_peers
   - "matched by name" = using name matching (check if name changed)

3. Verify your peer ID hasn't changed (SSH key replacement):

   ```bash
   tunnelmesh status
   ```

4. Update admin_peers with correct peer ID or name

### Coordinator Logs "matched by name (warning: mutable)"

**Symptoms**: Admin access works, but logs show warning.

**Cause**: Using name matching instead of peer ID matching.

**Solution**: Switch to peer ID matching (recommended):

1. Get peer ID: `tunnelmesh status`
2. Replace name with peer ID in config
3. Restart coordinator

This is not an error, just a security recommendation.

### Multiple Peers, Wrong One Has Admin

**Symptoms**: Different peer than expected has admin access.

**Cause**: Name collision or peer ID mismatch.

**Solution**:

1. Check which peer matched:

   ```bash
   sudo journalctl -u tunnelmesh | grep "admin peer matched"
   ```

2. Verify peer IDs:

   ```bash
   # On each peer
   tunnelmesh status
   ```

3. Update admin_peers with correct peer IDs (not names)

### Can't Access Dashboard at All

**Symptoms**: Cannot access `https://this.tm/` from mesh peer.

**Cause**: Not a part of the mesh, or DNS/routing issue.

**Solution**:

1. Verify mesh connectivity:

   ```bash
   tunnelmesh status
   tunnelmesh peers
   ```

2. Check if coordinator is reachable:

   ```bash
   ping this.tm
   curl -k https://this.tm/
   ```

3. Check mesh DNS is working:

   ```bash
   tunnelmesh resolve coordinator
   ```

4. Verify TUN interface is up:

   ```bash
   ip addr show tun-mesh0
   ```

This is not an admin issue - it's a connectivity issue. See the
[Getting Started Guide](GETTING_STARTED.md) for troubleshooting mesh connectivity.

## Related Documentation

- **[User Identity and RBAC](USER_IDENTITY.md)** - User authentication, peer IDs, role-based access control
- **[Getting Started Guide](GETTING_STARTED.md)** - Initial setup and configuration
- **[CLI Reference](CLI.md)** - Command-line tools for management
- **[Internal Packet Filter](INTERNAL_PACKET_FILTER.md)** - Port-based firewall configuration

## Summary

**Key takeaways:**

- ✅ Admin access is **explicitly configured** via `admin_peers` field
- ✅ Use **peer IDs** (16 hex chars) for security, not peer names
- ✅ Network functionality works **without admin access**
- ✅ Admin access enables **configuration and management**
- ✅ Get peer ID with `tunnelmesh status`
- ✅ Protect coordinator config file with restrictive permissions
- ✅ Monitor logs for admin matches and security events

For quick setup: use peer names initially, then switch to peer IDs for production security.
