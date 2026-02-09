%{ if !coordinator_enabled && peer_enabled ~}
# === PEER-ONLY CONFIGURATION ===
cat > /etc/tunnelmesh/peer.yaml <<'PEERCONF'
name: "${node_name}"
server: "${peer_server}"
auth_token: "${auth_token}"
ssh_port: ${ssh_tunnel_port}
private_key: /etc/tunnelmesh/peer.key
%{ if exit_node != "" ~}
exit_peer: "${exit_node}"
%{ endif ~}
%{ if allow_exit_traffic ~}
allow_exit_traffic: true
%{ endif ~}
%{ if location_latitude != null && location_longitude != null ~}
location:
  latitude: ${location_latitude}
  longitude: ${location_longitude}
%{ if location_city != "" ~}
  city: "${location_city}"
%{ endif ~}
%{ if location_country != "" ~}
  country: "${location_country}"
%{ endif ~}
%{ endif ~}

tun:
  name: tun-mesh
  mtu: 1400

dns:
  enabled: true
  listen: "127.0.0.1:5353"

%{ if wireguard_enabled ~}
wireguard:
  enabled: true
  listen_port: ${wg_listen_port}
  data_dir: /var/lib/tunnelmesh/wireguard
  endpoint: "${wg_endpoint}"
%{ endif ~}
PEERCONF

# Install service using built-in command
/usr/local/bin/tunnelmesh service install --mode join --config /etc/tunnelmesh/peer.yaml
%{ endif ~}
