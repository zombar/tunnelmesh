#!/bin/bash
set -e

# Generate unique coordinator name from hostname
COORD_NAME="coordinator-$(hostname)"
export COORD_NAME

echo "=== TunnelMesh Coordinator Starting ==="
echo "Coordinator Name: $COORD_NAME"
echo "Hostname: $(hostname)"

# Generate SSH keys if they don't exist
KEY_PATH="/root/.tunnelmesh/id_ed25519"
if [ ! -f "$KEY_PATH" ]; then
    echo "Generating SSH keys..."
    tunnelmesh init
fi

# Validate auth token is set
if [ -z "$TUNNELMESH_TOKEN" ]; then
    echo "ERROR: TUNNELMESH_TOKEN environment variable is not set"
    echo "Generate a token with: openssl rand -hex 32"
    exit 1
fi

# Validate token format (must be 64 hex characters)
if ! echo "$TUNNELMESH_TOKEN" | grep -qE '^[0-9a-fA-F]{64}$'; then
    echo "ERROR: TUNNELMESH_TOKEN must be exactly 64 hexadecimal characters"
    echo "Current token length: ${#TUNNELMESH_TOKEN}"
    echo "Generate a valid token with: openssl rand -hex 32"
    exit 1
fi

# Generate config from template
envsubst < /etc/tunnelmesh/coordinator.yaml.template > /etc/tunnelmesh/coordinator.yaml

echo "Generated config:"
cat /etc/tunnelmesh/coordinator.yaml

# Build command - each coordinator bootstraps independently
CMD="tunnelmesh join --config /etc/tunnelmesh/coordinator.yaml"

# Set log level from environment (default: info)
LOG_LEVEL="${LOG_LEVEL:-info}"
CMD="$CMD --log-level $LOG_LEVEL"

# Start the coordinator
echo "Starting mesh coordinator..."
echo "Command: $CMD"
exec $CMD
