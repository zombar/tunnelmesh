#!/bin/bash
set -e

echo "=== TunnelMesh Node Setup ==="
echo "Coordinator: ${coordinator_enabled}"
echo "Peer: ${peer_enabled}"
echo "WireGuard: ${wireguard_enabled}"
echo "SSL: ${ssl_enabled}"

# Make apt fully non-interactive
export DEBIAN_FRONTEND=noninteractive
export NEEDRESTART_MODE=a

# Update system (keep existing configs on conflicts)
apt-get update
apt-get -o Dpkg::Options::="--force-confold" -o Dpkg::Options::="--force-confdef" upgrade -y

# Install base dependencies
apt-get install -y -q curl wget jq

%{ if coordinator_enabled && ssl_enabled ~}
# Install nginx and certbot for SSL termination
apt-get install -y -q nginx certbot python3-certbot-nginx
%{ endif ~}

# Create tunnelmesh directories
mkdir -p /etc/tunnelmesh
mkdir -p /var/lib/tunnelmesh

%{ if wireguard_enabled && peer_enabled ~}
mkdir -p /var/lib/tunnelmesh/wireguard
%{ endif ~}

# Download tunnelmesh binary
ARCH=$(dpkg --print-architecture)
%{ if binary_version == "latest" ~}
DOWNLOAD_URL="https://github.com/${github_owner}/tunnelmesh/releases/latest/download/tunnelmesh-linux-$ARCH"
%{ else ~}
DOWNLOAD_URL="https://github.com/${github_owner}/tunnelmesh/releases/download/${binary_version}/tunnelmesh-linux-$ARCH"
%{ endif ~}

echo "Downloading tunnelmesh from $DOWNLOAD_URL"
curl -sL "$DOWNLOAD_URL" -o /usr/local/bin/tunnelmesh
chmod +x /usr/local/bin/tunnelmesh

# Verify installation
/usr/local/bin/tunnelmesh version || echo "Warning: version check failed"

%{ if coordinator_enabled ~}
# === COORDINATOR CONFIGURATION ===
cat > /etc/tunnelmesh/server.yaml <<'SERVERCONF'
listen: ":${coordinator_port}"
auth_token: "${auth_token}"
mesh_cidr: "${mesh_cidr}"
domain_suffix: "${domain_suffix}"

admin:
  enabled: true
%{ if peer_enabled ~}
  port: 443  # Mesh-internal admin on port 443 (nginx uses 8443 for external API)
%{ endif ~}
  # Admin accessible at https://this.tunnelmesh/admin/ from mesh peers

relay:
  enabled: ${relay_enabled}
  pair_timeout: "90s"

locations: ${locations_enabled}

%{ if wireguard_enabled ~}
wireguard:
  enabled: true
  endpoint: "${wg_endpoint}"
%{ endif ~}

%{ if peer_enabled ~}
# Embedded peer (join_mesh mode)
join_mesh:
  name: "${node_name}"
  ssh_port: ${ssh_tunnel_port}
  private_key: /etc/tunnelmesh/peer.key
%{ if exit_node != "" ~}
  exit_node: "${exit_node}"
%{ endif ~}
%{ if allow_exit_traffic ~}
  allow_exit_traffic: true
%{ endif ~}
%{ if location_latitude != null && location_longitude != null ~}
  location:
    latitude: ${location_latitude}
    longitude: ${location_longitude}
%{ if location_city != "" ~}
    city: "${location_city}"
%{ endif ~}
%{ if location_country != "" ~}
    country: "${location_country}"
%{ endif ~}
%{ endif ~}
  tun:
    name: tun-mesh
    mtu: 1400
  dns:
    enabled: true
    listen: "127.0.0.1:5353"
%{ if wireguard_enabled ~}
  wireguard:
    enabled: true
    listen_port: ${wg_listen_port}
    data_dir: /var/lib/tunnelmesh/wireguard
    endpoint: "${wg_endpoint}"
%{ endif ~}
%{ endif ~}
SERVERCONF

# Install service using built-in command
/usr/local/bin/tunnelmesh service install --mode serve --config /etc/tunnelmesh/server.yaml

%{ if ssl_enabled ~}
# Configure nginx as SSL reverse proxy
cat > /etc/nginx/sites-available/tunnelmesh <<'NGINX'
server {
    listen 80;
    server_name ${node_name}.${domain};

    location /.well-known/acme-challenge/ {
        root /var/www/html;
    }

    location / {
        return 301 https://$host:${external_api_port}$request_uri;
    }
}

server {
    listen ${external_api_port} ssl http2;
    server_name ${node_name}.${domain};

    # SSL certificates (will be configured by certbot)
    ssl_certificate /etc/letsencrypt/live/${node_name}.${domain}/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/${node_name}.${domain}/privkey.pem;

    # Modern SSL configuration
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384;
    ssl_prefer_server_ciphers off;

    # External coordination API - admin is mesh-internal at https://this.tunnelmesh/admin/
    location / {
        proxy_pass http://127.0.0.1:${coordinator_port};
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 86400;
        proxy_send_timeout 86400;
    }
}
NGINX

# Temporary HTTP-only config for initial certbot run
cat > /etc/nginx/sites-available/tunnelmesh-temp <<'NGINX'
server {
    listen 80;
    server_name ${node_name}.${domain};

    location /.well-known/acme-challenge/ {
        root /var/www/html;
    }

    location / {
        proxy_pass http://127.0.0.1:${coordinator_port};
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
NGINX

# Enable temporary config first
ln -sf /etc/nginx/sites-available/tunnelmesh-temp /etc/nginx/sites-enabled/tunnelmesh
rm -f /etc/nginx/sites-enabled/default

mkdir -p /var/www/html
systemctl enable nginx
systemctl start nginx
%{ endif ~}

%{ else ~}
# === PEER-ONLY CONFIGURATION ===
cat > /etc/tunnelmesh/peer.yaml <<'PEERCONF'
name: "${node_name}"
server: "${peer_server}"
auth_token: "${auth_token}"
ssh_port: ${ssh_tunnel_port}
private_key: /etc/tunnelmesh/peer.key
%{ if exit_node != "" ~}
exit_node: "${exit_node}"
%{ endif ~}
%{ if allow_exit_traffic ~}
allow_exit_traffic: true
%{ endif ~}
%{ if location_latitude != null && location_longitude != null ~}
location:
  latitude: ${location_latitude}
  longitude: ${location_longitude}
%{ if location_city != "" ~}
  city: "${location_city}"
%{ endif ~}
%{ if location_country != "" ~}
  country: "${location_country}"
%{ endif ~}
%{ endif ~}

tun:
  name: tun-mesh
  mtu: 1400

dns:
  enabled: true
  listen: "127.0.0.1:5353"

%{ if wireguard_enabled ~}
wireguard:
  enabled: true
  listen_port: ${wg_listen_port}
  data_dir: /var/lib/tunnelmesh/wireguard
  endpoint: "${wg_endpoint}"
%{ endif ~}
PEERCONF

# Install service using built-in command
/usr/local/bin/tunnelmesh service install --mode join --config /etc/tunnelmesh/peer.yaml
%{ endif ~}

# Configure firewall
ufw allow 22/tcp comment 'SSH'

%{ if peer_enabled || coordinator_enabled ~}
ufw allow ${ssh_tunnel_port}/tcp comment 'TunnelMesh SSH'
ufw allow ${ssh_tunnel_port + 1}/udp comment 'TunnelMesh UDP'
%{ endif ~}

%{ if coordinator_enabled ~}
ufw allow 80/tcp comment 'HTTP'
ufw allow ${external_api_port}/tcp comment 'HTTPS API'
%{ if peer_enabled ~}
ufw allow in on tun-mesh to any port 443 proto tcp comment 'Mesh Admin HTTPS'
%{ endif ~}
%{ endif ~}

%{ if wireguard_enabled && peer_enabled ~}
ufw allow ${wg_listen_port}/udp comment 'WireGuard'
%{ endif ~}

ufw --force enable

%{ if wireguard_enabled || peer_enabled ~}
# Enable IP forwarding
cat >> /etc/sysctl.conf <<'SYSCTL'
net.ipv4.ip_forward = 1
net.ipv6.conf.all.forwarding = 1
SYSCTL
sysctl -p
%{ endif ~}

# Start the tunnelmesh service
%{ if coordinator_enabled ~}
/usr/local/bin/tunnelmesh service start --name tunnelmesh-server
%{ else ~}
/usr/local/bin/tunnelmesh service start --name tunnelmesh
%{ endif ~}

%{ if coordinator_enabled && ssl_enabled ~}
# Wait for DNS propagation and obtain SSL certificate
echo "Waiting for DNS propagation before SSL cert request..."
sleep 60

# Try to get certificate
certbot certonly --webroot -w /var/www/html \
    -d ${node_name}.${domain} \
    --non-interactive --agree-tos \
    --email ${ssl_email} || {
    echo "Certbot failed, will retry on next boot or manually"
    exit 0
}

# Switch to full SSL config
ln -sf /etc/nginx/sites-available/tunnelmesh /etc/nginx/sites-enabled/tunnelmesh
systemctl reload nginx

echo "SSL certificate installed successfully"
%{ endif ~}

%{ if auto_update_enabled ~}
# Set up auto-update timer
cat > /etc/systemd/system/tunnelmesh-update.service <<'SERVICE'
[Unit]
Description=TunnelMesh Auto-Update
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/tunnelmesh update
StandardOutput=journal
StandardError=journal
SERVICE

cat > /etc/systemd/system/tunnelmesh-update.timer <<TIMER
[Unit]
Description=Run TunnelMesh update ${auto_update_schedule}

[Timer]
OnCalendar=${auto_update_schedule}
RandomizedDelaySec=3600
Persistent=true

[Install]
WantedBy=timers.target
TIMER

systemctl daemon-reload
systemctl enable --now tunnelmesh-update.timer
echo "Auto-update timer enabled (${auto_update_schedule})"
%{ endif ~}

echo "=== TunnelMesh Node Setup Complete ==="
