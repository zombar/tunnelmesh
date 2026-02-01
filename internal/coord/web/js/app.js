// Dashboard state
const state = {
    peers: [],
    throughputChart: null,
    packetsChart: null
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

    // Update peers table
    const tbody = document.getElementById('peers-body');
    const noPeers = document.getElementById('no-peers');

    if (data.peers.length === 0) {
        tbody.innerHTML = '';
        noPeers.style.display = 'block';
    } else {
        noPeers.style.display = 'none';
        tbody.innerHTML = data.peers.map(peer => `
            <tr>
                <td><strong>${escapeHtml(peer.name)}</strong></td>
                <td><code>${peer.mesh_ip}</code></td>
                <td><span class="status-badge ${peer.online ? 'online' : 'offline'}">${peer.online ? 'Online' : 'Offline'}</span></td>
                <td>${formatTime(peer.last_seen)}</td>
                <td>${peer.stats?.active_tunnels ?? '-'}</td>
                <td>${formatBytes(peer.bytes_sent_rate)}/s</td>
                <td>${formatBytes(peer.bytes_received_rate)}/s</td>
                <td>${formatNumber((peer.stats?.packets_sent ?? 0) + (peer.stats?.packets_received ?? 0))}</td>
                <td>${peer.stats?.errors ?? 0}</td>
            </tr>
        `).join('');
    }

    // Update charts
    updateCharts(data.peers);
}

function formatBytes(bytes) {
    if (bytes === 0 || bytes === undefined || bytes === null) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB'];
    const i = Math.floor(Math.log(Math.abs(bytes)) / Math.log(k));
    const clampedI = Math.min(i, sizes.length - 1);
    return parseFloat((bytes / Math.pow(k, clampedI)).toFixed(1)) + ' ' + sizes[clampedI];
}

function formatNumber(num) {
    if (num === undefined || num === null) return '0';
    return num.toLocaleString();
}

function formatTime(isoString) {
    if (!isoString) return '-';
    const d = new Date(isoString);
    const now = new Date();
    const diff = (now - d) / 1000;
    if (diff < 0) return 'just now';
    if (diff < 60) return `${Math.floor(diff)}s ago`;
    if (diff < 3600) return `${Math.floor(diff/60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff/3600)}h ago`;
    return d.toLocaleDateString();
}

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

function initCharts() {
    const chartOptions = {
        responsive: true,
        maintainAspectRatio: true,
        animation: {
            duration: 400
        },
        plugins: {
            legend: {
                labels: {
                    color: '#888'
                }
            }
        },
        scales: {
            x: {
                ticks: { color: '#888' },
                grid: { color: 'rgba(255,255,255,0.1)' }
            },
            y: {
                beginAtZero: true,
                ticks: { color: '#888' },
                grid: { color: 'rgba(255,255,255,0.1)' }
            }
        }
    };

    const throughputCtx = document.getElementById('throughputChart').getContext('2d');
    state.throughputChart = new Chart(throughputCtx, {
        type: 'line',
        data: {
            labels: [],
            datasets: [
                {
                    label: 'TX Rate',
                    data: [],
                    borderColor: '#3498db',
                    backgroundColor: 'rgba(52, 152, 219, 0.1)',
                    borderWidth: 2,
                    fill: true,
                    tension: 0.3
                },
                {
                    label: 'RX Rate',
                    data: [],
                    borderColor: '#2ecc71',
                    backgroundColor: 'rgba(46, 204, 113, 0.1)',
                    borderWidth: 2,
                    fill: true,
                    tension: 0.3
                }
            ]
        },
        options: {
            ...chartOptions,
            plugins: {
                ...chartOptions.plugins,
                title: {
                    display: true,
                    text: 'Throughput (bytes/sec)',
                    color: '#4fc3f7'
                }
            }
        }
    });

    const packetsCtx = document.getElementById('packetsChart').getContext('2d');
    state.packetsChart = new Chart(packetsCtx, {
        type: 'line',
        data: {
            labels: [],
            datasets: [
                {
                    label: 'Packets Sent',
                    data: [],
                    borderColor: '#3498db',
                    backgroundColor: 'rgba(52, 152, 219, 0.1)',
                    borderWidth: 2,
                    fill: true,
                    tension: 0.3
                },
                {
                    label: 'Packets Received',
                    data: [],
                    borderColor: '#2ecc71',
                    backgroundColor: 'rgba(46, 204, 113, 0.1)',
                    borderWidth: 2,
                    fill: true,
                    tension: 0.3
                }
            ]
        },
        options: {
            ...chartOptions,
            plugins: {
                ...chartOptions.plugins,
                title: {
                    display: true,
                    text: 'Total Packets by Peer',
                    color: '#4fc3f7'
                }
            }
        }
    });
}

function updateCharts(peers) {
    const labels = peers.map(p => p.name);

    // Update throughput chart - modify data in place to animate from previous values
    state.throughputChart.data.labels = labels;
    state.throughputChart.data.datasets[0].data = peers.map(p => p.bytes_sent_rate || 0);
    state.throughputChart.data.datasets[1].data = peers.map(p => p.bytes_received_rate || 0);
    state.throughputChart.update('active');

    // Update packets chart - modify data in place to animate from previous values
    state.packetsChart.data.labels = labels;
    state.packetsChart.data.datasets[0].data = peers.map(p => p.stats?.packets_sent || 0);
    state.packetsChart.data.datasets[1].data = peers.map(p => p.stats?.packets_received || 0);
    state.packetsChart.update('active');
}

// Initialize
document.addEventListener('DOMContentLoaded', () => {
    initCharts();
    fetchData();
    setInterval(fetchData, 5000); // Refresh every 5 seconds
});
