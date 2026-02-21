![TunnelMesh][banner]

# TunnelMesh

**Peer-to-peer mesh networking with encrypted tunnels, distributed storage, and zero-trust access control.**

TunnelMesh creates secure, direct connections between nodes in a distributed topology — no
centralized VPN or traffic routing required. Every node is a peer; coordinators are simply peers
with admin services enabled.

## What is TunnelMesh?

TunnelMesh is a single Go binary that turns any machine into a mesh network node. Peers discover
each other through lightweight coordinators and establish direct encrypted tunnels using the
[Noise protocol](https://noiseprotocol.org/) (IKpsk2) with ChaCha20-Poly1305. A TUN interface
provides transparent IP routing so existing applications work without modification.

### Core Capabilities

- **Encrypted P2P tunnels** with automatic transport negotiation (UDP, SSH, WebSocket relay)
- **NAT traversal** via UDP hole-punching, NAT-PMP, PCP, and UPnP
- **Mesh DNS** for resolving peer hostnames (e.g., `mynode.tunnelmesh`)
- **S3-compatible distributed storage** with chunk-level replication and erasure coding
- **NFS file shares** mountable as network drives across the mesh
- **WireGuard concentrator** for mobile device connectivity with QR code provisioning
- **Exit node routing** for split-tunnel VPN through designated peers
- **Port-based packet filter** with per-peer firewall rules
- **Docker integration** with automatic port forwarding for published containers
- **Web dashboard** with network visualizer, geographic map, and S3 object browser
- **Built-in observability** via Prometheus, Grafana, and Loki

### Quick Start

```bash
# Bootstrap a new mesh (first node becomes coordinator)
export TUNNELMESH_TOKEN=$(openssl rand -hex 32)
tunnelmesh join

# Join from another machine
export TUNNELMESH_TOKEN="<same-token>"
tunnelmesh join coord.example.com:8443
```

### Platforms

Linux (amd64, arm64) | macOS (amd64, arm64) | Windows (amd64)

## Repository

| Repository                                                 | Description                                                  |
|------------------------------------------------------------|--------------------------------------------------------------|
| [tunnelmesh](https://github.com/tunnelmesh/tunnelmesh)     | Core mesh networking tool, coordinator, transports, and UI   |

## Documentation

Full documentation is available in the
[tunnelmesh](https://github.com/tunnelmesh/tunnelmesh) repository:

- [Getting Started][docs-start] -- Installation, configuration, and running as a service
- [CLI Reference][docs-cli] -- Complete command-line reference
- [S3 Storage][docs-s3] -- Distributed object storage and file shares
- [WireGuard][docs-wg] -- Mobile device connectivity
- [Docker][docs-docker] -- Container deployment and integration
- [Cloud Deployment][docs-cloud] -- Deploy to DigitalOcean with Terraform

## License

[GNU Affero General Public License v3.0][license]

[banner]: https://raw.githubusercontent.com/tunnelmesh/tunnelmesh/main/docs/images/tunnelmesh_banner.webp
[docs-start]: https://github.com/tunnelmesh/tunnelmesh/blob/main/docs/GETTING_STARTED.md
[docs-cli]: https://github.com/tunnelmesh/tunnelmesh/blob/main/docs/CLI.md
[docs-s3]: https://github.com/tunnelmesh/tunnelmesh/blob/main/docs/S3_STORAGE.md
[docs-wg]: https://github.com/tunnelmesh/tunnelmesh/blob/main/docs/WIREGUARD.md
[docs-docker]: https://github.com/tunnelmesh/tunnelmesh/blob/main/docs/DOCKER.md
[docs-cloud]: https://github.com/tunnelmesh/tunnelmesh/blob/main/docs/CLOUD_DEPLOYMENT.md
[license]: https://github.com/tunnelmesh/tunnelmesh/blob/main/LICENSE
