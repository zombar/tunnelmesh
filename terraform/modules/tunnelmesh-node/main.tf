# TunnelMesh Node Module
# Deploys a DigitalOcean droplet configured as coordinator, peer, or both

terraform {
  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.0"
    }
  }
}

locals {
  # Computed values
  wg_endpoint = var.wg_endpoint != "" ? var.wg_endpoint : "${var.name}.${var.domain}:${var.wg_listen_port}"

  # Server URL for peer mode
  # If coordinator is also enabled, peer connects to localhost
  # Otherwise, use the provided peer_server_url
  peer_server = var.coordinator_enabled ? "http://127.0.0.1:${var.coordinator_port}" : var.peer_server_url

  # Determine which services to run
  run_coordinator = var.coordinator_enabled
  run_peer        = var.peer_enabled || (var.coordinator_enabled && var.peer_enabled)
  # WireGuard concentrator only runs on peer nodes (coordinator just needs admin panel)
  run_wireguard_concentrator = var.wireguard_enabled && var.peer_enabled

  # Tags based on enabled features
  feature_tags = concat(
    var.tags,
    var.coordinator_enabled ? ["coordinator"] : [],
    var.peer_enabled ? ["peer"] : [],
    var.wireguard_enabled ? ["wireguard"] : []
  )

  # SSL email defaults to admin@domain if not specified
  ssl_email = var.ssl_email != "" ? var.ssl_email : "admin@${var.domain}"
}

# The droplet
resource "digitalocean_droplet" "node" {
  name     = var.name
  region   = var.region
  size     = var.droplet_size
  image    = var.droplet_image
  ssh_keys = var.ssh_key_ids

  monitoring = true
  tags       = local.feature_tags

  user_data = templatefile("${path.module}/templates/cloud-init.sh.tpl", {
    # Feature flags
    coordinator_enabled = var.coordinator_enabled
    peer_enabled        = var.peer_enabled
    wireguard_enabled   = var.wireguard_enabled
    ssl_enabled         = var.ssl_enabled && var.coordinator_enabled

    # Names and domains
    node_name     = var.name
    domain        = var.domain
    domain_suffix = var.domain_suffix

    # Coordinator settings
    coordinator_port = var.coordinator_port
    mesh_cidr        = var.mesh_cidr
    relay_enabled    = var.relay_enabled
    auth_token       = var.auth_token
    admin_token      = var.admin_token

    # Peer settings
    peer_server     = local.peer_server
    ssh_tunnel_port = var.ssh_tunnel_port

    # WireGuard settings
    wg_listen_port = var.wg_listen_port
    wg_endpoint    = local.wg_endpoint

    # Binary settings
    github_owner   = var.github_owner
    binary_version = var.binary_version

    # SSL settings
    ssl_email = local.ssl_email
  })
}

# Reserved IP for static addressing
resource "digitalocean_reserved_ip" "node" {
  region = var.region
}

# Assign the reserved IP to the droplet
resource "digitalocean_reserved_ip_assignment" "node" {
  ip_address = digitalocean_reserved_ip.node.ip_address
  droplet_id = digitalocean_droplet.node.id
}

# Firewall
resource "digitalocean_firewall" "node" {
  name        = "${var.name}-firewall"
  droplet_ids = [digitalocean_droplet.node.id]

  # SSH access
  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  # TunnelMesh SSH tunnel port (if peer enabled)
  dynamic "inbound_rule" {
    for_each = var.peer_enabled || var.coordinator_enabled ? [1] : []
    content {
      protocol         = "tcp"
      port_range       = tostring(var.ssh_tunnel_port)
      source_addresses = ["0.0.0.0/0", "::/0"]
    }
  }

  # HTTP (for SSL cert issuance and redirect)
  dynamic "inbound_rule" {
    for_each = var.coordinator_enabled ? [1] : []
    content {
      protocol         = "tcp"
      port_range       = "80"
      source_addresses = ["0.0.0.0/0", "::/0"]
    }
  }

  # HTTPS (coordinator API)
  dynamic "inbound_rule" {
    for_each = var.coordinator_enabled ? [1] : []
    content {
      protocol         = "tcp"
      port_range       = "443"
      source_addresses = ["0.0.0.0/0", "::/0"]
    }
  }

  # WireGuard UDP (only for peer nodes running concentrator)
  dynamic "inbound_rule" {
    for_each = local.run_wireguard_concentrator ? [1] : []
    content {
      protocol         = "udp"
      port_range       = tostring(var.wg_listen_port)
      source_addresses = ["0.0.0.0/0", "::/0"]
    }
  }

  # UDP transport port (SSH port + 1)
  dynamic "inbound_rule" {
    for_each = var.peer_enabled || var.coordinator_enabled ? [1] : []
    content {
      protocol         = "udp"
      port_range       = tostring(var.ssh_tunnel_port + 1)
      source_addresses = ["0.0.0.0/0", "::/0"]
    }
  }

  # Allow all outbound
  outbound_rule {
    protocol              = "tcp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol              = "udp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol              = "icmp"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
}

# DNS record (points to reserved IP for stability)
resource "digitalocean_record" "node" {
  domain = var.domain
  type   = "A"
  name   = var.name
  value  = digitalocean_reserved_ip.node.ip_address
  ttl    = 300
}
