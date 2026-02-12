# S3-Compatible Storage

> [!IMPORTANT]
> TunnelMesh includes an S3-compatible object storage service that runs on the coordinator. This provides
> **mesh-only accessible** storage - the S3 API is not exposed to the public internet for security.

File shares can also be mounted as network drives via NFS - see the [NFS documentation](NFS.md).

## Overview

The S3 storage service:

- Is only accessible from within the mesh network
- Uses the same authentication as other mesh services
- Supports standard S3 API operations
- Stores coordinator internal state (users, roles, stats)

## Configuration

S3 is automatically enabled on all coordinators (hardcoded port: 9000).

Optional S3 configuration in coordinator config:

```yaml
# S3 is always enabled on coordinators (port 9000, mesh IP only)
coordinator:
  s3:
    max_size: "100Gi"                  # Storage quota (required)
    data_dir: /var/lib/tunnelmesh/s3   # Storage directory
```

### Configuration Options

| Option | Default | Description |
| -------- | --------- | ------------- |
| `max_size` | `1Gi` | Storage quota (e.g., `10Gi`, `500Mi`, `1Ti`) |
| `data_dir` | `{data_dir}/s3` | Directory for object storage |
| `object_expiry_days` | `9125` | Days until objects auto-expire (25 years) |
| `share_expiry_days` | `365` | Days until file shares expire (1 year) |
| `tombstone_retention_days` | `90` | Days to keep soft-deleted items before purge |

### Size Format

The `max_size` option accepts Kubernetes-style size notation:

- `Ki`, `Mi`, `Gi`, `Ti` - binary units (1Ki = 1024 bytes)
- `K`, `M`, `G`, `T` - also supported as aliases
- Plain numbers are interpreted as bytes

## API Endpoints

The S3 API is available at `https://this.tm:9000` (or your configured port).

### Supported Operations

| Operation | Method | Path | Description |
| ----------- | -------- | ------ | ------------- |
| ListBuckets | GET | `/` | List all buckets |
| CreateBucket | PUT | `/{bucket}` | Create a bucket |
| DeleteBucket | DELETE | `/{bucket}` | Delete an empty bucket |
| HeadBucket | HEAD | `/{bucket}` | Check if bucket exists |
| ListObjects | GET | `/{bucket}` | List objects in bucket |
| ListObjectsV2 | GET | `/{bucket}?list-type=2` | List objects (V2) |
| PutObject | PUT | `/{bucket}/{key}` | Upload an object |
| GetObject | GET | `/{bucket}/{key}` | Download an object |
| DeleteObject | DELETE | `/{bucket}/{key}` | Delete an object |
| HeadObject | HEAD | `/{bucket}/{key}` | Get object metadata |

### Authentication

> [!NOTE]
> **Multiple authentication methods supported**: TunnelMesh S3 accepts AWS Signature V4 (standard S3),
> Basic Auth (simple), and Bearer Token (access key only). Use whatever your client library supports.

The S3 service supports multiple authentication methods:

1. **AWS Signature V4** - Standard S3 authentication

   ```text
   Authorization: AWS4-HMAC-SHA256 Credential=ACCESS_KEY/...
   ```

2. **Basic Auth** - Simple username/password

   ```text
   Authorization: Basic base64(access_key:secret_key)
   ```

3. **Bearer Token** - Access key only

   ```text
   Authorization: Bearer access_key
   ```

## CLI Commands

### Bucket Operations

```bash
# Create a bucket
tunnelmesh bucket create my-bucket

# List buckets
tunnelmesh bucket list

# Delete a bucket
tunnelmesh bucket delete my-bucket

# Get bucket info
tunnelmesh bucket info my-bucket

```

### Object Operations

```bash
# Upload a file
tunnelmesh object put my-bucket/path/to/file.txt local-file.txt

# Download a file
tunnelmesh object get my-bucket/path/to/file.txt output.txt

# List objects
tunnelmesh object list my-bucket
tunnelmesh object list my-bucket --prefix docs/

# Delete an object
tunnelmesh object delete my-bucket/path/to/file.txt

```

## System Bucket

> [!WARNING]
> **Reserved bucket**: The `_tunnelmesh` bucket stores critical coordinator state. Do not delete or
> modify it manually. Only service users with the `system` role can access it.

The coordinator uses a reserved `_tunnelmesh` bucket for internal state:

```text
_tunnelmesh/
  auth/
    users.json      # Registered users
    roles.json      # Custom roles
    bindings.json   # Role bindings
  stats/
    history.json    # Stats history
  wireguard/
    clients.json    # WireGuard clients

```

This bucket is only accessible to service users with the `system` role.

## File Shares

File shares are user-accessible storage areas backed by S3 buckets. They provide an easy way to share files across the
mesh with automatic permission management.

### Creating File Shares

File shares can be created via the admin panel's Data tab or the API:

```bash
# Via API
curl -X POST https://this.tm/api/shares \

  -H "Content-Type: application/json" \
  -d '{"name": "team-files", "description": "Shared team files", "quota_bytes": 10737418240}'

```

### Share Properties

| Property | Description |
| ---------- | ------------- |
| Name | DNS-safe identifier (alphanumeric + hyphens, max 63 chars) |
| Description | Optional description |
| Quota | Per-share storage limit (0 = unlimited, max 1TB) |
| Owner | Creator gets bucket-admin role automatically |
| Expires | Configurable expiry date (or use default from `share_expiry_days`) |
| Guest Read | Allow guest user read access (default: true) |

### Default Permissions

When a file share is created:

- The creator becomes the owner with `bucket-admin` role
- If "Guest Read" is enabled (default): all mesh users (`everyone` group) get `bucket-read` access
- If "Guest Read" is disabled: access is controlled entirely via RBAC bindings
- Additional permissions can be granted via role bindings in the Data tab

### Accessing Share Contents

File shares are backed by S3 buckets with a `fs+` prefix. Access them via:

**S3 API:**

```bash
aws s3 ls s3://fs+team-files/ --endpoint-url https://this.tm:9000

```

**NFS Mount:**

```bash
sudo mount -t nfs this.tm:/team-files /mnt/team-files

```

**S3 Explorer:** Browse in the admin panel's Data tab.

See the [NFS documentation](NFS.md) for detailed mount instructions.

### Soft Delete (Tombstoning)

Deleted file shares are tombstoned rather than immediately removed:

- Share data is retained for `tombstone_retention_days` (default: 90)
- Recreating a share with the same name restores the previous content
- After the retention period, data is permanently purged

## Using with AWS CLI

Configure the AWS CLI to use your mesh S3:

```bash
# Configure credentials
aws configure --profile tunnelmesh
# Enter your access key and secret key

# Set endpoint
export AWS_ENDPOINT_URL=https://this.tm:9000

# List buckets
aws s3 ls --profile tunnelmesh --endpoint-url $AWS_ENDPOINT_URL

# Upload a file
aws s3 cp file.txt s3://my-bucket/file.txt --profile tunnelmesh --endpoint-url $AWS_ENDPOINT_URL

```

## Using with SDKs

### Go

```go
import (
    "github.com/aws/aws-sdk-go-v2/aws"
    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/s3"
)

cfg, _ := config.LoadDefaultConfig(context.TODO(),
    config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
        accessKey, secretKey, "",
    )),
)

client := s3.NewFromConfig(cfg, func(o *s3.Options) {
    o.BaseEndpoint = aws.String("https://this.tm:9000")
    o.UsePathStyle = true
})

```

### Python (boto3)

```python
import boto3

s3 = boto3.client('s3',
    endpoint_url='https://this.tm:9000',
    aws_access_key_id='ACCESS_KEY',
    aws_secret_access_key='SECRET_KEY',
)

# List buckets
response = s3.list_buckets()

```

## Storage Quotas

When `max_size` is configured, the service enforces storage limits:

- Object uploads are rejected if quota would be exceeded
- Quota is tracked per-bucket and total
- Stats available via `tunnelmesh bucket info`

Check quota usage:

```bash
tunnelmesh storage status
# Output:
# Total: 45.2 GB / 100 GB (45.2%)
# Buckets:
#   my-bucket:     30.1 GB
#   backups:       15.1 GB

```

## Data Persistence

All S3 data is stored in the configured `data_dir`:

```text
{data_dir}/
  buckets/
    {bucket}/
      _meta.json           # Bucket metadata
      objects/
        {key}              # Object data
      meta/
        {key}.json         # Object metadata

```

### Backup

> [!CAUTION]
> **Backup the data directory**: S3 data is stored on disk, not in a database. Regular backups of the
> `data_dir` are essential. Stop the coordinator before backup to ensure consistency.

To backup S3 data, copy the entire `data_dir`:

```bash
# Stop coordinator first
sudo systemctl stop tunnelmesh

# Backup data
tar -czf s3-backup-$(date +%Y%m%d).tar.gz /var/lib/tunnelmesh/s3

# Restart
sudo systemctl start tunnelmesh
```

### Restore

```bash
tar -xzf s3-backup.tar.gz -C /
systemctl restart tunnelmesh-server

```
