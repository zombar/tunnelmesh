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
