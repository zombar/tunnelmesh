// Dashboard state - track history per peer
const state = {
    peerHistory: {}, // { peerName: { throughputTx: [], throughputRx: [], packetsTx: [], packetsRx: [] } }
    maxHistoryPoints: 20,
    networkSettings: null, // Current network settings from server
    settingsLoaded: false  // Track if settings have been loaded initially
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

    // Update exit node dropdown with current peers
    updateExitNodeDropdown(data.peers);

    // Update network settings display if included in response
    if (data.network_settings && !state.settingsLoaded) {
        state.networkSettings = data.network_settings;
        if (data.network_settings.exit_node_peer) {
            document.getElementById('exit-node-select').value = data.network_settings.exit_node_peer;
        }
        if (data.network_settings.exceptions && data.network_settings.exceptions.length > 0) {
            document.getElementById('network-exceptions').value = data.network_settings.exceptions.join('\n');
        }
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
            return `
            <tr>
                <td><strong>${escapeHtml(peer.name)}</strong></td>
                <td><code>${peer.mesh_ip}</code></td>
                <td class="ips-cell">${formatAdvertisedIPs(peer)}</td>
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

function formatAdvertisedIPs(peer) {
    const parts = [];
    const port = peer.ssh_port || 2222;

    if (peer.public_ips && peer.public_ips.length > 0) {
        const natBadge = peer.behind_nat ? ' <span class="nat-badge">NAT</span>' : '';
        parts.push(`<span class="ip-label">Public${natBadge}:</span> ${peer.public_ips.map(ip => `<code>${ip}:${port}</code>`).join(', ')}`);
    }
    if (peer.private_ips && peer.private_ips.length > 0) {
        parts.push(`<span class="ip-label">Private:</span> ${peer.private_ips.map(ip => `<code>${ip}:${port}</code>`).join(', ')}`);
    }

    return parts.length > 0 ? parts.join('<br>') : '<span class="no-ips">-</span>';
}

// Update exit node dropdown with online peers
function updateExitNodeDropdown(peers) {
    const select = document.getElementById('exit-node-select');
    const currentValue = select.value;

    // Get list of online peers
    const onlinePeers = peers.filter(p => p.online);

    // Rebuild options
    select.innerHTML = '<option value="">Disabled</option>';
    onlinePeers.forEach(peer => {
        const option = document.createElement('option');
        option.value = peer.name;
        option.textContent = `${peer.name} (${peer.mesh_ip})`;
        select.appendChild(option);
    });

    // Restore selection if still valid
    if (currentValue && onlinePeers.some(p => p.name === currentValue)) {
        select.value = currentValue;
    } else if (state.networkSettings?.exit_node_peer) {
        // Set to saved value if it exists
        select.value = state.networkSettings.exit_node_peer;
    }
}

// Load network settings from server
async function loadNetworkSettings() {
    try {
        const resp = await fetch('/admin/api/network-settings');
        if (!resp.ok) {
            throw new Error(`HTTP ${resp.status}`);
        }
        const settings = await resp.json();
        state.networkSettings = settings;

        // Update UI
        const select = document.getElementById('exit-node-select');
        if (settings.exit_node_peer) {
            select.value = settings.exit_node_peer;
        }

        const textarea = document.getElementById('network-exceptions');
        if (settings.exceptions && settings.exceptions.length > 0) {
            textarea.value = settings.exceptions.join('\n');
        }

        state.settingsLoaded = true;
    } catch (err) {
        console.error('Failed to load network settings:', err);
    }
}

// Save network settings to server
async function saveNetworkSettings() {
    const statusEl = document.getElementById('save-status');
    const exitNodePeer = document.getElementById('exit-node-select').value;
    const exceptionsText = document.getElementById('network-exceptions').value;
    const exceptions = exceptionsText
        .split('\n')
        .map(s => s.trim())
        .filter(s => s.length > 0);

    statusEl.textContent = 'Saving...';
    statusEl.className = 'save-status';

    try {
        const resp = await fetch('/admin/api/network-settings', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                exit_node_peer: exitNodePeer,
                exceptions: exceptions
            })
        });

        if (!resp.ok) {
            const error = await resp.json();
            throw new Error(error.message || `HTTP ${resp.status}`);
        }

        state.networkSettings = { exit_node_peer: exitNodePeer, exceptions };
        statusEl.textContent = 'Saved!';
        statusEl.className = 'save-status success';
        setTimeout(() => { statusEl.textContent = ''; }, 2000);
    } catch (err) {
        console.error('Failed to save network settings:', err);
        statusEl.textContent = 'Error: ' + err.message;
        statusEl.className = 'save-status error';
    }
}

// Initialize
document.addEventListener('DOMContentLoaded', () => {
    fetchData();
    loadNetworkSettings();
    setInterval(fetchData, 5000); // Refresh every 5 seconds

    // Set up save button handler
    document.getElementById('save-settings').addEventListener('click', saveNetworkSettings);
});
