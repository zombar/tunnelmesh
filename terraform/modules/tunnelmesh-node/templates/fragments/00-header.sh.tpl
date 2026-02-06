#!/bin/bash
set -e

echo "=== TunnelMesh Node Setup ==="
echo "Coordinator: ${coordinator_enabled}"
echo "Peer: ${peer_enabled}"
echo "WireGuard: ${wireguard_enabled}"
echo "SSL: ${ssl_enabled}"
%{ if monitoring_enabled ~}
echo "Monitoring: ${monitoring_enabled}"
%{ endif ~}
