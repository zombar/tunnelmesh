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

# Build command - coord-1 bootstraps, others join coord-1
LOG_LEVEL="${LOG_LEVEL:-info}"

if [ "$(hostname)" = "coord-1" ]; then
    # coord-1 is the bootstrap coordinator
    echo "Starting as BOOTSTRAP coordinator..."
    CMD="tunnelmesh join --config /etc/tunnelmesh/coordinator.yaml --log-level $LOG_LEVEL"
else
    # coord-2 and coord-3 join coord-1's mesh
    echo "Waiting for coord-1 to be ready..."
    until curl -sf http://coord-1:8080/health > /dev/null 2>&1; do
        echo "  coord-1 not ready, waiting..."
        sleep 2
    done
    echo "coord-1 is ready! Joining mesh..."
    CMD="tunnelmesh join http://coord-1:8080 --config /etc/tunnelmesh/coordinator.yaml --log-level $LOG_LEVEL"
fi

# Start the coordinator
echo "Starting mesh coordinator..."
echo "Command: $CMD"
exec $CMD
