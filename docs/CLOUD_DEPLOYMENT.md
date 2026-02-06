# Cloud Deployment with Terraform

Deploy TunnelMesh coordination server to DigitalOcean App Platform using Terraform.

## Prerequisites

- [Terraform](https://developer.hashicorp.com/terraform/install) installed
- DigitalOcean account with API token
- Domain managed in DigitalOcean DNS
- GitHub repository with TunnelMesh (for container images)

## Quick Start

```bash
# Clone and configure
cd terraform
cp terraform.tfvars.example terraform.tfvars

# Set your DO token
export TF_VAR_do_token="dop_v1_xxx"

# Edit terraform.tfvars with your domain and auth token

# Deploy
terraform init
terraform apply
```

## Setup Guide

### 1. Push to GitHub

Trigger the Docker image build via GitHub Actions:

```bash
git push origin main
```

This builds and pushes the container image to `ghcr.io`.

### 2. Configure Terraform Variables

```bash
cd terraform
cp terraform.tfvars.example terraform.tfvars
```

Set your DigitalOcean API token as an environment variable:

```bash
export TF_VAR_do_token="dop_v1_your_token_here"
```

Generate a secure authentication token:

```bash
openssl rand -hex 32
```

Edit `terraform.tfvars`:

```hcl
domain     = "example.com"           # Your domain
auth_token = "your-generated-token"  # From openssl command above
```

### 3. Deploy

```bash
terraform init    # Download providers
terraform plan    # Preview changes
terraform apply   # Deploy
```

## Configuration Reference

### Required Variables

| Variable | Description |
|----------|-------------|
| `do_token` | DigitalOcean API token (use `TF_VAR_do_token` env) |
| `domain` | Base domain name |
| `auth_token` | Mesh authentication token |

### Optional Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `subdomain` | Subdomain for coord server | `tunnelmesh` |
| `github_owner` | GitHub owner for container image | `zombar` |
| `image_tag` | Docker image tag | `latest` |
| `mesh_cidr` | Mesh network CIDR | `172.30.0.0/16` |
| `region` | DigitalOcean region | `ams` |

### Feature Flags

| Variable | Description | Default |
|----------|-------------|---------|
| `locations_enabled` | Enable node location tracking | `false` |
| `monitoring_enabled` | Enable Prometheus/Grafana/Loki stack | `false` |
| `auto_update_enabled` | Enable automatic binary updates | `true` |
| `auto_update_schedule` | Update schedule | `hourly` |

### Monitoring Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `prometheus_retention_days` | Prometheus data retention | `3` |
| `loki_retention_days` | Loki log retention | `3` |

## Monitoring Stack

Enable full observability with `monitoring_enabled = true`:

```hcl
monitoring_enabled = true
prometheus_retention_days = 7
loki_retention_days = 7
```

### Included Services

| Service | Purpose | Access |
|---------|---------|--------|
| Prometheus | Metrics collection and alerting | `/prometheus/` |
| Grafana | Dashboards and visualization | `/grafana/` |
| Loki | Log aggregation | Internal only |
| SD Generator | Auto-discovers peers for Prometheus | Internal only |

### Pre-configured Alerts

- Peer disconnections and connectivity issues
- Packet drops and error rates
- WireGuard and relay status
- Resource utilization

### Accessing Grafana

From within the mesh network:

```
https://tunnelmesh.example.com/grafana/
```

Default credentials: `admin` / `admin`

## Node Location Tracking

The `locations_enabled` flag enables geographic visualization of mesh nodes on a map.

### Privacy Considerations

**This feature is disabled by default** because it:

1. Uses external services (ip-api.com) for geolocation
2. Sends public IP addresses to external APIs
3. Requires internet access from the coordinator

### Enabling Locations

```hcl
locations_enabled = true
```

Or via CLI:

```bash
tunnelmesh serve --locations
```

### Manual Coordinates

Nodes can provide manual coordinates (takes precedence over IP geolocation):

```bash
tunnelmesh join --latitude 52.3676 --longitude 4.9041
```

## Outputs

After deployment, Terraform provides:

| Output | Description |
|--------|-------------|
| `app_url` | Default App Platform URL |
| `coord_url` | Custom domain URL |
| `admin_url` | Admin dashboard URL |
| `peer_config_example` | Example peer configuration |

View outputs:

```bash
terraform output
```

## Connecting Peers

Once deployed, configure peers to connect:

### Peer Configuration

```yaml
name: "my-peer"
server: "https://tunnelmesh.example.com"
auth_token: "your-secure-token"
```

### CLI

```bash
tunnelmesh join \
  --server https://tunnelmesh.example.com \
  --token your-secure-token \
  --name my-peer
```

## Managing the Deployment

### Update Configuration

```bash
# Edit terraform.tfvars
terraform apply
```

### View Logs

```bash
# Via DigitalOcean CLI
doctl apps logs <app-id> --follow
```

### Scale Resources

Edit the App Platform spec in Terraform:

```hcl
instance_size_slug = "professional-xs"  # or professional-s, professional-m
instance_count     = 2
```

### Destroy

```bash
terraform destroy
```

## Troubleshooting

### DNS Not Resolving

```bash
# Check DNS propagation
dig tunnelmesh.example.com

# Verify DO DNS zone
doctl compute domain records list example.com
```

### App Not Starting

```bash
# Check app status
doctl apps list

# View deployment logs
doctl apps logs <app-id> --type=deploy
```

### Container Image Issues

Ensure GitHub Actions completed successfully:

1. Check Actions tab in GitHub repository
2. Verify image exists: `ghcr.io/zombar/tunnelmesh:latest`
3. Check image is public or configure registry credentials

### Peers Can't Connect

1. Verify `auth_token` matches between server and peers
2. Check firewall allows HTTPS (443)
3. Verify TLS certificate is valid
4. Check coordinator logs for auth errors

## Cost Optimization

### App Platform Pricing

| Instance Size | vCPU | RAM | Cost/month |
|---------------|------|-----|------------|
| basic-xxs | 1 | 256MB | ~$5 |
| basic-xs | 1 | 512MB | ~$10 |
| professional-xs | 1 | 1GB | ~$12 |

### Reducing Costs

1. Disable monitoring if not needed
2. Use smaller instance sizes for low-traffic deployments
3. Reduce log retention days
4. Use spot instances for non-critical workloads

## Security Best Practices

1. **Rotate auth tokens** periodically
2. **Use strong tokens**: `openssl rand -hex 32`
3. **Restrict admin access** to mesh network only
4. **Enable monitoring** for visibility into access patterns
5. **Review logs** for unauthorized access attempts
6. **Keep updated**: Enable auto-updates or deploy regularly

## Example: Full Production Configuration

```hcl
# terraform.tfvars

# Required
domain     = "example.com"
auth_token = "64-character-hex-token-here"

# Recommended for production
monitoring_enabled        = true
prometheus_retention_days = 14
loki_retention_days       = 7

# Optional features
locations_enabled    = false
auto_update_enabled  = true
auto_update_schedule = "daily"

# Infrastructure
region = "nyc1"
```
