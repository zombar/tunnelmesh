#!/bin/bash
set -e

echo "=== TunnelMesh Server Starting (with mesh join) ==="

# Generate SSH keys if they don't exist
KEY_PATH="/root/.tunnelmesh/id_ed25519"
if [ ! -f "$KEY_PATH" ]; then
    echo "Generating SSH keys..."
    tunnelmesh init
fi

# Start the server (which will also join the mesh as a client)
echo "Starting mesh server..."
exec tunnelmesh serve --config /etc/tunnelmesh/server.yaml --log-level debug
