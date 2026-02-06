# === PROMETHEUS INSTALLATION ===
echo "Installing Prometheus ${prometheus_version}..."

PROM_VERSION="${prometheus_version}"
ARCH=$(dpkg --print-architecture)
# Map to prometheus naming convention
case $ARCH in
    amd64) PROM_ARCH="linux-amd64" ;;
    arm64) PROM_ARCH="linux-arm64" ;;
    *) PROM_ARCH="linux-$ARCH" ;;
esac

cd /tmp
curl -sLO "https://github.com/prometheus/prometheus/releases/download/v$PROM_VERSION/prometheus-$PROM_VERSION.$PROM_ARCH.tar.gz"
tar xzf "prometheus-$PROM_VERSION.$PROM_ARCH.tar.gz"
cp "prometheus-$PROM_VERSION.$PROM_ARCH/prometheus" /opt/monitoring/prometheus/
cp "prometheus-$PROM_VERSION.$PROM_ARCH/promtool" /opt/monitoring/prometheus/
rm -rf "prometheus-$PROM_VERSION.$PROM_ARCH"*

# Create Prometheus config
cat > /etc/prometheus/prometheus.yml <<'PROMCONFIG'
global:
  scrape_interval: 15s
  evaluation_interval: 15s

rule_files:
  - '/etc/prometheus/alerts.yml'

scrape_configs:
  - job_name: 'prometheus'
    metrics_path: /prometheus/metrics
    static_configs:
      - targets: ['localhost:9090']

  - job_name: 'tunnelmesh-peers'
    scheme: https
    tls_config:
      insecure_skip_verify: true
    file_sd_configs:
      - files:
          - '/etc/prometheus/targets/peers.json'
        refresh_interval: 30s
PROMCONFIG

# Create alerts config
cat > /etc/prometheus/alerts.yml <<'ALERTS'
groups:
  - name: tunnelmesh-warnings
    rules:
      - alert: TunnelMeshElevatedPacketDrops
        expr: sum(rate(tunnelmesh_dropped_no_route_total[5m])) > 1 or sum(rate(tunnelmesh_dropped_no_tunnel_total[5m])) > 1
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "Elevated packet drops detected"
          description: "Packet drop rate exceeds 1/s for 2 minutes"

      - alert: TunnelMeshReconnectionAttempts
        expr: sum(rate(tunnelmesh_reconnects_total[5m])) > 0.1
        for: 3m
        labels:
          severity: warning
        annotations:
          summary: "Frequent reconnection attempts"
          description: "Reconnection rate indicates unstable connections"

      - alert: TunnelMeshPeerDisconnected
        expr: count(tunnelmesh_connection_state == 0) > 0
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "Peer(s) disconnected"
          description: "One or more peers have been disconnected for 2+ minutes"

      - alert: TunnelMeshUnhealthyTunnels
        expr: (sum(tunnelmesh_active_tunnels) - sum(tunnelmesh_healthy_tunnels)) > 2
        for: 3m
        labels:
          severity: warning
        annotations:
          summary: "Unhealthy tunnels detected"
          description: "Multiple tunnels are active but not healthy"

      - alert: TunnelMeshForwarderErrors
        expr: sum(rate(tunnelmesh_forwarder_errors_total[5m])) > 0.1
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "Forwarder errors detected"
          description: "Forwarder error rate is elevated"

  - name: tunnelmesh-critical
    rules:
      - alert: TunnelMeshMultiplePeersDown
        expr: count(tunnelmesh_connection_state == 0) >= 3
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "Multiple peers disconnected"
          description: "3+ peers are currently disconnected - mesh connectivity severely impacted"

      - alert: TunnelMeshHighErrorRate
        expr: sum(rate(tunnelmesh_forwarder_errors_total[1m])) > 1
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "High forwarder error rate"
          description: "Forwarder errors exceeding 1/s - service is degraded"

      - alert: TunnelMeshSignificantPacketLoss
        expr: (sum(rate(tunnelmesh_dropped_no_route_total[1m])) + sum(rate(tunnelmesh_dropped_no_tunnel_total[1m]))) / (sum(rate(tunnelmesh_packets_sent_total[1m])) + 1) > 0.05
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "Significant packet loss detected"
          description: "Packet drop rate exceeds 5% of total traffic"

      - alert: TunnelMeshNoHealthyTunnels
        expr: tunnelmesh_active_tunnels > 0 and tunnelmesh_healthy_tunnels == 0
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "Peer has no healthy tunnels"
          description: "Active tunnels exist but none are healthy - connectivity impaired"

      - alert: TunnelMeshRelayDisconnected
        expr: tunnelmesh_relay_connected == 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "Relay connection lost"
          description: "Peer lost connection to relay server - NAT traversal may fail"

  - name: tunnelmesh-page
    rules:
      - alert: TunnelMeshAllTunnelsDown
        expr: sum(tunnelmesh_active_tunnels) == 0 and count(tunnelmesh_peer_info) > 1
        for: 30s
        labels:
          severity: page
        annotations:
          summary: "All mesh tunnels are down"
          description: "Complete mesh network failure - no active tunnels between any peers"

      - alert: TunnelMeshMajorOutage
        expr: count(tunnelmesh_connection_state == 0) / count(tunnelmesh_connection_state) > 0.5
        for: 1m
        labels:
          severity: page
        annotations:
          summary: "Majority of peers unreachable"
          description: "Over 50% of peers are disconnected - major outage"

      - alert: TunnelMeshRelayCompleteFailure
        expr: count(tunnelmesh_relay_connected == 0) == count(tunnelmesh_relay_connected) and count(tunnelmesh_relay_connected) > 0
        for: 1m
        labels:
          severity: page
        annotations:
          summary: "All peers lost relay connection"
          description: "Complete relay server failure - NAT traversal unavailable for all peers"

      - alert: TunnelMeshNoPeersResponding
        expr: absent(tunnelmesh_peer_info)
        for: 2m
        labels:
          severity: page
        annotations:
          summary: "No TunnelMesh peers responding"
          description: "Cannot reach any TunnelMesh peer metrics - possible complete network outage"

      - alert: TunnelMeshWireGuardDown
        expr: tunnelmesh_wireguard_enabled == 1 and tunnelmesh_wireguard_device_running == 0
        for: 1m
        labels:
          severity: page
        annotations:
          summary: "WireGuard concentrator device down"
          description: "WireGuard is enabled but the device is not running"
ALERTS

# Create systemd service
cat > /etc/systemd/system/prometheus.service <<'PROMSERVICE'
[Unit]
Description=Prometheus Monitoring System
Documentation=https://prometheus.io/docs/introduction/overview/
After=network-online.target tunnelmesh-server.service
Wants=network-online.target
Requires=tunnelmesh-server.service

[Service]
Type=simple
User=prometheus
Group=prometheus
ExecStart=/opt/monitoring/prometheus/prometheus \
    --config.file=/etc/prometheus/prometheus.yml \
    --storage.tsdb.path=/var/lib/prometheus \
    --web.listen-address=127.0.0.1:9090 \
    --web.external-url=/prometheus/ \
    --web.route-prefix=/prometheus/ \
    --storage.tsdb.retention.time=${prometheus_retention_days}d
Restart=on-failure
RestartSec=5

NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/var/lib/prometheus
PrivateTmp=yes

StandardOutput=journal
StandardError=journal
SyslogIdentifier=prometheus

[Install]
WantedBy=multi-user.target
PROMSERVICE

chown -R prometheus:prometheus /opt/monitoring/prometheus /var/lib/prometheus /etc/prometheus
systemctl daemon-reload
systemctl enable prometheus
systemctl start prometheus
echo "Prometheus installed and started"
