# === LOKI INSTALLATION ===
echo "Installing Loki ${loki_version}..."

LOKI_VERSION="${loki_version}"
ARCH=$(dpkg --print-architecture)
# Map to loki naming convention
case $ARCH in
    amd64) LOKI_ARCH="linux-amd64" ;;
    arm64) LOKI_ARCH="linux-arm64" ;;
    *) LOKI_ARCH="linux-$ARCH" ;;
esac

cd /tmp
curl -sLO "https://github.com/grafana/loki/releases/download/v$LOKI_VERSION/loki-$LOKI_ARCH.zip"
apt-get install -y -q unzip
unzip -o "loki-$LOKI_ARCH.zip"
cp "loki-$LOKI_ARCH" /opt/monitoring/loki/loki
chmod +x /opt/monitoring/loki/loki
rm -f "loki-$LOKI_ARCH.zip" "loki-$LOKI_ARCH"

# Create Loki config
cat > /etc/loki/local-config.yaml <<LOKICONFIG
auth_enabled: false

server:
  http_listen_port: 3100
  grpc_listen_port: 9096
  http_listen_address: 127.0.0.1

common:
  instance_addr: 127.0.0.1
  path_prefix: /var/lib/loki
  storage:
    filesystem:
      chunks_directory: /var/lib/loki/chunks
      rules_directory: /var/lib/loki/rules
  replication_factor: 1
  ring:
    kvstore:
      store: inmemory

query_range:
  results_cache:
    cache:
      embedded_cache:
        enabled: true
        max_size_mb: 100

schema_config:
  configs:
    - from: 2020-10-24
      store: tsdb
      object_store: filesystem
      schema: v13
      index:
        prefix: index_
        period: 24h

compactor:
  working_directory: /var/lib/loki/compactor
  compaction_interval: 10m
  retention_enabled: true
  retention_delete_delay: 2h
  retention_delete_worker_count: 150
  delete_request_store: filesystem

limits_config:
  reject_old_samples: true
  reject_old_samples_max_age: 168h
  ingestion_rate_mb: 10
  ingestion_burst_size_mb: 20
  retention_period: ${loki_retention_days * 24}h
LOKICONFIG

# Create systemd service
cat > /etc/systemd/system/loki.service <<'LOKISERVICE'
[Unit]
Description=Loki Log Aggregation System
Documentation=https://grafana.com/docs/loki/latest/
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=loki
Group=loki
ExecStart=/opt/monitoring/loki/loki -config.file=/etc/loki/local-config.yaml
Restart=on-failure
RestartSec=5

NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/var/lib/loki
PrivateTmp=yes

StandardOutput=journal
StandardError=journal
SyslogIdentifier=loki

[Install]
WantedBy=multi-user.target
LOKISERVICE

mkdir -p /var/lib/loki/{chunks,rules,compactor}
chown -R loki:loki /opt/monitoring/loki /var/lib/loki /etc/loki
systemctl daemon-reload
systemctl enable loki
systemctl start loki
echo "Loki installed and started"
