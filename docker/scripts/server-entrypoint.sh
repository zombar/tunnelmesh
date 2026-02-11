#!/bin/bash
set -e

echo "=== TunnelMesh Server Starting (with mesh join) ==="

# Generate SSH keys if they don't exist
KEY_PATH="/root/.tunnelmesh/id_ed25519"
if [ ! -f "$KEY_PATH" ]; then
    echo "Generating SSH keys..."
    tunnelmesh init
fi

# Build command with required token
AUTH_TOKEN="${AUTH_TOKEN:-docker-test-token-123}"
CMD="tunnelmesh join --config /etc/tunnelmesh/server.yaml --token $AUTH_TOKEN"

# Set log level from environment (default: info)
LOG_LEVEL="${LOG_LEVEL:-info}"
CMD="$CMD --log-level $LOG_LEVEL"

# Start the server (which will also join the mesh as a client)
echo "Starting mesh server..."
exec $CMD
