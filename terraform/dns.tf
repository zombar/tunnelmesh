# DNS Configuration
#
# DNS records for individual nodes are managed by the tunnelmesh-node module.
# This file can be used for additional domain-wide DNS configuration if needed.
#
# The module automatically creates:
# - A record: {node_name}.{domain} -> droplet IP
#
# Example: Add a CNAME for the coordinator
# resource "digitalocean_record" "coord_alias" {
#   count  = var.coordinator_enabled ? 1 : 0
#   domain = var.domain
#   type   = "CNAME"
#   name   = "mesh"
#   value  = "${var.coordinator_name}.${var.domain}."
#   ttl    = 300
# }
