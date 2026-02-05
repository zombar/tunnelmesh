# TunnelMesh Infrastructure
#
# Deploy any number of TunnelMesh nodes with varying configurations.
# Define your nodes in the `nodes` variable - each node can have its own settings.
#
# Example configurations in terraform.tfvars:
#
# nodes = {
#   # All-in-one: coordinator + peer + wireguard
#   "tunnelmesh" = {
#     coordinator = true
#     peer        = true
#     wireguard   = true
#   }
#
#   # Additional WireGuard peer in different region
#   "tm-eu" = {
#     peer      = true
#     wireguard = true
#     region    = "fra1"
#   }
#
#   # Exit node in Asia
#   "tm-asia" = {
#     peer   = true
#     region = "sgp1"
#   }
# }

terraform {
  required_version = ">= 1.0"

  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.0"
    }
  }
}

provider "digitalocean" {
  token = var.do_token
}

# Look up SSH key if specified
data "digitalocean_ssh_key" "main" {
  count = var.ssh_key_name != "" ? 1 : 0
  name  = var.ssh_key_name
}

locals {
  ssh_key_ids = var.ssh_key_name != "" ? [data.digitalocean_ssh_key.main[0].id] : []

  # Find the coordinator node (there should be exactly one if any nodes need it)
  coordinator_name = one([for name, cfg in var.nodes : name if lookup(cfg, "coordinator", false)])
  coordinator_url  = local.coordinator_name != null ? "https://${local.coordinator_name}.${var.domain}" : var.external_coordinator_url
}

# Deploy all nodes using the tunnelmesh-node module
module "node" {
  source   = "./modules/tunnelmesh-node"
  for_each = var.nodes

  name        = each.key
  domain      = var.domain
  auth_token  = var.auth_token
  admin_token = var.admin_token
  region      = lookup(each.value, "region", var.default_region)

  # Feature flags from node config
  coordinator_enabled = lookup(each.value, "coordinator", false)
  peer_enabled        = lookup(each.value, "peer", false)
  wireguard_enabled   = lookup(each.value, "wireguard", false)

  # Coordinator settings
  mesh_cidr         = var.mesh_cidr
  domain_suffix     = var.domain_suffix
  locations_enabled = var.locations_enabled

  # Peer server URL (for non-coordinator nodes)
  # If this node is the coordinator, it connects to localhost
  # Otherwise, connect to the coordinator node or external URL
  peer_server_url = lookup(each.value, "coordinator", false) ? "" : local.coordinator_url

  # WireGuard settings
  wg_listen_port = lookup(each.value, "wg_port", var.default_wg_port)
  wg_endpoint    = lookup(each.value, "wireguard", false) ? "${each.key}.${var.domain}:${lookup(each.value, "wg_port", var.default_wg_port)}" : ""

  # Droplet settings
  droplet_size    = lookup(each.value, "size", var.default_droplet_size)
  ssh_key_ids     = local.ssh_key_ids
  ssh_tunnel_port = lookup(each.value, "ssh_port", var.default_ssh_port)

  # Binary settings
  github_owner   = var.github_owner
  binary_version = var.binary_version

  # SSL (only for coordinator)
  ssl_enabled = lookup(each.value, "coordinator", false)
  ssl_email   = var.ssl_email

  # Auto-update settings
  auto_update_enabled  = var.auto_update_enabled
  auto_update_schedule = var.auto_update_schedule

  tags = concat(
    ["tunnelmesh"],
    lookup(each.value, "coordinator", false) ? ["coordinator"] : [],
    lookup(each.value, "peer", false) ? ["peer"] : [],
    lookup(each.value, "wireguard", false) ? ["wireguard"] : [],
    try(each.value.tags, [])
  )
}
