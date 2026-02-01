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
            return `
            <tr>
                <td><strong>${escapeHtml(peer.name)}</strong></td>
                <td><code>${peer.mesh_ip}</code></td>
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

// Initialize
document.addEventListener('DOMContentLoaded', () => {
    fetchData();
    setInterval(fetchData, 5000); // Refresh every 5 seconds
});
