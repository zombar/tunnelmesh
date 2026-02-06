%{ if coordinator_enabled && ssl_enabled ~}
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

    # External coordination API - admin is mesh-internal at https://this.tunnelmesh/
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
