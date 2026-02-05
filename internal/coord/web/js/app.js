// Dashboard state - track history per peer
const state = {
    peerHistory: {}, // { peerName: { throughputTx: [], throughputRx: [], packetsTx: [], packetsRx: [] } }
    maxHistoryPoints: 20,
    wgClients: [],
    wgEnabled: false,
    currentWGConfig: null,
    // Chart state
    charts: {
        throughput: null,
        packets: null,
        chartData: {
            labels: [],      // timestamps (shared across both charts)
            throughput: {},  // { peerName: [values] }
            packets: {}      // { peerName: [values] }
        },
        maxChartPoints: 4320  // 12 hours at 10-second intervals
    }
};

// Green gradient for chart lines (dim to bright based on outlier status)
// Dimmest green for average peers, brightest for outliers
const GREEN_GRADIENT = [
    '#2d4a37', // dimmest - most average
    '#3d6b4a',
    '#3fb950', // middle
    '#56d364',
    '#7ee787'  // brightest - outliers
];

// Max time range for charts (12 hours)
const MAX_RANGE_HOURS = 12;
// At 10-second heartbeat intervals, 12 hours = 4320 points
// Request full resolution (no downsampling)
const MAX_CHART_POINTS = 4320;

// Fetch and update dashboard
async function fetchData(includeHistory = false) {
    try {
        const url = includeHistory ? '/admin/api/overview?history=20' : '/admin/api/overview';
        const resp = await fetch(url);
        if (resp.status === 401) {
            // Browser will show Basic Auth prompt on page load
            // If we get 401 during polling, credentials were rejected
            showAuthError();
            return;
        }
        if (!resp.ok) {
            throw new Error(`HTTP ${resp.status}`);
        }
        hideAuthError();
        const data = await resp.json();
        updateDashboard(data, includeHistory);
    } catch (err) {
        console.error('Failed to fetch data:', err);
    }
}

function showAuthError() {
    let banner = document.getElementById('auth-error-banner');
    if (!banner) {
        banner = document.createElement('div');
        banner.id = 'auth-error-banner';
        banner.style.cssText = 'background:#d32f2f;color:white;padding:12px 20px;text-align:center;position:fixed;top:0;left:0;right:0;z-index:1000;';
        banner.innerHTML = 'Authentication required. <a href="/admin/" style="color:white;text-decoration:underline;">Click here to login</a>';
        document.body.prepend(banner);
    }
}

function hideAuthError() {
    const banner = document.getElementById('auth-error-banner');
    if (banner) {
        banner.remove();
    }
}

function updateDashboard(data, loadHistory = false) {
    // Update header stats
    document.getElementById('uptime').textContent = data.server_uptime;
    document.getElementById('peer-count').textContent = `${data.online_peers}/${data.total_peers}`;

    // Update footer version
    const versionEl = document.getElementById('server-version');
    if (versionEl && data.server_version) {
        versionEl.textContent = data.server_version;
    }

    // Update charts with new data during polling (not on initial history load)
    if (!loadHistory && state.charts.throughput) {
        updateChartsWithNewData(data.peers);
    }

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

        // If loading history from server, populate from server data
        if (loadHistory && peer.history && peer.history.length > 0) {
            // Server returns newest first, reverse to get oldest first
            const serverHistory = [...peer.history].reverse();
            history.throughputTx = serverHistory.map(h => h.txB || 0);
            history.throughputRx = serverHistory.map(h => h.rxB || 0);
            history.packetsTx = serverHistory.map(h => h.txP || 0);
            history.packetsRx = serverHistory.map(h => h.rxP || 0);
        } else {
            // Add new data points from current rates
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
                <td><code>${escapeHtml(peer.version || '-')}</code></td>
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

// Chart functions

function initCharts() {
    const chartOptions = {
        responsive: true,
        maintainAspectRatio: false,
        animation: false,
        interaction: {
            mode: 'index',
            intersect: false
        },
        scales: {
            x: {
                type: 'time',
                time: {
                    displayFormats: {
                        minute: 'HH:mm',
                        hour: 'HH:mm',
                        day: 'd MMM'
                    }
                },
                grid: { display: false },
                ticks: { color: '#8b949e', maxTicksLimit: 5, maxRotation: 0 },
                border: { display: false }
            },
            y: {
                beginAtZero: true,
                grid: { color: '#21262d' },
                ticks: { color: '#8b949e', maxTicksLimit: 4 },
                border: { display: false }
            }
        },
        plugins: {
            legend: { display: false },
            tooltip: { enabled: false }
        }
    };

    // Throughput chart
    const throughputCtx = document.getElementById('throughput-chart');
    if (throughputCtx) {
        state.charts.throughput = new Chart(throughputCtx, {
            type: 'line',
            data: { datasets: [] },
            options: chartOptions
        });
    }

    // Packets chart
    const packetsCtx = document.getElementById('packets-chart');
    if (packetsCtx) {
        state.charts.packets = new Chart(packetsCtx, {
            type: 'line',
            data: { datasets: [] },
            options: chartOptions
        });
    }
}

async function fetchChartHistory() {
    try {
        // Fetch up to 12 hours of history
        const since = new Date(Date.now() - MAX_RANGE_HOURS * 60 * 60 * 1000);

        const url = `/admin/api/overview?since=${since.toISOString()}&maxPoints=${MAX_CHART_POINTS}`;
        const resp = await fetch(url);
        if (!resp.ok) {
            throw new Error(`HTTP ${resp.status}`);
        }

        const data = await resp.json();
        initializeChartData(data);
    } catch (err) {
        console.error('Failed to fetch chart history:', err);
    }
}

// Quantize timestamp to 10-second intervals
function quantizeTimestamp(ts) {
    return Math.round(ts / 10000) * 10000;
}

function initializeChartData(data) {
    // Clear existing chart data and lastSeenTimes
    state.charts.chartData.labels = [];
    state.charts.chartData.throughput = {};
    state.charts.chartData.packets = {};
    state.charts.lastSeenTimes = {};

    // Build chart data from peer histories
    // Quantize timestamps to 10-second intervals so they align across peers
    const allTimestamps = new Set();
    const peerHistories = {};
    const peerFirstTs = {}; // Track when each peer first came online
    const peerLastTs = {};  // Track when each peer was last seen

    data.peers.forEach(peer => {
        if (!peer.history || peer.history.length === 0) return;

        // History comes newest first, reverse to get oldest first
        const history = [...peer.history].reverse();
        peerHistories[peer.name] = {};

        history.forEach(point => {
            const quantized = quantizeTimestamp(new Date(point.ts).getTime());
            allTimestamps.add(quantized);
            // Store by quantized timestamp (last value wins if multiple in same interval)
            peerHistories[peer.name][quantized] = point;

            // Track first/last timestamps for this peer
            if (!peerFirstTs[peer.name] || quantized < peerFirstTs[peer.name]) {
                peerFirstTs[peer.name] = quantized;
            }
            if (!peerLastTs[peer.name] || quantized > peerLastTs[peer.name]) {
                peerLastTs[peer.name] = quantized;
            }
        });
    });

    // Sort timestamps
    const sortedTimestamps = Array.from(allTimestamps).sort((a, b) => a - b);
    state.charts.chartData.labels = sortedTimestamps.map(ts => new Date(ts));

    // Build data arrays for each peer
    Object.entries(peerHistories).forEach(([peerName, historyMap]) => {
        const throughputData = [];
        const packetsData = [];
        const firstTs = peerFirstTs[peerName];
        const lastTs = peerLastTs[peerName];

        sortedTimestamps.forEach(ts => {
            const point = historyMap[ts];
            if (point) {
                // Combine TX and RX for total throughput/packets
                throughputData.push((point.txB || 0) + (point.rxB || 0));
                packetsData.push((point.txP || 0) + (point.rxP || 0));
            } else if (ts < firstTs || ts > lastTs) {
                // Before peer existed or after last seen - use null (no line)
                throughputData.push(null);
                packetsData.push(null);
            } else {
                // Gap in the middle - peer was offline, use -1 (red line at 0)
                throughputData.push(-1);
                packetsData.push(-1);
            }
        });

        state.charts.chartData.throughput[peerName] = throughputData;
        state.charts.chartData.packets[peerName] = packetsData;

        // Initialize lastSeenTimes from the peer's last timestamp (quantized)
        if (lastTs) {
            state.charts.lastSeenTimes[peerName] = lastTs;
        }
    });

    rebuildChartDatasets();

    // Zoom out to show all available data
    fitChartsToData();
}

function fitChartsToData() {
    const labels = state.charts.chartData.labels;
    if (labels.length === 0) return;

    // Get the time range of the data
    const minTime = labels[0];
    const maxTime = labels[labels.length - 1];

    // Update both charts to show full data range
    if (state.charts.throughput) {
        state.charts.throughput.options.scales.x.min = minTime;
        state.charts.throughput.options.scales.x.max = maxTime;
        state.charts.throughput.update();
    }

    if (state.charts.packets) {
        state.charts.packets.options.scales.x.min = minTime;
        state.charts.packets.options.scales.x.max = maxTime;
        state.charts.packets.update();
    }
}

function updateChartsWithNewData(peers) {
    if (!state.charts.throughput || !state.charts.packets) return;

    // Track last seen times to detect new heartbeats
    if (!state.charts.lastSeenTimes) {
        state.charts.lastSeenTimes = {};
    }

    // Collect new data points with their timestamps
    const newPoints = [];

    peers.forEach(peer => {
        const peerLastSeen = new Date(peer.last_seen);
        const peerLastSeenQuantized = quantizeTimestamp(peerLastSeen.getTime());
        const knownLastSeen = state.charts.lastSeenTimes[peer.name] || 0;

        // Only process if this is a new heartbeat (quantized)
        if (peerLastSeenQuantized > knownLastSeen) {
            state.charts.lastSeenTimes[peer.name] = peerLastSeenQuantized;

            newPoints.push({
                peer: peer.name,
                timestamp: new Date(peerLastSeenQuantized),
                throughput: (peer.bytes_sent_rate || 0) + (peer.bytes_received_rate || 0),
                packets: (peer.packets_sent_rate || 0) + (peer.packets_received_rate || 0)
            });
        }
    });

    // No new data
    if (newPoints.length === 0) return;

    // Group new points by quantized timestamp (10-second intervals)
    const groups = new Map();
    newPoints.forEach(point => {
        const key = point.timestamp.getTime();
        if (!groups.has(key)) {
            groups.set(key, { timestamp: point.timestamp, peers: {} });
        }
        groups.get(key).peers[point.peer] = point;
    });

    // Sort groups by timestamp and add each as a data point
    const sortedGroups = Array.from(groups.values()).sort((a, b) => a.timestamp - b.timestamp);

    // Get the last timestamp in the chart (if any)
    const lastChartTime = state.charts.chartData.labels.length > 0
        ? state.charts.chartData.labels[state.charts.chartData.labels.length - 1].getTime()
        : 0;

    sortedGroups.forEach(group => {
        // Only add if this timestamp is newer than the last one in the chart
        if (group.timestamp.getTime() <= lastChartTime) {
            return; // Skip data points in the past
        }

        // Add timestamp
        state.charts.chartData.labels.push(group.timestamp);

        // For each existing peer, add their value (or null if no data in this group)
        const allPeers = new Set([
            ...Object.keys(state.charts.chartData.throughput),
            ...Object.keys(group.peers)
        ]);

        allPeers.forEach(peerName => {
            // Check if this is a new peer
            const isNewPeer = !state.charts.chartData.throughput[peerName];

            // Initialize arrays if needed (fill with nulls for timestamps before they existed)
            if (isNewPeer) {
                const chartLen = state.charts.chartData.labels.length - 1; // -1 because we just added the new timestamp
                state.charts.chartData.throughput[peerName] = new Array(chartLen).fill(null);
                state.charts.chartData.packets[peerName] = new Array(chartLen).fill(null);
            }

            if (group.peers[peerName]) {
                // This peer has data for this timestamp
                state.charts.chartData.throughput[peerName].push(group.peers[peerName].throughput);
                state.charts.chartData.packets[peerName].push(group.peers[peerName].packets);
            } else if (isNewPeer) {
                // New peer with no data yet - use null (didn't exist)
                state.charts.chartData.throughput[peerName].push(null);
                state.charts.chartData.packets[peerName].push(null);
            } else {
                // Existing peer with no data - went offline, use -1 (shows as red line at 0)
                state.charts.chartData.throughput[peerName].push(-1);
                state.charts.chartData.packets[peerName].push(-1);
            }
        });
    });

    // Trim to max points (rolling window)
    const maxPoints = state.charts.maxChartPoints;
    while (state.charts.chartData.labels.length > maxPoints) {
        state.charts.chartData.labels.shift();
        Object.keys(state.charts.chartData.throughput).forEach(peerName => {
            if (state.charts.chartData.throughput[peerName].length > maxPoints) {
                state.charts.chartData.throughput[peerName].shift();
            }
            if (state.charts.chartData.packets[peerName].length > maxPoints) {
                state.charts.chartData.packets[peerName].shift();
            }
        });
    }

    // Clean up peers that are no longer present
    const currentPeers = new Set(peers.map(p => p.name));
    Object.keys(state.charts.chartData.throughput).forEach(peerName => {
        if (!currentPeers.has(peerName)) {
            delete state.charts.chartData.throughput[peerName];
            delete state.charts.chartData.packets[peerName];
            delete state.charts.lastSeenTimes[peerName];
        }
    });

    rebuildChartDatasets();
}

// Calculate color for each peer based on how much they deviate from average
// Peers close to average get dim green, outliers get bright green
function calculatePeerColors(dataMap) {
    const peerAverages = {};
    const allAverages = [];

    // Calculate average for each peer
    Object.entries(dataMap).forEach(([peerName, values]) => {
        const validValues = values.filter(v => v !== null && v > 0);
        if (validValues.length > 0) {
            const avg = validValues.reduce((a, b) => a + b, 0) / validValues.length;
            peerAverages[peerName] = avg;
            allAverages.push(avg);
        } else {
            peerAverages[peerName] = 0;
        }
    });

    if (allAverages.length === 0) return {};

    // Calculate overall average
    const overallAvg = allAverages.reduce((a, b) => a + b, 0) / allAverages.length;

    // Calculate max deviation from average
    const deviations = allAverages.map(avg => Math.abs(avg - overallAvg));
    const maxDeviation = Math.max(...deviations, 1); // avoid division by zero

    // Assign colors based on deviation (normalized 0-1)
    const colors = {};
    Object.entries(peerAverages).forEach(([peerName, avg]) => {
        const deviation = Math.abs(avg - overallAvg);
        const normalizedDeviation = deviation / maxDeviation; // 0 = average, 1 = max outlier
        const colorIndex = Math.floor(normalizedDeviation * (GREEN_GRADIENT.length - 1));
        colors[peerName] = GREEN_GRADIENT[colorIndex];
    });

    return colors;
}

function rebuildChartDatasets() {
    const labels = state.charts.chartData.labels;

    // Calculate colors based on outlier status
    const throughputColors = calculatePeerColors(state.charts.chartData.throughput);
    const packetsColors = calculatePeerColors(state.charts.chartData.packets);

    // Helper to build dataset with offline detection
    // null = peer didn't exist yet (don't show)
    // -1 = peer was offline (show red dashed line at y=0)
    const buildDataset = (peerName, values, baseColor) => {
        const data = values.map((v, i) => ({
            x: labels[i],
            y: v === null ? null : (v === -1 ? 0 : v)
        }));

        // Track which segments are offline (were -1 in original data)
        const offlineSegments = values.map(v => v === -1);

        return {
            label: peerName,
            data: data,
            borderColor: baseColor,
            borderWidth: 1.5,
            pointRadius: 0,
            tension: 0.3,
            cubicInterpolationMode: 'monotone',
            fill: false,
            spanGaps: false, // Don't connect across null gaps
            segment: {
                borderColor: ctx => {
                    const p0Offline = offlineSegments[ctx.p0DataIndex];
                    const p1Offline = offlineSegments[ctx.p1DataIndex];
                    if (p0Offline || p1Offline) {
                        return '#d32f2f'; // Red for offline
                    }
                    return baseColor;
                },
                borderDash: ctx => {
                    const p0Offline = offlineSegments[ctx.p0DataIndex];
                    const p1Offline = offlineSegments[ctx.p1DataIndex];
                    if (p0Offline || p1Offline) {
                        return [5, 5];
                    }
                    return undefined;
                }
            }
        };
    };

    // Build throughput datasets
    const throughputDatasets = Object.entries(state.charts.chartData.throughput).map(([peerName, values]) => {
        return buildDataset(peerName, values, throughputColors[peerName] || GREEN_GRADIENT[2]);
    });

    // Build packets datasets
    const packetsDatasets = Object.entries(state.charts.chartData.packets).map(([peerName, values]) => {
        return buildDataset(peerName, values, packetsColors[peerName] || GREEN_GRADIENT[2]);
    });

    if (state.charts.throughput) {
        state.charts.throughput.data.datasets = throughputDatasets;
        state.charts.throughput.update();
    }

    if (state.charts.packets) {
        state.charts.packets.data.datasets = packetsDatasets;
        state.charts.packets.update();
    }
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
    // Show IPv6 external address if available
    if (peer.udp_external_addr6) {
        // Extract just the IP from [ip]:port format
        const ipv6Match = peer.udp_external_addr6.match(/^\[([^\]]+)\]/);
        const ipv6 = ipv6Match ? ipv6Match[1] : peer.udp_external_addr6;
        parts.push(`<span class="ip-label">IPv6:</span> <code>${ipv6}</code>`);
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

// WireGuard Client Management

async function checkWireGuardStatus() {
    try {
        const resp = await fetch('/admin/api/wireguard/clients');
        document.getElementById('wireguard-section').style.display = 'block';

        if (resp.ok) {
            state.wgEnabled = true;
            state.wgConcentratorConnected = true;
            const data = await resp.json();
            state.wgClients = data.clients || [];
            updateWGClientsTable();
        } else if (resp.status === 503) {
            // WireGuard enabled but no concentrator connected
            state.wgEnabled = true;
            state.wgConcentratorConnected = false;
            state.wgClients = [];
            updateWGClientsTable();
        } else {
            // WireGuard not enabled (404 or other error)
            document.getElementById('wireguard-section').style.display = 'none';
            state.wgEnabled = false;
        }
    } catch (err) {
        // WireGuard not enabled or network error
        state.wgEnabled = false;
    }
}

async function fetchWGClients() {
    if (!state.wgEnabled) return;

    try {
        const resp = await fetch('/admin/api/wireguard/clients');
        if (resp.ok) {
            state.wgConcentratorConnected = true;
            const data = await resp.json();
            state.wgClients = data.clients || [];
            updateWGClientsTable();
        } else if (resp.status === 503) {
            state.wgConcentratorConnected = false;
            state.wgClients = [];
            updateWGClientsTable();
        }
    } catch (err) {
        console.error('Failed to fetch WG clients:', err);
    }
}

function updateWGClientsTable() {
    const tbody = document.getElementById('wg-clients-body');
    const noClients = document.getElementById('no-wg-clients');
    const addBtn = document.getElementById('add-wg-client-btn');

    // Check if concentrator is connected
    if (!state.wgConcentratorConnected) {
        tbody.innerHTML = '';
        noClients.textContent = 'No WireGuard concentrator connected. Start a mesh peer with --wireguard flag.';
        noClients.style.display = 'block';
        if (addBtn) addBtn.disabled = true;
        return;
    }

    if (addBtn) addBtn.disabled = false;

    if (state.wgClients.length === 0) {
        tbody.innerHTML = '';
        noClients.textContent = 'No WireGuard peers yet. Add a peer to generate a QR code.';
        noClients.style.display = 'block';
        return;
    }

    noClients.style.display = 'none';
    tbody.innerHTML = state.wgClients.map(client => {
        const statusClass = client.enabled ? 'online' : 'offline';
        const statusText = client.enabled ? 'Enabled' : 'Disabled';
        const lastSeen = client.last_seen ? formatLastSeen(client.last_seen) : 'Never';

        return `
            <tr>
                <td><strong>${escapeHtml(client.name)}</strong></td>
                <td><code>${client.mesh_ip}</code></td>
                <td><code>${escapeHtml(client.dns_name)}.tunnelmesh</code></td>
                <td><span class="status-badge ${statusClass}">${statusText}</span></td>
                <td>${lastSeen}</td>
                <td class="actions-cell">
                    <button class="btn-icon" onclick="toggleWGClient('${client.id}', ${!client.enabled})" title="${client.enabled ? 'Disable' : 'Enable'}">
                        ${client.enabled ? '⏸' : '▶'}
                    </button>
                    <button class="btn-danger" onclick="deleteWGClient('${client.id}', '${escapeHtml(client.name)}')">Delete</button>
                </td>
            </tr>
        `;
    }).join('');
}

function formatLastSeen(timestamp) {
    const date = new Date(timestamp);
    const now = new Date();
    const diffMs = now - date;
    const diffSec = Math.floor(diffMs / 1000);

    if (diffSec < 60) return 'Just now';
    if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`;
    if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`;
    return date.toLocaleDateString();
}

function showAddWGClientModal() {
    if (!state.wgConcentratorConnected) {
        return; // Don't open modal if no concentrator
    }
    document.getElementById('wg-modal-title').textContent = 'Add WireGuard Peer';
    document.getElementById('wg-add-form').style.display = 'block';
    document.getElementById('wg-config-display').style.display = 'none';
    document.getElementById('wg-client-name').value = '';
    document.getElementById('wg-modal').style.display = 'flex';
}

function closeWGModal() {
    document.getElementById('wg-modal').style.display = 'none';
    state.currentWGConfig = null;
    // Refresh the client list
    fetchWGClients();
}

async function createWGClient() {
    const nameInput = document.getElementById('wg-client-name');
    const name = nameInput.value.trim();

    if (!name) {
        alert('Please enter a client name');
        return;
    }

    try {
        const resp = await fetch('/admin/api/wireguard/clients', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name })
        });

        if (!resp.ok) {
            const err = await resp.json();
            alert('Failed to create client: ' + (err.message || 'Unknown error'));
            return;
        }

        const data = await resp.json();
        state.currentWGConfig = data;

        // Show the config
        document.getElementById('wg-modal-title').textContent = 'Client Created';
        document.getElementById('wg-add-form').style.display = 'none';
        document.getElementById('wg-config-display').style.display = 'block';

        document.getElementById('wg-qr-image').src = data.qr_code || '';
        document.getElementById('wg-created-name').textContent = data.client.name;
        document.getElementById('wg-created-ip').textContent = data.client.mesh_ip;
        document.getElementById('wg-created-dns').textContent = data.client.dns_name + '.tunnelmesh';
        document.getElementById('wg-config-text').value = data.config;

    } catch (err) {
        console.error('Failed to create WG client:', err);
        alert('Failed to create client');
    }
}

function downloadWGConfig() {
    if (!state.currentWGConfig) return;

    const config = state.currentWGConfig.config;
    const name = state.currentWGConfig.client.dns_name || 'wireguard';
    const blob = new Blob([config], { type: 'text/plain' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${name}.conf`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
}

async function toggleWGClient(id, enabled) {
    try {
        const resp = await fetch(`/admin/api/wireguard/clients/${id}`, {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ enabled })
        });

        if (!resp.ok) {
            alert('Failed to update client');
            return;
        }

        fetchWGClients();
    } catch (err) {
        console.error('Failed to toggle WG client:', err);
        alert('Failed to update client');
    }
}

async function deleteWGClient(id, name) {
    if (!confirm(`Delete WireGuard peer "${name}"?`)) {
        return;
    }

    try {
        const resp = await fetch(`/admin/api/wireguard/clients/${id}`, {
            method: 'DELETE'
        });

        if (!resp.ok) {
            alert('Failed to delete client');
            return;
        }

        fetchWGClients();
    } catch (err) {
        console.error('Failed to delete WG client:', err);
        alert('Failed to delete client');
    }
}

// Initialize
document.addEventListener('DOMContentLoaded', () => {
    // Initialize charts first
    initCharts();

    // Fetch initial chart history (up to 3 days)
    fetchChartHistory();

    fetchData(true); // Load with history on initial fetch
    setInterval(() => fetchData(false), 5000); // Refresh every 5 seconds without history

    // Check if WireGuard is enabled and setup handlers
    checkWireGuardStatus();
    setInterval(fetchWGClients, 10000); // Refresh WG clients every 10 seconds

    // Add client button handler
    const addBtn = document.getElementById('add-wg-client-btn');
    if (addBtn) {
        addBtn.addEventListener('click', showAddWGClientModal);
    }

    // Close modal on background click
    const modal = document.getElementById('wg-modal');
    if (modal) {
        modal.addEventListener('click', (e) => {
            if (e.target === modal) {
                closeWGModal();
            }
        });
    }
});
