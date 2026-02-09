# NFS File Sharing

TunnelMesh provides an NFS v3 server for mounting file shares as network drives. The NFS server is backed by the S3
storage system and uses the same authentication and authorization.

## Overview

The NFS server:

- Serves file shares created via the admin panel or API
- Authenticates using TLS client certificates (same as other mesh services)
- Respects RBAC permissions from the S3 storage layer
- Runs on the coordinator's mesh IP on port 2049 (standard NFS port)
- Only starts when at least one file share exists

## Prerequisites

NFS is automatically enabled when:

1. S3 storage is enabled (`s3.enabled: true` in server config)
2. At least one file share exists

No additional configuration is required.

## Mounting File Shares

### Linux

```bash
# Install NFS client (if not already installed)
sudo apt install nfs-common  # Debian/Ubuntu
sudo yum install nfs-utils   # RHEL/CentOS

# Create mount point
sudo mkdir -p /mnt/myshare

# Mount the share
sudo mount -t nfs this.tm:/myshare /mnt/myshare

# Or with explicit options
sudo mount -t nfs -o vers=3,tcp this.tm:/myshare /mnt/myshare
```

### macOS

```bash
# Create mount point
sudo mkdir -p /Volumes/myshare

# Mount the share
sudo mount -t nfs this.tm:/myshare /Volumes/myshare
```

Or use Finder: Go → Connect to Server → `nfs://this.tm/myshare`

### Persistent Mounts

**Linux (`/etc/fstab`):**

```
this.tm:/myshare /mnt/myshare nfs vers=3,tcp,_netdev 0 0
```

**macOS (`/etc/fstab`):**

```
this.tm:/myshare /Volumes/myshare nfs vers=3,tcp 0 0
```

## Authentication

The NFS server authenticates clients using TLS client certificates. This happens automatically when you connect from a
mesh peer - your peer's identity certificate is used.

Authentication flow:

1. Client connects with TLS certificate
2. Server extracts the user ID from the certificate's Common Name
3. RBAC is checked against the S3 authorizer
4. If authorized, the share is mounted

## Permissions

File share permissions are managed via RBAC in the Data tab of the admin panel:

| Role | Permissions |
| ------ | ------------- |
| `bucket-admin` | Full read/write access |
| `bucket-write` | Read and write objects |
| `bucket-read` | Read-only access |

### Default Permissions

When a file share is created:

- The creator gets `bucket-admin` on the share
- If "Allow guest user read" is enabled (default), everyone gets `bucket-read`

### Checking Access

The mount will succeed with read-only access if you have `bucket-read`, or read-write access if you have `bucket-write`
or `bucket-admin`.

## File Shares vs Buckets

File shares are a user-friendly abstraction over S3 buckets:

| Feature | File Share | S3 Bucket |
| --------- | ------------ | ----------- |
| Access | NFS mount + S3 API | S3 API only |
| Naming | `myshare` | `fs+myshare` |
| Creation | Admin panel, wizard | API only |
| Defaults | Auto permissions, quotas | Manual setup |
| Lifecycle | Expiry, tombstoning | Manual deletion |

File shares are backed by S3 buckets with a `fs+` prefix. The same data is accessible via both NFS and S3.

## Supported Operations

The NFS server supports standard file operations:

| Operation | Support |
| ----------- | --------- |
| Read files | ✓ |
| Write files | ✓ (if authorized) |
| Create directories | ✓ |
| Delete files/dirs | ✓ |
| List directories | ✓ |
| Rename/move | ✓ |
| File attributes | Basic (size, timestamps) |

### Limitations

- No hard links (S3 doesn't support them)
- No symlinks
- File locking is advisory only
- Large file uploads may be slower than S3 (no multipart)

## Troubleshooting

### Mount fails with "Permission denied"

Check that:

1. You're connecting from a mesh peer with valid identity
2. The file share exists (check admin panel)
3. You have at least `bucket-read` permission on the share

```bash
# Check your current user
tunnelmesh status

# Check available shares
 tunnelmesh buckets list | grep fs+ 
```

### Mount fails with "Connection refused"

The NFS server only runs when file shares exist:

1. Create a file share via the admin panel
2. Wait a moment for the NFS server to start
3. Retry the mount

### Slow performance

For better performance:

- Use S3 API for large file transfers
- Mount with `rsize=65536,wsize=65536` for larger block sizes
- Consider using the S3 Explorer in the admin panel for browsing

### File not visible after S3 upload

NFS caches directory listings. Force a refresh:

```bash
ls -la /mnt/myshare  # Triggers cache refresh
```

Or remount:

```bash
sudo umount /mnt/myshare && sudo mount ...
```

## Security

- All NFS traffic is encrypted via TLS
- Authentication uses the same certificates as other mesh services
- Authorization is enforced per-operation via RBAC
- The NFS server is only accessible from within the mesh

## API Reference

### List Mounts

The NFS server exposes each file share as a mountable path:

- `/sharename` - Mount the entire share
- `/sharename/subdir` - Mount a specific subdirectory

### Port

The NFS server runs on port 2049 (TCP) on the coordinator's mesh IP.

## Related Documentation

- [S3 Storage](S3_STORAGE.md) - S3 API access to the same storage
- [User Identity](USER_IDENTITY.md) - Certificate-based authentication
- [Getting Started](GETTING_STARTED.md) - Initial setup
