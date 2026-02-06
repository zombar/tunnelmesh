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
