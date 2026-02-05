# TunnelMesh Node Module Variables
# Supports: coordinator-only, coordinator+peer, peer+wireguard, peer-only

# --- Required Variables ---

variable "name" {
  description = "Node name (used for droplet name, DNS record, and mesh peer name)"
  type        = string
}

variable "domain" {
  description = "Domain name for DNS records"
  type        = string
}

variable "auth_token" {
  description = "Authentication token for the mesh"
  type        = string
  sensitive   = true
}

variable "region" {
  description = "DigitalOcean region"
  type        = string
}

# --- Feature Flags ---

variable "coordinator_enabled" {
  description = "Run the coordination server"
  type        = bool
  default     = false
}

variable "peer_enabled" {
  description = "Run as a mesh peer"
  type        = bool
  default     = false
}

variable "wireguard_enabled" {
  description = "Enable WireGuard concentrator (requires peer_enabled=true)"
  type        = bool
  default     = false
}

# --- Coordinator Settings ---

variable "coordinator_port" {
  description = "HTTP port for the coordination server"
  type        = number
  default     = 8080
}

variable "mesh_cidr" {
  description = "CIDR for mesh IP allocation"
  type        = string
  default     = "10.99.0.0/16"
}

variable "domain_suffix" {
  description = "Domain suffix for mesh DNS (e.g., .tunnelmesh)"
  type        = string
  default     = ".tunnelmesh"
}

variable "relay_enabled" {
  description = "Enable relay server on coordinator"
  type        = bool
  default     = true
}

variable "admin_token" {
  description = "Authentication token for admin web interface"
  type        = string
  sensitive   = true
  default     = ""
}

variable "locations_enabled" {
  description = "Enable node location tracking (uses external IP geolocation API)"
  type        = bool
  default     = false
}

# --- Peer Settings ---

variable "peer_server_url" {
  description = "Coordination server URL (required if peer_enabled=true and coordinator_enabled=false)"
  type        = string
  default     = ""
}

variable "ssh_tunnel_port" {
  description = "SSH tunnel port for mesh connections"
  type        = number
  default     = 2222
}

# --- WireGuard Settings ---

variable "wg_listen_port" {
  description = "WireGuard UDP listen port"
  type        = number
  default     = 51820
}

variable "wg_endpoint" {
  description = "WireGuard public endpoint (defaults to {name}.{domain}:{wg_listen_port})"
  type        = string
  default     = ""
}

# --- Droplet Settings ---

variable "droplet_size" {
  description = "Droplet size slug"
  type        = string
  default     = "s-1vcpu-512mb-10gb" # $4/month
}

variable "droplet_image" {
  description = "Droplet image slug"
  type        = string
  default     = "ubuntu-24-04-x64"
}

variable "ssh_key_ids" {
  description = "List of SSH key IDs for droplet access"
  type        = list(string)
  default     = []
}

variable "tags" {
  description = "Tags to apply to resources"
  type        = list(string)
  default     = ["tunnelmesh"]
}

# --- Binary Settings ---

variable "github_owner" {
  description = "GitHub owner for downloading tunnelmesh binary"
  type        = string
  default     = "zombar"
}

variable "binary_version" {
  description = "TunnelMesh version to install (latest or specific version)"
  type        = string
  default     = "latest"
}

# --- SSL Settings ---

variable "ssl_enabled" {
  description = "Enable SSL via Let's Encrypt (requires coordinator_enabled=true)"
  type        = bool
  default     = true
}

variable "ssl_email" {
  description = "Email for Let's Encrypt certificate notifications"
  type        = string
  default     = ""
}

# --- Auto-Update Settings ---

variable "auto_update_enabled" {
  description = "Enable automatic updates via systemd timer"
  type        = bool
  default     = true
}

variable "auto_update_schedule" {
  description = "Schedule for auto-updates (systemd OnCalendar format: hourly, daily, weekly)"
  type        = string
  default     = "hourly"
}
