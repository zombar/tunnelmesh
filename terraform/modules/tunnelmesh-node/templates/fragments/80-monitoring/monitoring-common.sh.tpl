# === MONITORING STACK SETUP ===
echo "Setting up monitoring infrastructure..."

# Create monitoring users
useradd --system --no-create-home --shell /bin/false prometheus || true
useradd --system --no-create-home --shell /bin/false loki || true

# Create directories
mkdir -p /opt/monitoring/{prometheus,loki,sd-generator}
mkdir -p /var/lib/prometheus
mkdir -p /var/lib/loki
mkdir -p /etc/prometheus/targets
mkdir -p /etc/loki
mkdir -p /var/lib/grafana/dashboards

# Set ownership
chown -R prometheus:prometheus /var/lib/prometheus /etc/prometheus
chown -R loki:loki /var/lib/loki /etc/loki
