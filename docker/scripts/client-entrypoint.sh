#!/bin/bash
set -e

# Generate unique node name from hostname
NODE_NAME="node-$(hostname)"
export NODE_NAME

echo "=== TunnelMesh Client Starting ==="
echo "Node Name: $NODE_NAME"
echo "Server URL: $SERVER_URL"

# Wait for server to be available
echo "Waiting for coordination server..."
until curl -sf "${SERVER_URL}/health" > /dev/null 2>&1; do
    echo "  Server not ready, waiting..."
    sleep 2
done
echo "Server is ready!"

# Generate SSH keys if they don't exist
KEY_PATH="/root/.tunnelmesh/id_ed25519"
if [ ! -f "$KEY_PATH" ]; then
    echo "Generating SSH keys..."
    tunnelmesh init
fi

# Generate peer config from template
envsubst < /etc/tunnelmesh/peer.yaml.template > /etc/tunnelmesh/peer.yaml

echo "Generated peer config:"
cat /etc/tunnelmesh/peer.yaml

# Start the mesh client in background
echo "Starting mesh daemon..."
tunnelmesh join --config /etc/tunnelmesh/peer.yaml --log-level debug &
MESH_PID=$!

# Wait for TUN device to be created
echo "Waiting for TUN device..."
for i in $(seq 1 30); do
    if ip link show tun-mesh0 > /dev/null 2>&1; then
        echo "TUN device created!"
        ip addr show tun-mesh0
        break
    fi
    sleep 1
done

# Give time for initial peer discovery (Go code handles jitter and fast retries)
echo "Waiting for initial peer discovery..."
sleep 5

# Peer list and ping loop
echo "Starting ping test loop..."
PING_INTERVAL="${PING_INTERVAL:-10}"
DISCOVERY_INTERVAL="${DISCOVERY_INTERVAL:-30}"

counter=0
PEER_IPS=""

while true; do
    # Refresh peer list periodically
    if [ $((counter % DISCOVERY_INTERVAL)) -eq 0 ] || [ -z "$PEER_IPS" ]; then
        echo ""
        echo "--- Refreshing peer list ---"
        PEERS_JSON=$(curl -sf -H "Authorization: Bearer $AUTH_TOKEN" "${SERVER_URL}/api/v1/peers" 2>/dev/null || echo '{"peers":[]}')

        # Extract mesh IPs of other peers (excluding self)
        PEER_IPS=$(echo "$PEERS_JSON" | jq -r ".peers[] | select(.name != \"$NODE_NAME\") | .mesh_ip" 2>/dev/null | grep -v "^$" || true)

        if [ -n "$PEER_IPS" ]; then
            echo "Discovered peers:"
            echo "$PEERS_JSON" | jq -r ".peers[] | select(.name != \"$NODE_NAME\") | \"  \\(.name) -> \\(.mesh_ip)\"" 2>/dev/null || true
        else
            echo "No other peers discovered yet"
        fi
    fi

    # Pick a random peer and ping it
    if [ -n "$PEER_IPS" ]; then
        # Convert to array and pick random
        readarray -t PEER_ARRAY <<< "$PEER_IPS"
        PEER_COUNT=${#PEER_ARRAY[@]}

        if [ $PEER_COUNT -gt 0 ]; then
            RANDOM_INDEX=$((RANDOM % PEER_COUNT))
            TARGET_IP="${PEER_ARRAY[$RANDOM_INDEX]}"
            TARGET_NAME=$(echo "$PEERS_JSON" | jq -r ".peers[] | select(.mesh_ip == \"$TARGET_IP\") | .name" 2>/dev/null || echo "unknown")

            echo "[$(date '+%H:%M:%S')] Pinging $TARGET_NAME ($TARGET_IP) via mesh..."
            if ping -c 1 -W 2 "$TARGET_IP" > /dev/null 2>&1; then
                echo "  SUCCESS: $TARGET_NAME is reachable!"
            else
                echo "  FAILED: Cannot reach $TARGET_NAME"
            fi
        fi
    fi

    counter=$((counter + PING_INTERVAL))
    sleep "$PING_INTERVAL"
done
