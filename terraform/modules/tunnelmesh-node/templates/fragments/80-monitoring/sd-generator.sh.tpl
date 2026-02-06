# === SD GENERATOR INSTALLATION ===
echo "Installing TunnelMesh Prometheus SD Generator..."

ARCH=$(dpkg --print-architecture)
%{ if binary_version == "latest" ~}
SD_DOWNLOAD_URL="https://github.com/${github_owner}/tunnelmesh/releases/latest/download/tunnelmesh-prometheus-sd-generator-linux-$ARCH"
%{ else ~}
SD_DOWNLOAD_URL="https://github.com/${github_owner}/tunnelmesh/releases/download/${binary_version}/tunnelmesh-prometheus-sd-generator-linux-$ARCH"
%{ endif ~}

echo "Downloading SD generator from $SD_DOWNLOAD_URL"
curl -sL "$SD_DOWNLOAD_URL" -o /opt/monitoring/sd-generator/tunnelmesh-prometheus-sd-generator
chmod +x /opt/monitoring/sd-generator/tunnelmesh-prometheus-sd-generator

# Create systemd service
cat > /etc/systemd/system/tunnelmesh-prometheus-sd-generator.service <<'SDSERVICE'
[Unit]
Description=TunnelMesh Prometheus Service Discovery Generator
Documentation=https://github.com/${github_owner}/tunnelmesh
After=network-online.target tunnelmesh-server.service
Wants=network-online.target
Requires=tunnelmesh-server.service

[Service]
Type=simple
User=prometheus
Group=prometheus
Environment="COORD_SERVER_URL=http://127.0.0.1:${coordinator_port}"
Environment="AUTH_TOKEN=${auth_token}"
Environment="POLL_INTERVAL=30s"
Environment="OUTPUT_FILE=/etc/prometheus/targets/peers.json"
Environment="METRICS_PORT=9443"
Environment="TLS_SKIP_VERIFY=true"
ExecStart=/opt/monitoring/sd-generator/tunnelmesh-prometheus-sd-generator
Restart=on-failure
RestartSec=10

NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/etc/prometheus/targets
PrivateTmp=yes

StandardOutput=journal
StandardError=journal
SyslogIdentifier=tunnelmesh-prometheus-sd-generator

[Install]
WantedBy=multi-user.target
SDSERVICE

# Create initial empty targets file
echo "[]" > /etc/prometheus/targets/peers.json
chown prometheus:prometheus /etc/prometheus/targets/peers.json

systemctl daemon-reload
systemctl enable tunnelmesh-prometheus-sd-generator
systemctl start tunnelmesh-prometheus-sd-generator
echo "SD Generator installed and started"
