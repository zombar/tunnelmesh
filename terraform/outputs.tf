output "app_url" {
  description = "Default App Platform URL"
  value       = digitalocean_app.tunnelmesh_coord.default_ingress
}

output "coord_url" {
  description = "Coordination server URL (custom domain)"
  value       = "https://${var.subdomain}.${var.domain}"
}

output "admin_url" {
  description = "Admin dashboard URL"
  value       = "https://${var.subdomain}.${var.domain}/admin/"
}

output "auth_token" {
  description = "Authentication token for mesh peers"
  value       = var.auth_token
  sensitive   = true
}

output "mesh_cidr" {
  description = "Mesh network CIDR"
  value       = var.mesh_cidr
}

output "peer_config_example" {
  description = "Example peer configuration"
  sensitive   = true
  value       = <<-EOF
    # Add this to your peer.yaml:
    server: "https://${var.subdomain}.${var.domain}"
    auth_token: "${var.auth_token}"
  EOF
}
