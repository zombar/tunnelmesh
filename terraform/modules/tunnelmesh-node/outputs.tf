# TunnelMesh Node Module Outputs

output "droplet_id" {
  description = "The ID of the droplet"
  value       = digitalocean_droplet.node.id
}

output "ipv4_address" {
  description = "The public IPv4 address of the droplet"
  value       = digitalocean_droplet.node.ipv4_address
}

output "ipv6_address" {
  description = "The public IPv6 address of the droplet"
  value       = digitalocean_droplet.node.ipv6_address
}

output "hostname" {
  description = "The full hostname of the node"
  value       = "${var.name}.${var.domain}"
}

output "ssh_command" {
  description = "SSH command to connect to the droplet"
  value       = "ssh root@${digitalocean_droplet.node.ipv4_address}"
}

# Coordinator-specific outputs
output "coordinator_url" {
  description = "URL of the coordination server (if enabled)"
  value       = var.coordinator_enabled ? (var.ssl_enabled ? "https://${var.name}.${var.domain}" : "http://${var.name}.${var.domain}:${var.coordinator_port}") : null
}

output "coordinator_api_url" {
  description = "API URL for peers to connect to"
  value       = var.coordinator_enabled ? (var.ssl_enabled ? "https://${var.name}.${var.domain}" : "http://${digitalocean_droplet.node.ipv4_address}:${var.coordinator_port}") : null
}

# WireGuard-specific outputs
output "wireguard_endpoint" {
  description = "WireGuard endpoint for clients (if enabled)"
  value       = var.wireguard_enabled ? "${var.name}.${var.domain}:${var.wg_listen_port}" : null
}

# Peer-specific outputs
output "peer_name" {
  description = "The mesh peer name"
  value       = var.peer_enabled || var.coordinator_enabled ? var.name : null
}
