// Constants
const HEARTBEAT_INTERVAL_MS = 10000;    // 10 seconds between heartbeats
const POLL_INTERVAL_MS = 10000;         // Polling fallback interval
const SSE_RETRY_DELAY_MS = 2000;        // Base delay for SSE reconnection
const MAX_SSE_RETRIES = 3;              // Max SSE reconnection attempts
const ROWS_PER_PAGE = 7;                // Default pagination size
const MAX_HISTORY_POINTS = 20;          // Max sparkline history points per peer
const MAX_CHART_POINTS = 360;           // 1 hour at 10-second intervals
const TOAST_DURATION_MS = 4000;         // Toast notification duration
const TOAST_FADE_MS = 300;              // Toast fade-out animation duration
const QUANTIZE_INTERVAL_MS = 10000;     // Timestamp quantization interval

// Toggle collapsible section
function toggleSection(header) {
    header.classList.toggle('collapsed');
    const content = header.nextElementSibling;
    if (content?.classList.contains('collapsible-content')) {
        content.classList.toggle('collapsed');
    }
}
window.toggleSection = toggleSection;

// Cached DOM elements (populated on DOMContentLoaded)
const dom = {};

// Simple event bus for cross-component communication
const events = {
    listeners: {},
    on(event, fn) {
        (this.listeners[event] ||= []).push(fn);
    },
    off(event, fn) {
        if (this.listeners[event]) {
            this.listeners[event] = this.listeners[event].filter(f => f !== fn);
        }
    },
    emit(event, data) {
        this.listeners[event]?.forEach(fn => fn(data));
    }
};

// Dashboard state - track history per peer
const state = {
    peerHistory: {}, // { peerName: { throughputTx: [], throughputRx: [], packetsTx: [], packetsRx: [] } }
    wgClients: [],
    wgEnabled: false,
    currentWGConfig: null,
    eventSource: null, // Store EventSource for cleanup
    selectedNodeId: null, // Centralized selection state
    // Chart state
    charts: {
        throughput: null,
        packets: null,
        chartData: {
            labels: [],      // timestamps (shared across both charts)
            throughput: {},  // { peerName: [values] }
            packets: {}      // { peerName: [values] }
        },
        highlightedPeer: null // Peer to highlight on charts (selected in visualizer)
    },
    // Visualizer state
    visualizer: null,
    // Map state
    nodeMap: null,
    locationsEnabled: false, // Server-side locations feature flag
    domainSuffix: '.tunnelmesh',
    // Pagination state
    peersVisibleCount: ROWS_PER_PAGE,
    dnsVisibleCount: ROWS_PER_PAGE,
    wgVisibleCount: ROWS_PER_PAGE,
    currentPeers: [],  // Store current peers data for pagination
    // Alerts state
    alertsEnabled: false,
    alertHistory: {
        warning: [],
        critical: [],
        page: []
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

// Max time range for charts (1 hour)
const MAX_RANGE_HOURS = 1;

// Initialize DOM cache - call once on DOMContentLoaded
function initDOMCache() {
    // Header elements
    dom.uptime = document.getElementById('uptime');
    dom.peerCount = document.getElementById('peer-count');
    dom.serverVersion = document.getElementById('server-version');

    // Peers table elements
    dom.peersBody = document.getElementById('peers-body');
    dom.noPeers = document.getElementById('no-peers');

    // DNS table elements
    dom.dnsBody = document.getElementById('dns-body');
    dom.noDns = document.getElementById('no-dns');

    // WireGuard elements
    dom.wgClientsBody = document.getElementById('wg-clients-body');
    dom.noWgClients = document.getElementById('no-wg-clients');
    dom.addWgClientBtn = document.getElementById('add-wg-client-btn');
    dom.wgModal = document.getElementById('wg-modal');

    // Chart canvases
    dom.throughputChart = document.getElementById('throughput-chart');
    dom.packetsChart = document.getElementById('packets-chart');

    // Visualizer
    dom.visualizerCanvas = document.getElementById('visualizer-canvas');

    // Toast container
    dom.toastContainer = document.getElementById('toast-container');
}

// Toast notification system
function showToast(message, type = 'error', duration = TOAST_DURATION_MS) {
    if (!dom.toastContainer) return;

    const toast = document.createElement('div');
    toast.className = `toast ${type}`;
    toast.textContent = message;
    dom.toastContainer.appendChild(toast);

    // Auto-dismiss after duration
    setTimeout(() => {
        toast.classList.add('fade-out');
        setTimeout(() => toast.remove(), TOAST_FADE_MS);
    }, duration);
}

// Fetch and update dashboard
async function fetchData(includeHistory = false) {
    try {
        const url = includeHistory ? `api/overview?history=${MAX_HISTORY_POINTS}` : 'api/overview';
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

// Setup Server-Sent Events for real-time updates
let sseRetryCount = 0;

function setupSSE() {
    // Check if EventSource is supported
    if (typeof EventSource === 'undefined') {
        console.log('SSE not supported, falling back to polling');
        startPolling();
        return;
    }

    // Close existing connection if any
    if (state.eventSource) {
        state.eventSource.close();
    }

    // EventSource with credentials for authenticated endpoints
    state.eventSource = new EventSource('api/events', { withCredentials: true });

    state.eventSource.addEventListener('connected', () => {
        console.log('SSE connected - dashboard will update in real-time');
        sseRetryCount = 0; // Reset retry count on successful connection
    });

    state.eventSource.addEventListener('heartbeat', () => {
        // Refresh dashboard when a heartbeat is received
        fetchData(false);
    });

    state.eventSource.onerror = (err) => {
        console.error('SSE error:', err);
        state.eventSource.close();
        state.eventSource = null;

        sseRetryCount++;
        if (sseRetryCount <= MAX_SSE_RETRIES) {
            // Retry SSE connection after a delay
            console.log(`SSE reconnecting (attempt ${sseRetryCount}/${MAX_SSE_RETRIES})...`);
            setTimeout(setupSSE, SSE_RETRY_DELAY_MS * sseRetryCount);
        } else {
            // Fall back to polling after max retries
            console.log('SSE failed, falling back to polling');
            startPolling();
        }
    };
}

function startPolling() {
    // Fallback polling (matches heartbeat interval)
    console.log(`Starting polling mode (every ${POLL_INTERVAL_MS / 1000} seconds)`);
    setInterval(() => fetchData(false), POLL_INTERVAL_MS);
}

// Cleanup on page unload to prevent memory leaks
function cleanup() {
    if (state.eventSource) {
        state.eventSource.close();
        state.eventSource = null;
    }
    if (state.visualizer) {
        state.visualizer.destroy();
    }
    if (state.nodeMap) {
        state.nodeMap.destroy();
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
    // Update header stats using cached DOM elements
    if (dom.uptime) dom.uptime.textContent = data.server_uptime;
    if (dom.peerCount) dom.peerCount.textContent = `${data.online_peers}/${data.total_peers}`;

    // Update footer version
    if (dom.serverVersion && data.server_version) {
        dom.serverVersion.textContent = data.server_version;
    }

    // Store domain suffix for visualizer
    state.domainSuffix = data.domain_suffix || '.tunnelmesh';

    // Track if locations feature is enabled
    state.locationsEnabled = data.locations_enabled || false;

    // Update visualizer with peer data
    if (state.visualizer) {
        state.visualizer.setDomainSuffix(state.domainSuffix);
        // TODO: Detect which peer is the WG concentrator (for now, null)
        state.visualizer.syncNodes(data.peers, null);
        state.visualizer.render();
    }

    // Update map with peer locations (only if locations feature is enabled)
    const mapSection = document.getElementById('map-section');
    if (state.locationsEnabled) {
        if (state.nodeMap) {
            state.nodeMap.updatePeers(data.peers);
        }
    } else if (mapSection) {
        // Hide map section entirely when locations is disabled
        mapSection.style.display = 'none';
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

            // Trim to max history using slice (O(n) instead of O(n) shift per element)
            const maxPoints = state.maxHistoryPoints;
            if (history.throughputTx.length > maxPoints) {
                const excess = history.throughputTx.length - maxPoints;
                history.throughputTx = history.throughputTx.slice(excess);
                history.throughputRx = history.throughputRx.slice(excess);
                history.packetsTx = history.packetsTx.slice(excess);
                history.packetsRx = history.packetsRx.slice(excess);
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

    // Sort peers: exit nodes first, then online, then by name
    const sortedPeers = [...data.peers].sort((a, b) => {
        // Exit-capable nodes (allows_exit_traffic) come first
        if (a.allows_exit_traffic && !b.allows_exit_traffic) return -1;
        if (!a.allows_exit_traffic && b.allows_exit_traffic) return 1;
        // Online nodes come before offline
        if (a.online && !b.online) return -1;
        if (!a.online && b.online) return 1;
        // Then sort by name
        return a.name.localeCompare(b.name);
    });

    // Store peers data for pagination
    state.currentPeers = sortedPeers;

    // Render tables with pagination (reuse the render functions)
    renderDnsTable();
    renderPeersTable();
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

// Pagination helper - updates pagination UI elements
function updatePaginationUI(config) {
    const {
        paginationId,
        showMoreId,
        showLessId,
        shownCountId,
        totalCountId,
        totalCount,
        visibleCount
    } = config;

    const paginationEl = document.getElementById(paginationId);
    const showMoreEl = document.getElementById(showMoreId);
    const showLessEl = document.getElementById(showLessId);

    if (!paginationEl) return;

    const hasMore = totalCount > visibleCount;
    const canShowLess = visibleCount > ROWS_PER_PAGE;

    if (hasMore || canShowLess) {
        paginationEl.style.display = 'block';
        if (showMoreEl) showMoreEl.style.display = hasMore ? 'inline' : 'none';
        if (showLessEl) showLessEl.style.display = canShowLess ? 'inline' : 'none';
        if (hasMore && shownCountId && totalCountId) {
            const shownEl = document.getElementById(shownCountId);
            const totalEl = document.getElementById(totalCountId);
            if (shownEl) shownEl.textContent = visibleCount;
            if (totalEl) totalEl.textContent = totalCount;
        }
    } else {
        paginationEl.style.display = 'none';
    }
}

// Pagination functions
function showMorePeers() {
    state.peersVisibleCount += ROWS_PER_PAGE;
    renderPeersTable();
}

function showLessPeers() {
    state.peersVisibleCount = ROWS_PER_PAGE;
    renderPeersTable();
}

function showMoreDns() {
    state.dnsVisibleCount += ROWS_PER_PAGE;
    renderDnsTable();
}

function showLessDns() {
    state.dnsVisibleCount = ROWS_PER_PAGE;
    renderDnsTable();
}

function showMoreWg() {
    state.wgVisibleCount += ROWS_PER_PAGE;
    updateWGClientsTable();
}

function showLessWg() {
    state.wgVisibleCount = ROWS_PER_PAGE;
    updateWGClientsTable();
}

function renderPeersTable() {
    const peers = state.currentPeers;

    if (peers.length === 0) {
        if (dom.peersBody) dom.peersBody.innerHTML = '';
        if (dom.noPeers) dom.noPeers.style.display = 'block';
        document.getElementById('peers-pagination').style.display = 'none';
        return;
    }

    if (dom.noPeers) dom.noPeers.style.display = 'none';
    const visiblePeers = peers.slice(0, state.peersVisibleCount);
    if (!dom.peersBody) return;
    dom.peersBody.innerHTML = visiblePeers.map(peer => {
        const history = state.peerHistory[peer.name] || { throughputTx: [], throughputRx: [], packetsTx: [], packetsRx: [] };
        const peerNameEscaped = escapeHtml(peer.name);
        const exitBadge = peer.allows_exit_traffic ? '<span class="status-badge exit">EXIT</span>' : '';
        const exitVia = peer.exit_node ? `<span class="exit-via">via ${escapeHtml(peer.exit_node)}</span>` : '';
        return `
        <tr>
            <td><strong>${peerNameEscaped}</strong>${exitBadge}${exitVia}</td>
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

    updatePaginationUI({
        paginationId: 'peers-pagination',
        showMoreId: 'peers-show-more',
        showLessId: 'peers-show-less',
        shownCountId: 'peers-shown-count',
        totalCountId: 'peers-total-count',
        totalCount: peers.length,
        visibleCount: state.peersVisibleCount
    });
}

function renderDnsTable() {
    const peers = state.currentPeers;
    const domainSuffix = state.domainSuffix;

    if (peers.length === 0) {
        if (dom.dnsBody) dom.dnsBody.innerHTML = '';
        if (dom.noDns) dom.noDns.style.display = 'block';
        document.getElementById('dns-pagination').style.display = 'none';
        return;
    }

    // Build DNS records list: peer names + aliases, sorted by peer name then record name
    const dnsRecords = [];
    for (const peer of peers) {
        // Add the peer's primary DNS name
        dnsRecords.push({
            name: peer.name,
            meshIp: peer.mesh_ip,
            peerName: peer.name,
            isAlias: false
        });
        // Add aliases for this peer
        if (peer.aliases && peer.aliases.length > 0) {
            for (const alias of peer.aliases) {
                dnsRecords.push({
                    name: alias,
                    meshIp: peer.mesh_ip,
                    peerName: peer.name,
                    isAlias: true
                });
            }
        }
    }

    // Sort by peer name first (to group), then by record name
    dnsRecords.sort((a, b) => {
        if (a.peerName !== b.peerName) {
            return a.peerName.localeCompare(b.peerName);
        }
        // Primary name comes before aliases
        if (a.isAlias !== b.isAlias) {
            return a.isAlias ? 1 : -1;
        }
        return a.name.localeCompare(b.name);
    });

    if (dom.noDns) dom.noDns.style.display = 'none';
    const visibleRecords = dnsRecords.slice(0, state.dnsVisibleCount);
    if (!dom.dnsBody) return;
    dom.dnsBody.innerHTML = visibleRecords.map(record => `
        <tr>
            <td><code${record.isAlias ? ' style="color: var(--text-secondary)"' : ''}>${record.isAlias ? '↳ ' : ''}${escapeHtml(record.name)}${domainSuffix}</code></td>
            <td><code>${record.meshIp}</code></td>
        </tr>
    `).join('');

    updatePaginationUI({
        paginationId: 'dns-pagination',
        showMoreId: 'dns-show-more',
        showLessId: 'dns-show-less',
        shownCountId: 'dns-shown-count',
        totalCountId: 'dns-total-count',
        totalCount: dnsRecords.length,
        visibleCount: state.dnsVisibleCount
    });
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
    const baseChartOptions = {
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
                        second: 'HH:mm',
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

    // Throughput chart with byte formatting on Y-axis
    if (dom.throughputChart) {
        const throughputOptions = JSON.parse(JSON.stringify(baseChartOptions));
        throughputOptions.scales.y.ticks = {
            color: '#8b949e',
            maxTicksLimit: 4,
            callback: function(value) {
                return formatBytes(value) + '/s';
            }
        };
        state.charts.throughput = new Chart(dom.throughputChart, {
            type: 'line',
            data: { datasets: [] },
            options: throughputOptions
        });
    }

    // Packets chart
    if (dom.packetsChart) {
        state.charts.packets = new Chart(dom.packetsChart, {
            type: 'line',
            data: { datasets: [] },
            options: baseChartOptions
        });
    }
}

async function fetchChartHistory() {
    try {
        // Fetch up to 1 hour of history
        const since = new Date(Date.now() - MAX_RANGE_HOURS * 60 * 60 * 1000);

        const url = `api/overview?since=${since.toISOString()}&maxPoints=${MAX_CHART_POINTS}`;
        const resp = await fetch(url);
        if (!resp.ok) {
            throw new Error(`HTTP ${resp.status}`);
        }

        const data = await resp.json();
        initializeChartData(data);

        // Setup SSE AFTER history is loaded to avoid race conditions
        setupSSE();
    } catch (err) {
        console.error('Failed to fetch chart history:', err);
        // Still setup SSE even if history fails
        setupSSE();
    }
}

// Quantize timestamp to heartbeat intervals
function quantizeTimestamp(ts) {
    return Math.round(ts / QUANTIZE_INTERVAL_MS) * QUANTIZE_INTERVAL_MS;
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

    // Build set of currently online peers (present in API response AND online)
    const onlinePeers = new Set(peers.filter(p => p.online).map(p => p.name));

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
    if (newPoints.length === 0) {
        return;
    }

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
        const groupTime = group.timestamp.getTime();

        // Check if this timestamp already exists in the chart
        const existingIndex = state.charts.chartData.labels.findIndex(
            label => label.getTime() === groupTime
        );

        if (existingIndex >= 0) {
            // Timestamp exists - update data for this timestamp
            Object.entries(group.peers).forEach(([peerName, point]) => {
                if (state.charts.chartData.throughput[peerName]) {
                    // Update existing value (overwrite null with actual data)
                    if (state.charts.chartData.throughput[peerName][existingIndex] === null) {
                        state.charts.chartData.throughput[peerName][existingIndex] = point.throughput;
                        state.charts.chartData.packets[peerName][existingIndex] = point.packets;
                    }
                }
            });
            return;
        }

        // Only add if this timestamp is newer than the last one in the chart
        if (groupTime <= lastChartTime) {
            return; // Skip data points in the past
        }

        // Add timestamp
        state.charts.chartData.labels.push(group.timestamp);

        // For each existing peer, add their value
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
            } else if (!onlinePeers.has(peerName)) {
                // Peer is gone from API response - truly offline, use -1 (red line at 0)
                state.charts.chartData.throughput[peerName].push(-1);
                state.charts.chartData.packets[peerName].push(-1);
            } else {
                // Peer is online but no new heartbeat yet - use null (gap, no line drawn)
                state.charts.chartData.throughput[peerName].push(null);
                state.charts.chartData.packets[peerName].push(null);
            }
        });
    });

    // Trim to max points (rolling window) using slice - O(n) instead of O(n²) with shift loop
    const maxPoints = state.charts.maxChartPoints;
    if (state.charts.chartData.labels.length > maxPoints) {
        const excess = state.charts.chartData.labels.length - maxPoints;
        state.charts.chartData.labels = state.charts.chartData.labels.slice(excess);
        Object.keys(state.charts.chartData.throughput).forEach(peerName => {
            state.charts.chartData.throughput[peerName] = state.charts.chartData.throughput[peerName].slice(excess);
            state.charts.chartData.packets[peerName] = state.charts.chartData.packets[peerName].slice(excess);
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

    // Helper to build dataset with offline detection and highlighting
    // null = peer didn't exist yet (don't show)
    // -1 = peer was offline (show red dashed line at y=0)
    const buildDataset = (peerName, values, baseColor) => {
        const data = values.map((v, i) => ({
            x: labels[i],
            y: v === null ? null : (v === -1 ? 0 : v)
        }));

        // Track which segments are offline (were -1 in original data)
        const offlineSegments = values.map(v => v === -1);

        // Check if this peer is highlighted (selected in visualizer)
        const isHighlighted = peerName === state.charts.highlightedPeer;
        const lineColor = isHighlighted ? '#58a6ff' : baseColor;
        const lineWidth = isHighlighted ? 2 : 1.5;

        return {
            label: peerName,
            data: data,
            borderColor: lineColor,
            borderWidth: lineWidth,
            pointRadius: 0,
            tension: 0.3,
            cubicInterpolationMode: 'monotone',
            fill: false,
            spanGaps: false, // Don't connect across null gaps
            order: isHighlighted ? 0 : 1, // Highlighted peer drawn on top
            segment: {
                borderColor: ctx => {
                    const p0Offline = offlineSegments[ctx.p0DataIndex];
                    const p1Offline = offlineSegments[ctx.p1DataIndex];
                    if (p0Offline || p1Offline) {
                        return '#d32f2f'; // Red for offline
                    }
                    return lineColor;
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
        const resp = await fetch('api/wireguard/clients');
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
        const resp = await fetch('api/wireguard/clients');
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

// Prometheus alerts polling
const MAX_ALERT_HISTORY = 30;

async function checkPrometheusAvailable() {
    try {
        // Use alerts endpoint directly - if it works, Prometheus is available
        const resp = await fetch('/prometheus/api/v1/alerts');
        if (resp.ok) {
            state.alertsEnabled = true;
            document.getElementById('alerts-section').style.display = 'block';
            // Process the initial response
            const data = await resp.json();
            processAlertData(data);
            // Start polling
            setInterval(fetchAlerts, POLL_INTERVAL_MS);
        }
    } catch (err) {
        // Prometheus not available
        console.debug('Prometheus not available:', err.message);
        state.alertsEnabled = false;
    }
}

async function fetchAlerts() {
    if (!state.alertsEnabled) return;

    try {
        const resp = await fetch('/prometheus/api/v1/alerts');
        if (!resp.ok) return;

        const data = await resp.json();
        processAlertData(data);
    } catch (err) {
        console.error('Failed to fetch alerts:', err);
    }
}

function processAlertData(data) {
    const alerts = data.data?.alerts || [];

    // Count alerts by severity
    const counts = { warning: 0, critical: 0, page: 0 };
    for (const alert of alerts) {
        if (alert.state === 'firing') {
            const severity = alert.labels?.severity || 'warning';
            if (counts.hasOwnProperty(severity)) {
                counts[severity]++;
            }
        }
    }

    // Update history for sparklines
    for (const severity of ['warning', 'critical', 'page']) {
        state.alertHistory[severity].push(counts[severity]);
        if (state.alertHistory[severity].length > MAX_ALERT_HISTORY) {
            state.alertHistory[severity].shift();
        }
    }

    updateAlertTiles(counts);
}

function updateAlertTiles(counts) {
    for (const severity of ['warning', 'critical', 'page']) {
        const countEl = document.getElementById(`alert-count-${severity}`);
        const tileEl = document.getElementById(`alert-tile-${severity}`);
        const sparklineEl = document.getElementById(`alert-sparkline-${severity}`);

        if (countEl) countEl.textContent = counts[severity];

        if (tileEl) {
            if (counts[severity] > 0) {
                tileEl.classList.add('active');
            } else {
                tileEl.classList.remove('active');
            }
        }

        if (sparklineEl) {
            updateAlertSparkline(sparklineEl, state.alertHistory[severity]);
        }
    }
}

function updateAlertSparkline(svgEl, data) {
    if (!data.length) return;

    const width = 100;
    const height = 40;
    const padding = 2;

    const maxVal = Math.max(...data, 1);
    const points = data.map((val, i) => {
        const x = (i / (MAX_ALERT_HISTORY - 1)) * width;
        const y = height - padding - ((val / maxVal) * (height - 2 * padding));
        return `${x},${y}`;
    });

    // Create area path (filled below the line)
    const pathEl = svgEl.querySelector('.sparkline-path');
    if (pathEl && points.length > 1) {
        const linePath = `M${points.join(' L')}`;
        const areaPath = `${linePath} L${width},${height} L0,${height} Z`;
        pathEl.setAttribute('d', areaPath);
        pathEl.style.fill = 'currentColor';
        pathEl.style.fillOpacity = '0.2';
    }
}

function updateWGClientsTable() {
    // Check if concentrator is connected
    if (!state.wgConcentratorConnected) {
        if (dom.wgClientsBody) dom.wgClientsBody.innerHTML = '';
        if (dom.noWgClients) {
            dom.noWgClients.textContent = 'No WireGuard concentrator connected. Start a mesh peer with --wireguard flag.';
            dom.noWgClients.style.display = 'block';
        }
        document.getElementById('wg-pagination').style.display = 'none';
        if (dom.addWgClientBtn) dom.addWgClientBtn.disabled = true;
        return;
    }

    if (dom.addWgClientBtn) dom.addWgClientBtn.disabled = false;

    if (state.wgClients.length === 0) {
        if (dom.wgClientsBody) dom.wgClientsBody.innerHTML = '';
        if (dom.noWgClients) {
            dom.noWgClients.textContent = 'No WireGuard peers yet. Add a peer to generate a QR code.';
            dom.noWgClients.style.display = 'block';
        }
        document.getElementById('wg-pagination').style.display = 'none';
        return;
    }

    if (dom.noWgClients) dom.noWgClients.style.display = 'none';
    const visibleClients = state.wgClients.slice(0, state.wgVisibleCount);
    if (!dom.wgClientsBody) return;
    dom.wgClientsBody.innerHTML = visibleClients.map(client => {
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

    updatePaginationUI({
        paginationId: 'wg-pagination',
        showMoreId: 'wg-show-more',
        showLessId: 'wg-show-less',
        shownCountId: 'wg-shown-count',
        totalCountId: 'wg-total-count',
        totalCount: state.wgClients.length,
        visibleCount: state.wgVisibleCount
    });
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
        showToast('Please enter a client name', 'warning');
        return;
    }

    try {
        const resp = await fetch('api/wireguard/clients', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name })
        });

        if (!resp.ok) {
            const err = await resp.json();
            showToast('Failed to create client: ' + (err.message || 'Unknown error'), 'error');
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
        showToast('Failed to create client', 'error');
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
        const resp = await fetch(`api/wireguard/clients/${id}`, {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ enabled })
        });

        if (!resp.ok) {
            showToast('Failed to update client', 'error');
            return;
        }

        fetchWGClients();
    } catch (err) {
        console.error('Failed to toggle WG client:', err);
        showToast('Failed to update client', 'error');
    }
}

async function deleteWGClient(id, name) {
    if (!confirm(`Delete WireGuard peer "${name}"?`)) {
        return;
    }

    try {
        const resp = await fetch(`api/wireguard/clients/${id}`, {
            method: 'DELETE'
        });

        if (!resp.ok) {
            showToast('Failed to delete client', 'error');
            return;
        }

        fetchWGClients();
    } catch (err) {
        console.error('Failed to delete WG client:', err);
        showToast('Failed to delete client', 'error');
    }
}

// Highlight peer on charts when selected
function highlightPeerOnCharts(peerName) {
    state.charts.highlightedPeer = peerName;
    rebuildChartDatasets();
}

// Central function to select a node - emits event for all listeners
// Exposed globally so it can be called from HTML onclick handlers
function selectNode(nodeId) {
    state.selectedNodeId = nodeId;
    events.emit('nodeSelected', nodeId);
}
window.selectNode = selectNode;

// Initialize visualizer
function initVisualizer() {
    const canvas = document.getElementById('visualizer-canvas');
    if (!canvas) return;

    state.visualizer = new NodeVisualizer(canvas);
    state.visualizer.setDomainSuffix(state.domainSuffix);

    // Wire up visualizer selection to central event system
    state.visualizer.onNodeSelected = (nodeId) => {
        selectNode(nodeId);
    };

    // Subscribe visualizer to selection events (for external selection changes)
    events.on('nodeSelected', (nodeId) => {
        if (state.visualizer) {
            state.visualizer.setSelection(nodeId);
        }
    });

    // Subscribe charts to selection events
    events.on('nodeSelected', highlightPeerOnCharts);
}

// Initialize map
function initMap() {
    const container = document.getElementById('node-map');
    if (!container) return;

    state.nodeMap = new NodeMap('node-map');
    // Map is initialized lazily when first peer with location is added

    // Subscribe map to selection events
    events.on('nodeSelected', (nodeId) => {
        if (state.nodeMap) {
            state.nodeMap.setSelectedPeer(nodeId);
        }
    });
}

// Initialize
document.addEventListener('DOMContentLoaded', () => {
    // Cache DOM elements first
    initDOMCache();

    // Initialize visualizer
    initVisualizer();

    // Initialize map (for geolocation display)
    initMap();

    // Initialize charts
    initCharts();

    // Fetch initial chart history (up to 1 hour)
    // SSE is set up AFTER history is loaded (inside fetchChartHistory)
    fetchChartHistory();

    fetchData(true); // Load with history on initial fetch

    // Check if WireGuard is enabled and setup handlers
    checkWireGuardStatus();
    setInterval(fetchWGClients, POLL_INTERVAL_MS);

    // Check if Prometheus is available for alerts
    checkPrometheusAvailable();

    // Add client button handler
    if (dom.addWgClientBtn) {
        dom.addWgClientBtn.addEventListener('click', showAddWGClientModal);
    }

    // Close modal on background click
    if (dom.wgModal) {
        dom.wgModal.addEventListener('click', (e) => {
            if (e.target === dom.wgModal) {
                closeWGModal();
            }
        });
    }
});

// Cleanup on page unload to prevent memory leaks
window.addEventListener('beforeunload', cleanup);
