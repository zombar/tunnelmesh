# === GRAFANA INSTALLATION ===
echo "Installing Grafana..."

# Add Grafana GPG key and repository
apt-get install -y -q apt-transport-https software-properties-common
mkdir -p /etc/apt/keyrings/
curl -sL https://apt.grafana.com/gpg.key | gpg --dearmor -o /etc/apt/keyrings/grafana.gpg
echo "deb [signed-by=/etc/apt/keyrings/grafana.gpg] https://apt.grafana.com stable main" | tee /etc/apt/sources.list.d/grafana.list
apt-get update
apt-get install -y -q grafana

# Create systemd override for subpath configuration
mkdir -p /etc/systemd/system/grafana-server.service.d
cat > /etc/systemd/system/grafana-server.service.d/tunnelmesh.conf <<'GRAFANAOVERRIDE'
[Unit]
After=prometheus.service loki.service
Wants=prometheus.service loki.service

[Service]
Environment="GF_SERVER_HTTP_PORT=3000"
Environment="GF_SERVER_HTTP_ADDR=127.0.0.1"
Environment="GF_SERVER_ROOT_URL=%(protocol)s://%(domain)s/grafana/"
Environment="GF_SERVER_SERVE_FROM_SUB_PATH=true"
Environment="GF_AUTH_ANONYMOUS_ENABLED=true"
Environment="GF_AUTH_ANONYMOUS_ORG_ROLE=Viewer"
Environment="GF_SECURITY_ADMIN_PASSWORD=admin"
GRAFANAOVERRIDE

# Provision datasources
mkdir -p /etc/grafana/provisioning/datasources
cat > /etc/grafana/provisioning/datasources/tunnelmesh.yml <<'DATASOURCES'
apiVersion: 1

datasources:
  - name: Prometheus
    type: prometheus
    access: proxy
    url: http://localhost:9090/prometheus
    isDefault: true
    editable: false

  - name: Loki
    type: loki
    access: proxy
    url: http://localhost:3100
    isDefault: false
    editable: false
    jsonData:
      maxLines: 1000
DATASOURCES

# Provision dashboard directory
mkdir -p /etc/grafana/provisioning/dashboards
cat > /etc/grafana/provisioning/dashboards/tunnelmesh.yml <<'DASHBOARDS'
apiVersion: 1

providers:
  - name: 'TunnelMesh'
    orgId: 1
    folder: ''
    folderUid: ''
    type: file
    disableDeletion: false
    updateIntervalSeconds: 30
    allowUiUpdates: true
    options:
      path: /var/lib/grafana/dashboards
DASHBOARDS

mkdir -p /var/lib/grafana/dashboards
chown -R grafana:grafana /var/lib/grafana/dashboards

systemctl daemon-reload
systemctl enable grafana-server
systemctl start grafana-server
echo "Grafana installed and started"
