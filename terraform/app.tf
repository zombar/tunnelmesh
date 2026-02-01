resource "digitalocean_app" "tunnelmesh_coord" {
  spec {
    name   = "tunnelmesh-coord"
    region = var.region

    domain {
      name = "${var.subdomain}.${var.domain}"
      zone = var.domain
    }

    service {
      name               = "coord"
      instance_count     = 1
      instance_size_slug = "basic-xxs" # $5/month - smallest tier for services

      image {
        registry_type = "GHCR"
        registry      = var.github_owner
        repository    = "tunnelmesh"
        tag           = var.image_tag
      }

      http_port = 8080

      # Generate config file from env vars and start server
      run_command = <<-EOF
        sh -c 'cat > /etc/tunnelmesh/server.yaml <<CONF
listen: ":8080"
auth_token: "$${AUTH_TOKEN}"
mesh_cidr: "$${MESH_CIDR}"
domain_suffix: "$${DOMAIN_SUFFIX}"
admin:
  enabled: true
CONF
exec tunnelmesh serve --config /etc/tunnelmesh/server.yaml'
      EOF

      health_check {
        http_path             = "/health"
        initial_delay_seconds = 10
        period_seconds        = 30
        timeout_seconds       = 5
        success_threshold     = 1
        failure_threshold     = 3
      }

      env {
        key   = "AUTH_TOKEN"
        value = var.auth_token
        type  = "SECRET"
      }

      env {
        key   = "MESH_CIDR"
        value = var.mesh_cidr
      }

      env {
        key   = "DOMAIN_SUFFIX"
        value = var.domain_suffix
      }
    }
  }
}
