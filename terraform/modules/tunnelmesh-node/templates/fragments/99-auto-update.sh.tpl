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
