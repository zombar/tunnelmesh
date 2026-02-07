# User Identity and RBAC

TunnelMesh provides a portable cryptographic identity system with Kubernetes-style RBAC for access control.

## Overview

The user identity system provides:
- **Portable Identity**: Same identity works across multiple meshes
- **Cryptographic Authentication**: ED25519 keypair derived from mnemonic
- **Simple Recovery**: 3-word mnemonic phrase for backup
- **Per-Mesh Registration**: Register with each mesh independently
- **RBAC Authorization**: Fine-grained access control

## User Setup

### Create a New Identity

Generate a new identity with a 3-word recovery phrase:

```bash
tunnelmesh user setup --name "Alice"
```

Output:
```
Your recovery phrase is: apple river mountain
Store this safely - you need it to recover your identity.

User ID: a1b2c3d4e5f67890
Public Key: AQID...
```

The identity is stored in `~/.tunnelmesh/user.json`.

### Recover an Identity

Restore an existing identity from your recovery phrase:

```bash
tunnelmesh user recover
# Enter your 3-word recovery phrase: apple river mountain
```

### View Current Identity

```bash
tunnelmesh user info
```

Output:
```
Identity:
  User ID: a1b2c3d4e5f67890
  Name: Alice
  Public Key: AQID...

Current Context: work
  Registered: Yes
  Roles: admin
  S3 Access Key: A1B2C3D4E5F67890ABCD
```

## Mesh Registration

Your identity must be registered with each mesh you want to access.

### Register with Current Context

```bash
# Switch to a context
tunnelmesh context use work

# Register with that mesh
tunnelmesh user register
```

Registration data is stored in `~/.tunnelmesh/contexts/{name}/registration.json`.

### First User = Admin

The first user to register with a mesh automatically becomes an admin.

## RBAC System

TunnelMesh uses Kubernetes-style Role-Based Access Control.

### Built-in Roles

| Role | Description | Permissions |
|------|-------------|-------------|
| `admin` | Full access | All operations on all resources |
| `bucket-admin` | Bucket management | Create/delete buckets, full object access |
| `bucket-write` | Write access | Read buckets, full object CRUD |
| `bucket-read` | Read-only access | Read buckets and objects |
| `system` | Coordinator internal | Access to `_tunnelmesh/` bucket only |

### Role Permissions

```
admin:
  - *:*                           # All verbs on all resources

bucket-admin:
  - create,delete,get,list:buckets
  - get,put,delete,list:objects

bucket-write:
  - get,list:buckets
  - get,put,delete,list:objects

bucket-read:
  - get,list:buckets
  - get,list:objects

system:
  - *:* (on _tunnelmesh/ only)
```

### View Roles

```bash
tunnelmesh role list
```

### Create Custom Role

```bash
tunnelmesh role create my-role --verbs get,list --resources buckets,objects
```

### Bind Role to User

```bash
# Global binding (all buckets)
tunnelmesh role bind alice admin

# Scoped binding (specific bucket)
tunnelmesh role bind bob bucket-write --bucket my-bucket
```

### View Bindings

```bash
tunnelmesh role bindings
tunnelmesh role bindings --user alice
```

## Service Users

The coordinator uses service users for internal authentication:

- **ID Format**: `svc:{service-name}` (e.g., `svc:coordinator`)
- **Keypair**: Derived from CA private key via HKDF
- **Role**: `system` role for `_tunnelmesh/` access

Service users authenticate to S3 like regular users but have their keypair derived deterministically from the mesh CA.

## Identity Architecture

### Global vs Per-Mesh

```
~/.tunnelmesh/
  user.json                    # Global identity (portable)
  contexts/
    work/
      config.yaml              # Peer config
      registration.json        # Mesh-specific registration
    home/
      config.yaml
      registration.json
```

- **Global Identity** (`user.json`): Your ED25519 keypair, derived from mnemonic
- **Per-Mesh Registration**: Certificate, roles, and S3 credentials for each mesh

### Key Derivation

```
Mnemonic (3 words)
       ↓
    SHA256
       ↓
  ED25519 Seed (32 bytes)
       ↓
  ED25519 Keypair
       ↓
  User ID (first 16 chars of SHA256(pubkey))
```

S3 credentials are derived from the public key:
```
Public Key → HKDF("s3-access-key") → Access Key (20 chars)
Public Key → HKDF("s3-secret-key") → Secret Key (40 chars)
```

## Security Considerations

### Mnemonic Security

The 3-word mnemonic provides approximately 33 bits of entropy (2048^3 combinations). This is sufficient because:

1. Mesh access already requires network authentication
2. The coordinator rate-limits registration attempts
3. The mnemonic is never transmitted over the network

For higher security, you can use a longer BIP39 mnemonic with the `--mnemonic-words` flag:

```bash
tunnelmesh user setup --mnemonic-words 12
```

### Key Storage

- Private keys are stored in `~/.tunnelmesh/user.json` with 0600 permissions
- Never share your mnemonic or private key
- Registration files contain derived credentials, not the master key

### Revoking Access

To revoke a user's access to a mesh:

```bash
# As admin on the mesh
tunnelmesh user revoke alice
```

This removes the user's registration and role bindings.

## CLI Reference

```bash
# Identity management
tunnelmesh user setup [--name NAME]     # Create new identity
tunnelmesh user recover                  # Recover from mnemonic
tunnelmesh user info                     # Show current identity
tunnelmesh user register                 # Register with current mesh

# Role management (admin only)
tunnelmesh role list                     # List all roles
tunnelmesh role create NAME [--verbs V] [--resources R]
tunnelmesh role delete NAME              # Delete custom role

# Binding management (admin only)
tunnelmesh role bind USER ROLE [--bucket B]
tunnelmesh role unbind USER ROLE [--bucket B]
tunnelmesh role bindings [--user U]

# User management (admin only)
tunnelmesh user list                     # List all users on mesh
tunnelmesh user revoke USER              # Revoke user access
```
