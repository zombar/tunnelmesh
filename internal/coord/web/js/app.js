// Dashboard state - track history per peer
const state = {
    peerHistory: {}, // { peerName: { throughputTx: [], throughputRx: [], packetsTx: [], packetsRx: [] } }
    maxHistoryPoints: 20
};

// Fetch and update dashboard
async function fetchData() {
    try {
        const resp = await fetch('/admin/api/overview');
        if (!resp.ok) {
            throw new Error(`HTTP ${resp.status}`);
        }
        const data = await resp.json();
        updateDashboard(data);
    } catch (err) {
        console.error('Failed to fetch data:', err);
    }
}

function updateDashboard(data) {
    // Update header stats
    document.getElementById('uptime').textContent = data.server_uptime;
    document.getElementById('peer-count').textContent = `${data.online_peers}/${data.total_peers}`;
    document.getElementById('heartbeats').textContent = formatNumber(data.total_heartbeats);

    // Update history for each peer
    data.peers.forEach(peer => {
        if (!state.peerHistory[peer.name]) {
            state.peerHistory[peer.name] = {
                throughputTx: [],
                throughputRx: [],
                packetsTx: [],
                packetsRx: []
            };
        }
        const history = state.peerHistory[peer.name];

        // Add new data points
        history.throughputTx.push(peer.bytes_sent_rate || 0);
        history.throughputRx.push(peer.bytes_received_rate || 0);
        history.packetsTx.push(peer.packets_sent_rate || 0);
        history.packetsRx.push(peer.packets_received_rate || 0);

        // Trim to max history
        if (history.throughputTx.length > state.maxHistoryPoints) {
            history.throughputTx.shift();
            history.throughputRx.shift();
            history.packetsTx.shift();
            history.packetsRx.shift();
        }
    });

    // Clean up history for removed peers
    const currentPeers = new Set(data.peers.map(p => p.name));
    Object.keys(state.peerHistory).forEach(name => {
        if (!currentPeers.has(name)) {
            delete state.peerHistory[name];
        }
    });

    // Update DNS records table
    const dnsTbody = document.getElementById('dns-body');
    const noDns = document.getElementById('no-dns');
    const domainSuffix = data.domain_suffix || '.tunnelmesh';

    if (data.peers.length === 0) {
        dnsTbody.innerHTML = '';
        noDns.style.display = 'block';
    } else {
        noDns.style.display = 'none';
        dnsTbody.innerHTML = data.peers.map(peer => `
            <tr>
                <td><code>${escapeHtml(peer.name)}${domainSuffix}</code></td>
                <td><code>${peer.mesh_ip}</code></td>
            </tr>
        `).join('');
    }

    // Update peers table
    const tbody = document.getElementById('peers-body');
    const noPeers = document.getElementById('no-peers');

    if (data.peers.length === 0) {
        tbody.innerHTML = '';
        noPeers.style.display = 'block';
    } else {
        noPeers.style.display = 'none';
        tbody.innerHTML = data.peers.map(peer => {
            const history = state.peerHistory[peer.name];
            const peerNameEscaped = escapeHtml(peer.name);
            const currentTransport = peer.preferred_transport || 'auto';
            return `
            <tr>
                <td><strong>${peerNameEscaped}</strong></td>
                <td><code>${peer.mesh_ip}</code></td>
                <td class="ips-cell">${formatAdvertisedIPs(peer)}</td>
                <td class="ports-cell">${formatPorts(peer)}</td>
                <td><span class="status-badge ${peer.online ? 'online' : 'offline'}">${peer.online ? 'Online' : 'Offline'}</span></td>
                <td>${peer.stats?.active_tunnels ?? '-'}</td>
                <td class="sparkline-cell">
                    ${createSparklineSVG(history.throughputTx, history.throughputRx)}
                    <div class="rate-values">
                        <span class="tx">${formatBytes(peer.bytes_sent_rate)}/s</span>
                        <span class="rx">${formatBytes(peer.bytes_received_rate)}/s</span>
                    </div>
                </td>
                <td class="sparkline-cell">
                    ${createSparklineSVG(history.packetsTx, history.packetsRx)}
                    <div class="rate-values">
                        <span class="tx">${formatRate(peer.packets_sent_rate)}</span>
                        <span class="rx">${formatRate(peer.packets_received_rate)}</span>
                    </div>
                </td>
                <td>${peer.stats?.errors ?? 0}</td>
                <td>
                    <select class="transport-select" data-peer="${peerNameEscaped}" onchange="setTransport(this)">
                        <option value="auto" ${currentTransport === 'auto' ? 'selected' : ''}>Auto</option>
                        <option value="udp" ${currentTransport === 'udp' ? 'selected' : ''}>UDP</option>
                        <option value="ssh" ${currentTransport === 'ssh' ? 'selected' : ''}>SSH</option>
                        <option value="relay" ${currentTransport === 'relay' ? 'selected' : ''}>Relay</option>
                    </select>
                </td>
                <td class="actions-cell">
                    <button class="restart-btn" onclick="restartConnection('${peerNameEscaped}')" title="Restart connection">&#x21bb;</button>
                </td>
            </tr>
        `}).join('');
    }
}

function createSparklineSVG(dataTx, dataRx) {
    const width = 80;
    const height = 24;
    const padding = 2;

    if (!dataTx.length) {
        return `<svg class="sparkline" viewBox="0 0 ${width} ${height}"></svg>`;
    }

    // Find max value across both datasets for consistent scale
    const allValues = [...dataTx, ...dataRx];
    const maxVal = Math.max(...allValues, 1); // At least 1 to avoid division by zero

    const pathTx = createSparklinePath(dataTx, width, height, padding, maxVal);
    const pathRx = createSparklinePath(dataRx, width, height, padding, maxVal);

    return `<svg class="sparkline" viewBox="0 0 ${width} ${height}">
        <path class="tx" d="${pathTx}"/>
        <path class="rx" d="${pathRx}"/>
    </svg>`;
}

function createSparklinePath(data, width, height, padding, maxVal) {
    if (!data.length) return '';

    const drawWidth = width - padding * 2;
    const drawHeight = height - padding * 2;
    const step = drawWidth / Math.max(data.length - 1, 1);

    const points = data.map((val, i) => {
        const x = padding + i * step;
        const y = padding + drawHeight - (val / maxVal) * drawHeight;
        return `${x.toFixed(1)},${y.toFixed(1)}`;
    });

    return 'M' + points.join(' L');
}

function formatBytes(bytes) {
    if (bytes === 0 || bytes === undefined || bytes === null) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB'];
    const i = Math.floor(Math.log(Math.abs(bytes)) / Math.log(k));
    const clampedI = Math.min(i, sizes.length - 1);
    return parseFloat((bytes / Math.pow(k, clampedI)).toFixed(1)) + ' ' + sizes[clampedI];
}

function formatRate(rate) {
    if (rate === 0 || rate === undefined || rate === null) return '0';
    return rate.toFixed(1);
}

function formatNumber(num) {
    if (num === undefined || num === null) return '0';
    return num.toLocaleString();
}

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

function formatAdvertisedIPs(peer) {
    const parts = [];

    if (peer.public_ips && peer.public_ips.length > 0) {
        const natBadge = peer.behind_nat ? '<span class="nat-badge">NAT</span>' : '';
        parts.push(`<span class="ip-label">Public:</span> ${peer.public_ips.map(ip => `<code>${ip}</code>`).join(', ')}${natBadge}`);
    }
    if (peer.private_ips && peer.private_ips.length > 0) {
        parts.push(`<span class="ip-label">Private:</span> ${peer.private_ips.map(ip => `<code>${ip}</code>`).join(', ')}`);
    }

    return parts.length > 0 ? parts.join('<br>') : '<span class="no-ips">-</span>';
}

function formatPorts(peer) {
    const sshPort = peer.ssh_port || 2222;
    const udpPort = peer.udp_port || 0;
    const parts = [`<span class="port-label">SSH:</span> <code>${sshPort}</code>`];
    if (udpPort > 0) {
        parts.push(`<span class="port-label">UDP:</span> <code>${udpPort}</code>`);
    }
    return parts.join('<br>');
}

// Set transport preference for a peer
async function setTransport(selectElement) {
    const peerName = selectElement.dataset.peer;
    const transport = selectElement.value;

    selectElement.disabled = true;

    try {
        const resp = await fetch(`/admin/api/peers/${encodeURIComponent(peerName)}/transport`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ preferred: transport })
        });

        if (!resp.ok) {
            const error = await resp.text();
            throw new Error(error || `HTTP ${resp.status}`);
        }

        console.log(`Transport set to ${transport} for ${peerName}`);
    } catch (err) {
        console.error('Failed to set transport:', err);
        alert(`Failed to set transport: ${err.message}`);
        // Revert to previous selection on error
        fetchData();
    } finally {
        selectElement.disabled = false;
    }
}

// Restart connection for a peer
async function restartConnection(peerName) {
    if (!confirm(`Restart connection for ${peerName}?`)) {
        return;
    }

    try {
        const resp = await fetch(`/admin/api/peers/${encodeURIComponent(peerName)}/reconnect`, {
            method: 'POST'
        });

        if (!resp.ok) {
            const error = await resp.text();
            throw new Error(error || `HTTP ${resp.status}`);
        }

        const result = await resp.json();
        console.log(`Reconnect initiated for ${peerName}:`, result.message);
    } catch (err) {
        console.error('Failed to restart connection:', err);
        alert(`Failed to restart connection: ${err.message}`);
    }
}

// Initialize
document.addEventListener('DOMContentLoaded', () => {
    fetchData();
    setInterval(fetchData, 5000); // Refresh every 5 seconds
});
