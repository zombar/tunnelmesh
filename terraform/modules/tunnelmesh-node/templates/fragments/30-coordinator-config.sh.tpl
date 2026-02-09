%{ if coordinator_enabled ~}
# === COORDINATOR CONFIGURATION ===
cat > /etc/tunnelmesh/server.yaml <<'SERVERCONF'
listen: ":${coordinator_port}"
auth_token: "${auth_token}"
%{ if length(admin_peers) > 0 ~}
admin_peers: [${join(", ", [for p in admin_peers : "\"${p}\""])}]
%{ endif ~}

admin:
  enabled: true
%{ if peer_enabled ~}
  port: 443  # Mesh-internal admin on port 443 (nginx uses 8443 for external API)
%{ endif ~}
%{ if monitoring_enabled ~}
  monitoring:
    prometheus_url: "${prometheus_url}"
    grafana_url: "${grafana_url}"
%{ endif ~}
  # Admin accessible at https://this.tunnelmesh/ from mesh peers

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
  exit_peer: "${exit_node}"
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
%{ if monitoring_enabled && loki_enabled ~}
  loki:
    enabled: true
    url: "http://127.0.0.1:3100"
%{ endif ~}
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
%{ endif ~}
