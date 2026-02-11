// Import constants from TM.utils (loaded via lib/utils.js)
const {
    POLL_INTERVAL_MS,
    SSE_RETRY_DELAY_MS,
    MAX_SSE_RETRIES,
    ROWS_PER_PAGE,
    MAX_HISTORY_POINTS,
    MAX_CHART_POINTS,
    TOAST_DURATION_MS,
    TOAST_FADE_MS,
    QUANTIZE_INTERVAL_MS,
} = TM.utils.CONSTANTS;

// Import utilities from TM modules
const { escapeHtml } = TM.utils;
const { formatBytes, formatRate, formatLatency, formatLastSeen, formatExpiry } = TM.format;
const { createPaginationController } = TM.pagination;
const { createSparklineSVG } = TM.table;
const { createModalController } = TM.modal;

// Panel refresh lists - defines which panels to refresh for each tab
const _PANELS_MESH_TAB = ['peers', 'wg-clients', 'logs', 'alerts', 'filter']; // Mesh tab panels (visualizer/charts/map loaded via fetchData)
const PANELS_APP_TAB = ['s3', 'shares', 'docker']; // App tab panels
const PANELS_DATA_TAB = ['peers-mgmt', 'groups', 'bindings', 'dns']; // Data tab panels

// Toggle collapsible section
function toggleSection(header) {
    header.classList.toggle('collapsed');
    const content = header.nextElementSibling;
    if (content?.classList.contains('collapsible-content')) {
        const wasCollapsed = content.classList.contains('collapsed');
        content.classList.toggle('collapsed');

        // If we just expanded the section, refresh any maps inside
        if (wasCollapsed) {
            requestAnimationFrame(() => {
                const section = header.closest('section');
                if (section && section.id === 'map-section' && state.nodeMap) {
                    state.nodeMap.refresh();
                }
            });
        }
    }
}
window.toggleSection = toggleSection;

// Cached DOM elements (populated on DOMContentLoaded)
const dom = {};

// Use event bus from TM.events module
const events = TM.events;

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
            labels: [], // timestamps (shared across both charts)
            throughput: {}, // { peerName: [values] }
            packets: {}, // { peerName: [values] }
        },
        highlightedPeer: null, // Peer to highlight on charts (selected in visualizer)
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
    peersMgmtVisibleCount: ROWS_PER_PAGE,
    groupsVisibleCount: ROWS_PER_PAGE,
    sharesVisibleCount: ROWS_PER_PAGE,
    bindingsVisibleCount: ROWS_PER_PAGE,
    currentPeers: [], // Store current peers data for pagination
    currentDnsRecords: [], // Store current DNS records for pagination
    currentPeersMgmt: [],
    currentGroups: [],
    currentShares: [],
    currentBindings: [],
    // Alerts state
    alertsEnabled: false,
    peerAlerts: {}, // { peerName: { warning: count, critical: count, page: count } }
    // Loki logs state
    lokiEnabled: false,
};

// Green gradient for chart lines (dim to bright based on outlier status)
// Dimmest green for average peers, brightest for outliers
const GREEN_GRADIENT = [
    '#2d4a37', // dimmest - most average
    '#3d6b4a',
    '#3fb950', // middle
    '#56d364',
    '#7ee787', // brightest - outliers
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

    // Filter elements
    dom.filterSection = document.getElementById('filter-section');
    dom.filterPeerSelect = document.getElementById('filter-peer');
    dom.filterRulesBody = document.getElementById('filter-rules-body');
    dom.noFilterRules = document.getElementById('no-filter-rules');
    dom.filterModal = document.getElementById('filter-modal');
    dom.filterInfo = document.getElementById('filter-info');
    dom.filterDefaultPolicy = document.getElementById('filter-default-policy');
    dom.filterTempWarning = document.getElementById('filter-temp-warning');
    dom.addFilterRuleBtn = document.getElementById('add-filter-rule-btn');

    // Chart canvases
    dom.throughputChart = document.getElementById('throughput-chart');
    dom.packetsChart = document.getElementById('packets-chart');

    // Visualizer
    dom.visualizerCanvas = document.getElementById('visualizer-canvas');

    // Toast container
    dom.toastContainer = document.getElementById('toast-container');

    // Logs resize handle
    dom.logsContainer = document.getElementById('logs-link');
    dom.logsResizeHandle = document.getElementById('logs-resize-handle');

    // S3 resize handle
    dom.s3Content = document.getElementById('s3-content');
    dom.s3ResizeHandle = document.getElementById('s3-resize-handle');
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

    state.eventSource.addEventListener('heartbeat', (e) => {
        // Refresh dashboard when a heartbeat is received
        console.log('[DEBUG] Heartbeat received');
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
        banner.style.cssText =
            'background:#d32f2f;color:white;padding:12px 20px;text-align:center;position:fixed;top:0;left:0;right:0;z-index:1000;';
        banner.innerHTML =
            'Authentication required. <a href="/admin/" style="color:white;text-decoration:underline;">Click here to login</a>';
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

    // Domain suffix is hardcoded (aliases .tm/.mesh redirect to .tunnelmesh)
    state.domainSuffix = '.tunnelmesh';

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
    data.peers.forEach((peer) => {
        if (!state.peerHistory[peer.name]) {
            state.peerHistory[peer.name] = {
                throughputTx: [],
                throughputRx: [],
                packetsTx: [],
                packetsRx: [],
            };
        }
        const history = state.peerHistory[peer.name];

        // If loading history from server, populate from server data
        if (loadHistory && peer.history && peer.history.length > 0) {
            // Server returns newest first, reverse to get oldest first
            const serverHistory = [...peer.history].reverse();
            history.throughputTx = serverHistory.map((h) => h.txB || 0);
            history.throughputRx = serverHistory.map((h) => h.rxB || 0);
            history.packetsTx = serverHistory.map((h) => h.txP || 0);
            history.packetsRx = serverHistory.map((h) => h.rxP || 0);
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
    const currentPeers = new Set(data.peers.map((p) => p.name));
    Object.keys(state.peerHistory).forEach((name) => {
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

    // Render peers table with pagination
    renderPeersTable();

    // Show filter section and populate peer dropdown if we have peers
    if (sortedPeers.length > 0 && dom.filterSection) {
        dom.filterSection.style.display = 'block';
        populateFilterPeerSelect(sortedPeers);
    }
}

// Pagination controllers
const peersPagination = createPaginationController({
    pageSize: ROWS_PER_PAGE,
    getItems: () => state.currentPeers,
    getVisibleCount: () => state.peersVisibleCount,
    setVisibleCount: (n) => {
        state.peersVisibleCount = n;
    },
    onRender: () => renderPeersTable(),
});

const dnsPagination = createPaginationController({
    pageSize: ROWS_PER_PAGE,
    getItems: () => state.currentDnsRecords || [],
    getVisibleCount: () => state.dnsVisibleCount,
    setVisibleCount: (n) => {
        state.dnsVisibleCount = n;
    },
    onRender: () => renderDnsTable(),
});

const wgPagination = createPaginationController({
    pageSize: ROWS_PER_PAGE,
    getItems: () => state.wgClients,
    getVisibleCount: () => state.wgVisibleCount,
    setVisibleCount: (n) => {
        state.wgVisibleCount = n;
    },
    onRender: () => updateWGClientsTable(),
});

const peersMgmtPagination = createPaginationController({
    pageSize: ROWS_PER_PAGE,
    getItems: () => state.currentPeersMgmt,
    getVisibleCount: () => state.peersMgmtVisibleCount,
    setVisibleCount: (n) => {
        state.peersMgmtVisibleCount = n;
    },
    onRender: () => renderPeersMgmtTable(),
});

const groupsPagination = createPaginationController({
    pageSize: ROWS_PER_PAGE,
    getItems: () => state.currentGroups,
    getVisibleCount: () => state.groupsVisibleCount,
    setVisibleCount: (n) => {
        state.groupsVisibleCount = n;
    },
    onRender: () => renderGroupsTable(),
});

const sharesPagination = createPaginationController({
    pageSize: ROWS_PER_PAGE,
    getItems: () => state.currentShares,
    getVisibleCount: () => state.sharesVisibleCount,
    setVisibleCount: (n) => {
        state.sharesVisibleCount = n;
    },
    onRender: () => renderSharesTable(),
});

const bindingsPagination = createPaginationController({
    pageSize: ROWS_PER_PAGE,
    getItems: () => state.currentBindings,
    getVisibleCount: () => state.bindingsVisibleCount,
    setVisibleCount: (n) => {
        state.bindingsVisibleCount = n;
    },
    onRender: () => renderBindingsTable(),
});

// Helper to update pagination UI for a section
// Can be called with (prefix, controller) or (prefix, { total, shown, hasMore, canShowLess })
function updateSectionPagination(prefix, controllerOrState) {
    const uiState =
        typeof controllerOrState.getUIState === 'function' ? controllerOrState.getUIState() : controllerOrState;
    const paginationEl = document.getElementById(`${prefix}-pagination`);
    if (!paginationEl) return;
    if (uiState.isEmpty || uiState.total === 0) {
        paginationEl.style.display = 'none';
        return;
    }
    paginationEl.style.display = uiState.hasMore || uiState.canShowLess ? 'block' : 'none';
    document.getElementById(`${prefix}-show-more`).style.display = uiState.hasMore ? 'inline' : 'none';
    document.getElementById(`${prefix}-show-less`).style.display = uiState.canShowLess ? 'inline' : 'none';
    document.getElementById(`${prefix}-shown-count`).textContent = uiState.shown;
    document.getElementById(`${prefix}-total-count`).textContent = uiState.total;
}
window.updateSectionPagination = updateSectionPagination;

// Expose pagination functions for HTML onclick handlers
window.showMorePeers = () => peersPagination.showMore();
window.showLessPeers = () => peersPagination.showLess();
window.showMoreDns = () => dnsPagination.showMore();
window.showLessDns = () => dnsPagination.showLess();
window.showMoreWg = () => wgPagination.showMore();
window.showLessWg = () => wgPagination.showLess();
window.showMorePeersMgmt = () => peersMgmtPagination.showMore();
window.showLessPeersMgmt = () => peersMgmtPagination.showLess();
window.showMoreGroups = () => groupsPagination.showMore();
window.showLessGroups = () => groupsPagination.showLess();
window.showMoreShares = () => sharesPagination.showMore();
window.showLessShares = () => sharesPagination.showLess();
window.showMoreBindings = () => bindingsPagination.showMore();
window.showLessBindings = () => bindingsPagination.showLess();
window.showLessWg = () => wgPagination.showLess();

// Modal controllers (initialized with element IDs, resolved lazily)
const wgModal = createModalController('wg-modal', {
    onClose: () => {
        state.currentWGConfig = null;
        fetchWGClients();
    },
});

const filterModal = createModalController('filter-modal', {
    onClose: () => {
        // Clear form
        const port = document.getElementById('filter-rule-port');
        if (port) port.value = '';
        const sourcePeer = document.getElementById('filter-rule-source-peer');
        if (sourcePeer) sourcePeer.value = '';
        const destPeer = document.getElementById('filter-rule-dest-peer');
        if (destPeer) destPeer.value = '__all__';
    },
});

function renderPeersTable() {
    const peers = state.currentPeers;
    const uiState = peersPagination.getUIState();

    if (peers.length === 0) {
        if (dom.peersBody) dom.peersBody.innerHTML = '';
        if (dom.noPeers) dom.noPeers.style.display = 'block';
        document.getElementById('peers-pagination').style.display = 'none';
        return;
    }

    if (dom.noPeers) dom.noPeers.style.display = 'none';
    const visiblePeers = peersPagination.getVisibleItems();
    if (!dom.peersBody) return;
    dom.peersBody.innerHTML = visiblePeers
        .map((peer) => {
            const history = state.peerHistory[peer.name] || {
                throughputTx: [],
                throughputRx: [],
                packetsTx: [],
                packetsRx: [],
            };
            const peerNameEscaped = escapeHtml(peer.name);
            // Alert badge - show if peer has any firing alerts
            const peerAlert = state.peerAlerts[peer.name];
            const hasAlert = peerAlert && (peerAlert.warning > 0 || peerAlert.critical > 0 || peerAlert.page > 0);
            const alertSeverity = peerAlert?.page > 0 ? 'page' : peerAlert?.critical > 0 ? 'critical' : 'warning';
            const alertBadge = hasAlert
                ? `<span class="status-badge alert-icon" title="${alertSeverity}">⚠</span>`
                : '';
            const exitBadge = peer.allows_exit_traffic ? '<span class="status-badge exit">EXIT</span>' : '';
            const exitVia = peer.exit_node ? `<span class="exit-via">via ${escapeHtml(peer.exit_node)}</span>` : '';
            // Tunnel count from connections map
            const tunnelCount = peer.connections ? Object.keys(peer.connections).length : 0;
            const tunnelSuffix = tunnelCount > 0 ? ` <span class="tunnel-count">(${tunnelCount})</span>` : '';
            return `
        <tr>
            <td><strong>${peerNameEscaped}</strong>${alertBadge}${exitBadge}${exitVia}</td>
            <td><code>${peer.mesh_ip}</code>${tunnelSuffix}</td>
            <td>${formatLatency(peer.coordinator_rtt_ms)}</td>
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
            <td><span class="status-badge ${peer.online ? 'online' : 'offline'}">${peer.online ? 'Online' : 'Offline'}</span></td>
        </tr>
    `;
        })
        .join('');

    // Update pagination UI using controller state
    const peersPaginationEl = document.getElementById('peers-pagination');
    if (peersPaginationEl) {
        peersPaginationEl.style.display = uiState.isEmpty ? 'none' : 'flex';
        document.getElementById('peers-show-more').style.display = uiState.hasMore ? 'inline' : 'none';
        document.getElementById('peers-show-less').style.display = uiState.canShowLess ? 'inline' : 'none';
        document.getElementById('peers-shown-count').textContent = uiState.shown;
        document.getElementById('peers-total-count').textContent = uiState.total;
    }
}

// Fetch DNS records from API
async function fetchDnsRecords() {
    try {
        const resp = await fetch('/api/dns');
        if (!resp.ok) {
            throw new Error(`HTTP ${resp.status}`);
        }
        const data = await resp.json();

        // Sort records by hostname
        state.currentDnsRecords = (data.records || []).sort((a, b) => a.hostname.localeCompare(b.hostname));

        renderDnsTable();
    } catch (err) {
        console.error('Failed to fetch DNS records:', err);
        state.currentDnsRecords = [];
        renderDnsTable();
    }
}

function renderDnsTable() {
    const records = state.currentDnsRecords || [];

    if (records.length === 0) {
        if (dom.dnsBody) dom.dnsBody.innerHTML = '';
        if (dom.noDns) dom.noDns.style.display = 'block';
        document.getElementById('dns-pagination').style.display = 'none';
        return;
    }

    if (dom.noDns) dom.noDns.style.display = 'none';
    const visibleRecords = dnsPagination.getVisibleItems();
    if (!dom.dnsBody) return;
    dom.dnsBody.innerHTML = visibleRecords
        .map(
            (record) => `
        <tr>
            <td><code>${escapeHtml(record.hostname)}</code></td>
            <td><code>${escapeHtml(record.mesh_ip)}</code></td>
        </tr>
    `,
        )
        .join('');

    // Update pagination UI using controller state
    const dnsUIState = dnsPagination.getUIState();
    const dnsPaginationEl = document.getElementById('dns-pagination');
    if (dnsPaginationEl) {
        dnsPaginationEl.style.display = dnsUIState.isEmpty ? 'none' : 'flex';
        document.getElementById('dns-show-more').style.display = dnsUIState.hasMore ? 'inline' : 'none';
        document.getElementById('dns-show-less').style.display = dnsUIState.canShowLess ? 'inline' : 'none';
        document.getElementById('dns-shown-count').textContent = dnsUIState.shown;
        document.getElementById('dns-total-count').textContent = dnsUIState.total;
    }
}

// Chart functions

function initCharts() {
    console.log('[DEBUG] initCharts called');
    const baseChartOptions = {
        responsive: true,
        maintainAspectRatio: false,
        animation: false,
        interaction: {
            mode: 'index',
            intersect: false,
        },
        scales: {
            x: {
                type: 'time',
                time: {
                    displayFormats: {
                        second: 'HH:mm',
                        minute: 'HH:mm',
                        hour: 'HH:mm',
                        day: 'd MMM',
                    },
                },
                grid: { display: false },
                ticks: { color: '#8b949e', maxTicksLimit: 5, maxRotation: 0 },
                border: { display: false },
            },
            y: {
                beginAtZero: true,
                grid: { color: '#21262d' },
                ticks: { color: '#8b949e', maxTicksLimit: 4 },
                border: { display: false },
            },
        },
        plugins: {
            legend: { display: false },
            tooltip: { enabled: false },
        },
    };

    // Throughput chart with byte formatting on Y-axis
    if (dom.throughputChart) {
        const throughputOptions = JSON.parse(JSON.stringify(baseChartOptions));
        throughputOptions.scales.y.ticks = {
            color: '#8b949e',
            maxTicksLimit: 4,
            callback: function (value) {
                return `${formatBytes(value)}/s`;
            },
        };
        state.charts.throughput = new Chart(dom.throughputChart, {
            type: 'line',
            data: { datasets: [] },
            options: throughputOptions,
        });
        console.log('[DEBUG] Throughput chart created, canvas size:', dom.throughputChart.width, 'x', dom.throughputChart.height);
    }

    // Packets chart
    if (dom.packetsChart) {
        state.charts.packets = new Chart(dom.packetsChart, {
            type: 'line',
            data: { datasets: [] },
            options: baseChartOptions,
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
    const peerLastTs = {}; // Track when each peer was last seen

    data.peers.forEach((peer) => {
        // Initialize peer history map even if no history yet
        peerHistories[peer.name] = {};

        if (peer.history && peer.history.length > 0) {
            // History comes newest first, reverse to get oldest first
            const history = [...peer.history].reverse();

            history.forEach((point) => {
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
        }
    });

    // Sort timestamps
    const sortedTimestamps = Array.from(allTimestamps).sort((a, b) => a - b);
    state.charts.chartData.labels = sortedTimestamps.map((ts) => new Date(ts));

    // Build data arrays for each peer
    Object.entries(peerHistories).forEach(([peerName, historyMap]) => {
        const throughputData = [];
        const packetsData = [];
        const firstTs = peerFirstTs[peerName];
        const lastTs = peerLastTs[peerName];

        // If peer has no history, initialize with nulls matching existing timeline
        // This keeps arrays aligned when new data points are added
        if (!firstTs && !lastTs) {
            state.charts.chartData.throughput[peerName] = new Array(sortedTimestamps.length).fill(null);
            state.charts.chartData.packets[peerName] = new Array(sortedTimestamps.length).fill(null);
            // Don't set lastSeenTimes - let updateChartsWithNewData handle first heartbeat
            return;
        }

        sortedTimestamps.forEach((ts) => {
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
    console.log('[DEBUG] updateChartsWithNewData called, peers:', peers?.length);
    if (!state.charts.throughput || !state.charts.packets) {
        console.log('[DEBUG] Charts not initialized, skipping update');
        return;
    }

    // Track last seen times to detect new heartbeats
    if (!state.charts.lastSeenTimes) {
        state.charts.lastSeenTimes = {};
    }

    // Build set of currently online peers (present in API response AND online)
    const onlinePeers = new Set(peers.filter((p) => p.online).map((p) => p.name));

    // Collect new data points with their timestamps
    const newPoints = [];

    peers.forEach((peer) => {
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
                packets: (peer.packets_sent_rate || 0) + (peer.packets_received_rate || 0),
            });
        }
    });

    // No new data
    if (newPoints.length === 0) {
        console.log('[DEBUG] No new data points to add');
        return;
    }
    console.log('[DEBUG] Adding', newPoints.length, 'new data points');

    // Group new points by quantized timestamp (10-second intervals)
    const groups = new Map();
    newPoints.forEach((point) => {
        const key = point.timestamp.getTime();
        if (!groups.has(key)) {
            groups.set(key, { timestamp: point.timestamp, peers: {} });
        }
        groups.get(key).peers[point.peer] = point;
    });

    // Sort groups by timestamp and add each as a data point
    const sortedGroups = Array.from(groups.values()).sort((a, b) => a.timestamp - b.timestamp);

    // First pass: Ensure all online peers exist in chartData
    // This must happen BEFORE we start adding new timestamps, otherwise peers
    // whose first heartbeat arrives at an existing timestamp never get initialized
    peers.forEach((peer) => {
        if (!state.charts.chartData.throughput[peer.name]) {
            // New peer - initialize with nulls for all existing timestamps
            const currentLen = state.charts.chartData.labels.length;
            state.charts.chartData.throughput[peer.name] = new Array(currentLen).fill(null);
            state.charts.chartData.packets[peer.name] = new Array(currentLen).fill(null);
            console.log('[DEBUG] New peer detected and initialized:', peer.name);
        }
    });

    // Get the last timestamp in the chart (if any)
    const lastChartTime =
        state.charts.chartData.labels.length > 0
            ? state.charts.chartData.labels[state.charts.chartData.labels.length - 1].getTime()
            : 0;

    sortedGroups.forEach((group) => {
        const groupTime = group.timestamp.getTime();

        // Check if this timestamp already exists in the chart
        const existingIndex = state.charts.chartData.labels.findIndex((label) => label.getTime() === groupTime);

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
        const allPeers = new Set([...Object.keys(state.charts.chartData.throughput), ...Object.keys(group.peers)]);

        allPeers.forEach((peerName) => {
            // Check if this is a new peer (fallback - should be caught by first pass above)
            const isNewPeer = !state.charts.chartData.throughput[peerName];

            // Initialize arrays if needed (fill with nulls for timestamps before they existed)
            if (isNewPeer) {
                console.log('[DEBUG] New peer detected (fallback):', peerName);
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
        Object.keys(state.charts.chartData.throughput).forEach((peerName) => {
            state.charts.chartData.throughput[peerName] = state.charts.chartData.throughput[peerName].slice(excess);
            state.charts.chartData.packets[peerName] = state.charts.chartData.packets[peerName].slice(excess);
        });
    }

    // Clean up peers that are no longer present
    const currentPeers = new Set(peers.map((p) => p.name));
    Object.keys(state.charts.chartData.throughput).forEach((peerName) => {
        if (!currentPeers.has(peerName)) {
            delete state.charts.chartData.throughput[peerName];
            delete state.charts.chartData.packets[peerName];
            delete state.charts.lastSeenTimes[peerName];
        }
    });

    rebuildChartDatasets();

    // Update x-axis range to show all data including new points
    fitChartsToData();
}

// Calculate color for each peer based on how much they deviate from average
// Peers close to average get dim green, outliers get bright green
function calculatePeerColors(dataMap) {
    const peerAverages = {};
    const allAverages = [];

    // Calculate average for each peer
    Object.entries(dataMap).forEach(([peerName, values]) => {
        const validValues = values.filter((v) => v !== null && v > 0);
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
    const deviations = allAverages.map((avg) => Math.abs(avg - overallAvg));
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
            y: v === null ? null : v === -1 ? 0 : v,
        }));

        // Track which segments are offline (were -1 in original data)
        const offlineSegments = values.map((v) => v === -1);

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
                borderColor: (ctx) => {
                    const p0Offline = offlineSegments[ctx.p0DataIndex];
                    const p1Offline = offlineSegments[ctx.p1DataIndex];
                    if (p0Offline || p1Offline) {
                        return '#d32f2f'; // Red for offline
                    }
                    return lineColor;
                },
                borderDash: (ctx) => {
                    const p0Offline = offlineSegments[ctx.p0DataIndex];
                    const p1Offline = offlineSegments[ctx.p1DataIndex];
                    if (p0Offline || p1Offline) {
                        return [5, 5];
                    }
                    return undefined;
                },
            },
        };
    };

    // Build throughput datasets
    const throughputDatasets = Object.entries(state.charts.chartData.throughput).map(([peerName, values]) => {
        return buildDataset(peerName, values, throughputColors[peerName] || GREEN_GRADIENT[2]);
    });
    console.log('[DEBUG] rebuildChartDatasets: peer names:', Object.keys(state.charts.chartData.throughput));

    // Build packets datasets
    const packetsDatasets = Object.entries(state.charts.chartData.packets).map(([peerName, values]) => {
        return buildDataset(peerName, values, packetsColors[peerName] || GREEN_GRADIENT[2]);
    });

    if (state.charts.throughput) {
        state.charts.throughput.data.labels = labels;
        state.charts.throughput.data.datasets = throughputDatasets;
        state.charts.throughput.update();
        console.log('[DEBUG] Throughput chart updated, labels:', labels.length, 'datasets:', throughputDatasets.length);
    }

    if (state.charts.packets) {
        state.charts.packets.data.labels = labels;
        state.charts.packets.data.datasets = packetsDatasets;
        state.charts.packets.update();
        console.log('[DEBUG] Packets chart updated, labels:', labels.length, 'datasets:', packetsDatasets.length);
    }
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
    } catch (_err) {
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

async function checkPrometheusAvailable() {
    try {
        // Use alerts endpoint directly - if it works, Prometheus is available
        const resp = await fetch('/prometheus/api/v1/alerts');
        if (resp.ok) {
            state.alertsEnabled = true;
            // Process the initial response
            const data = await resp.json();
            processAlertData(data);
            // Start polling
            setInterval(fetchAlerts, POLL_INTERVAL_MS);
            // Enable chart wrappers as clickable links to Grafana
            enableChartLinks();
        }
    } catch (err) {
        // Prometheus not available
        console.error('Prometheus not available:', err.message);
        state.alertsEnabled = false;
    }
}

function enableChartLinks() {
    const throughputWrapper = document.getElementById('throughput-wrapper');
    const packetsWrapper = document.getElementById('packets-wrapper');
    const grafanaUrl = '/grafana/';

    [throughputWrapper, packetsWrapper].forEach((wrapper) => {
        if (wrapper) {
            wrapper.classList.add('clickable-card');
            wrapper.addEventListener('click', () => {
                window.location.href = grafanaUrl;
            });
        }
    });
}

// Loki logs
async function checkLokiAvailable() {
    try {
        // First get the Loki datasource info from Grafana
        const dsResp = await fetch('/grafana/api/datasources/name/Loki');
        if (!dsResp.ok) {
            console.debug('Loki datasource not found');
            return;
        }
        const dsInfo = await dsResp.json();
        state.lokiDatasourceId = dsInfo.id;
        state.lokiDatasourceUid = dsInfo.uid;

        // Test query to verify Loki is responding
        const testResp = await fetch(`/grafana/api/datasources/proxy/${dsInfo.id}/loki/api/v1/labels`);
        if (testResp.ok) {
            state.lokiEnabled = true;
            document.getElementById('logs-section').style.display = 'block';
            updateLokiExploreLink();
            fetchLogs();
            setInterval(fetchLogs, POLL_INTERVAL_MS);
        }
    } catch (err) {
        console.debug('Loki not available:', err.message);
        state.lokiEnabled = false;
    }
}

function updateLokiExploreLink() {
    const logsLink = document.getElementById('logs-link');
    if (!logsLink) return;

    // Build Grafana explore URL with Loki query
    const now = Date.now();
    const from = now - 3600000; // 1 hour ago
    const dsUid = state.lokiDatasourceUid || 'loki';
    const leftParam = encodeURIComponent(
        JSON.stringify({
            datasource: { type: 'loki', uid: dsUid },
            queries: [{ refId: 'A', expr: '{job="tunnelmesh"}', queryType: 'range' }],
            range: { from: String(from), to: String(now) },
        }),
    );
    logsLink.href = `/grafana/explore?orgId=1&left=${leftParam}`;
}

async function fetchLogs() {
    if (!state.lokiEnabled || !state.lokiDatasourceId) return;

    try {
        const peerCount = Math.max(state.currentPeers.length, 1);
        const limit = 25 * peerCount;
        const now = Date.now() * 1000000; // nanoseconds
        const oneHourAgo = now - 3600 * 1000000000;

        const query = encodeURIComponent('{job="tunnelmesh"}');
        const url = `/grafana/api/datasources/proxy/${state.lokiDatasourceId}/loki/api/v1/query_range?query=${query}&start=${oneHourAgo}&end=${now}&limit=${limit}&direction=backward`;

        const resp = await fetch(url);
        if (!resp.ok) return;

        const data = await resp.json();
        displayLogs(data);
    } catch (err) {
        console.error('Failed to fetch logs:', err);
    }
}

function displayLogs(data) {
    const logsContent = document.getElementById('logs-content');
    const noLogs = document.getElementById('no-logs');
    if (!logsContent) return;

    const streams = data.data?.result || [];
    const allEntries = [];

    // Collect all log entries from all streams
    for (const stream of streams) {
        const labels = stream.stream || {};
        for (const [timestamp, line] of stream.values || []) {
            allEntries.push({
                timestamp: parseInt(timestamp, 10) / 1000000, // Convert to milliseconds
                line,
                labels,
            });
        }
    }

    // Sort by timestamp descending (newest first)
    allEntries.sort((a, b) => b.timestamp - a.timestamp);

    if (allEntries.length === 0) {
        logsContent.innerHTML = '';
        if (noLogs) noLogs.style.display = 'block';
        return;
    }

    if (noLogs) noLogs.style.display = 'none';

    const html = allEntries
        .map((entry) => {
            const date = new Date(entry.timestamp);
            const timeStr = date.toISOString().replace('T', ' ').substring(0, 23);

            // Parse the log line - try JSON first
            let level = 'info';
            let message = entry.line;

            try {
                const parsed = JSON.parse(entry.line);
                level = (parsed.level || 'info').toLowerCase();
                message = formatLogJson(parsed);
            } catch {
                // Not JSON, check for level prefix
                const levelMatch = entry.line.match(/^(DEBUG|INFO|WARN|ERROR)/i);
                if (levelMatch) {
                    level = levelMatch[1].toLowerCase();
                    message = escapeHtml(entry.line.substring(levelMatch[0].length).trim());
                } else {
                    message = escapeHtml(entry.line);
                }
            }

            return `<div class="log-entry">
            <span class="log-timestamp">${timeStr}</span>
            <span class="log-level ${level}">${level.toUpperCase()}</span>
            <span class="log-message">${message}</span>
        </div>`;
        })
        .join('');

    logsContent.innerHTML = html;
}

function formatLogJson(obj) {
    const parts = [];
    for (const [key, value] of Object.entries(obj)) {
        if (key === 'level' || key === 'ts' || key === 'time') continue;
        const valueStr =
            typeof value === 'string'
                ? `<span class="log-string">"${escapeHtml(value)}"</span>`
                : `<span class="log-value">${escapeHtml(String(value))}</span>`;
        parts.push(`<span class="log-key">${escapeHtml(key)}</span>:${valueStr}`);
    }
    return `{${parts.join(',')}}`;
}

async function fetchAlerts() {
    console.log('[DEBUG] fetchAlerts called, alertsEnabled:', state.alertsEnabled);
    if (!state.alertsEnabled) return;

    try{
        const resp = await fetch('/prometheus/api/v1/alerts');
        if (!resp.ok) {
            console.log('[DEBUG] fetchAlerts failed, status:', resp.status);
            return;
        }

        const data = await resp.json();
        console.log('[DEBUG] fetchAlerts got', data.data?.alerts?.length, 'alerts');
        processAlertData(data);
    } catch (err) {
        console.error('Failed to fetch alerts:', err);
    }
}

function processAlertData(data) {
    const alerts = data.data?.alerts || [];

    // Count alerts by severity
    const counts = { warning: 0, critical: 0, page: 0 };
    // Track alerts per peer
    const peerAlerts = {};

    for (const alert of alerts) {
        if (alert.state === 'firing') {
            const severity = alert.labels?.severity || 'warning';

            // Count alert by severity
            if (Object.hasOwn(counts, severity)) {
                counts[severity]++;
            }

            // Extract peer name from instance label (format: name.tunnelmesh:port or mesh_ip:port)
            const instance = alert.labels?.instance || '';
            const peerMatch = instance.match(/^([^.:]+)/);
            if (peerMatch) {
                const peerName = peerMatch[1];
                if (!peerAlerts[peerName]) {
                    peerAlerts[peerName] = { warning: 0, critical: 0, page: 0 };
                }
                if (Object.hasOwn(peerAlerts[peerName], severity)) {
                    peerAlerts[peerName][severity]++;
                }
            }
        }
    }

    state.peerAlerts = peerAlerts;
    updateAlertTiles(counts);
    // Re-render peers table to show alert badges
    renderPeersTable();
}

function updateAlertTiles(counts) {
    for (const severity of ['warning', 'critical', 'page']) {
        const countEl = document.getElementById(`alert-count-${severity}`);
        const tileEl = document.getElementById(`alert-tile-${severity}`);

        if (countEl) countEl.textContent = counts[severity];

        if (tileEl) {
            if (counts[severity] > 0) {
                tileEl.classList.add('active');
            } else {
                tileEl.classList.remove('active');
            }
        }
    }
}

function updateWGClientsTable() {
    // Check if concentrator is connected
    if (!state.wgConcentratorConnected) {
        if (dom.wgClientsBody) dom.wgClientsBody.innerHTML = '';
        if (dom.noWgClients) {
            dom.noWgClients.textContent =
                'No WireGuard concentrator connected. Start a mesh peer with --wireguard flag.';
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
    const visibleClients = wgPagination.getVisibleItems();
    if (!dom.wgClientsBody) return;
    dom.wgClientsBody.innerHTML = visibleClients
        .map((client) => {
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
        })
        .join('');

    // Update pagination UI using controller state
    const wgUIState = wgPagination.getUIState();
    const wgPaginationEl = document.getElementById('wg-pagination');
    if (wgPaginationEl) {
        wgPaginationEl.style.display = wgUIState.isEmpty ? 'none' : 'flex';
        document.getElementById('wg-show-more').style.display = wgUIState.hasMore ? 'inline' : 'none';
        document.getElementById('wg-show-less').style.display = wgUIState.canShowLess ? 'inline' : 'none';
        document.getElementById('wg-shown-count').textContent = wgUIState.shown;
        document.getElementById('wg-total-count').textContent = wgUIState.total;
    }
}

function showAddWGClientModal() {
    if (!state.wgConcentratorConnected) {
        return; // Don't open modal if no concentrator
    }
    document.getElementById('wg-modal-title').textContent = 'Add WireGuard Peer';
    document.getElementById('wg-add-form').style.display = 'block';
    document.getElementById('wg-config-display').style.display = 'none';
    document.getElementById('wg-client-name').value = '';
    wgModal.open();
}

function closeWGModal() {
    wgModal.close();
}
window.closeWGModal = closeWGModal;

async function _createWGClient() {
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
            body: JSON.stringify({ name }),
        });

        if (!resp.ok) {
            const err = await resp.json();
            showToast(`Failed to create client: ${err.message || 'Unknown error'}`, 'error');
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
        document.getElementById('wg-created-dns').textContent = `${data.client.dns_name}.tunnelmesh`;
        document.getElementById('wg-config-text').value = data.config;
    } catch (err) {
        console.error('Failed to create WG client:', err);
        showToast('Failed to create client', 'error');
    }
}
window.createWGClient = _createWGClient;

function _downloadWGConfig() {
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
window.downloadWGConfig = _downloadWGConfig;

async function _toggleWGClient(id, enabled) {
    try {
        const resp = await fetch(`api/wireguard/clients/${id}`, {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ enabled }),
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
window.toggleWGClient = _toggleWGClient;

async function _deleteWGClient(id, name) {
    if (!confirm(`Delete WireGuard peer "${name}"?`)) {
        return;
    }

    try {
        const resp = await fetch(`api/wireguard/clients/${id}`, {
            method: 'DELETE',
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
window.deleteWGClient = _deleteWGClient;

// ============= PACKET FILTER FUNCTIONS =============

// Populate the peer dropdown for filter rules
function populateFilterPeerSelect(peers) {
    if (!dom.filterPeerSelect) return;

    // Build new peer list for comparison - skip rebuild if unchanged
    const newPeerList = peers.map((p) => p.name).join(',');
    const currentOptions = Array.from(dom.filterPeerSelect.options);
    const currentPeerList = currentOptions.map((o) => o.value).join(',');

    if (newPeerList === currentPeerList) {
        // Peer list unchanged, skip rebuild to avoid disrupting user interaction
        return;
    }

    const currentValue = dom.filterPeerSelect.value;
    dom.filterPeerSelect.innerHTML = '';

    peers.forEach((peer) => {
        const opt = document.createElement('option');
        opt.value = peer.name;
        opt.textContent = peer.name;
        dom.filterPeerSelect.appendChild(opt);
    });

    // Restore selection if it still exists, otherwise use global selection or first peer
    if (currentValue && peers.some((p) => p.name === currentValue)) {
        dom.filterPeerSelect.value = currentValue;
    } else if (state.selectedNodeId && peers.some((p) => p.name === state.selectedNodeId)) {
        dom.filterPeerSelect.value = state.selectedNodeId;
        loadFilterRules();
    } else if (peers.length > 0 && !dom.filterPeerSelect.value) {
        dom.filterPeerSelect.value = peers[0].name;
        loadFilterRules();
    }
}

// Load filter rules for selected peer
async function loadFilterRules() {
    const peerName = dom.filterPeerSelect?.value;

    if (!peerName) {
        if (dom.filterRulesBody) dom.filterRulesBody.innerHTML = '';
        if (dom.noFilterRules) dom.noFilterRules.style.display = 'block';
        if (dom.filterInfo) dom.filterInfo.style.display = 'none';
        return;
    }

    try {
        const resp = await fetch(`api/filter/rules?peer=${encodeURIComponent(peerName)}`);
        if (!resp.ok) {
            showToast('Failed to load filter rules', 'error');
            return;
        }

        const data = await resp.json();
        renderFilterRules(data);
    } catch (err) {
        console.error('Failed to load filter rules:', err);
        showToast('Failed to load filter rules', 'error');
    }
}
window.loadFilterRules = loadFilterRules;

// Render filter rules table
function renderFilterRules(data) {
    if (!dom.filterRulesBody) return;

    // Show default policy
    if (dom.filterInfo) {
        dom.filterInfo.style.display = 'block';
    }
    if (dom.filterDefaultPolicy) {
        const policyText = data.default_deny ? 'deny' : 'allow';
        const modeText = data.default_deny ? 'allowlist mode' : 'blocklist mode';
        dom.filterDefaultPolicy.innerHTML = `<span class="action-badge ${policyText}">${policyText}</span> <span class="text-muted">(${modeText})</span>`;
    }

    if (!data.rules || data.rules.length === 0) {
        dom.filterRulesBody.innerHTML = '';
        if (dom.filterTempWarning) dom.filterTempWarning.style.display = 'none';
        if (dom.noFilterRules) {
            dom.noFilterRules.style.display = 'block';
            // Show error message if peer query failed
            if (data.error) {
                dom.noFilterRules.innerHTML = `<span class="text-warning">⚠ ${data.error}</span>`;
            } else {
                dom.noFilterRules.textContent = 'No filter rules configured for this peer.';
            }
        }
        return;
    }

    if (dom.noFilterRules) dom.noFilterRules.style.display = 'none';

    // Check for temporary rules and show warning if any exist (but not if S3 is enabled)
    const hasTemporaryRules = data.rules.some((r) => r.source === 'temporary');
    const shouldShowWarning = hasTemporaryRules && !data.s3_enabled;
    if (dom.filterTempWarning) {
        dom.filterTempWarning.style.display = shouldShowWarning ? 'block' : 'none';
    }

    // Sort rules: coordinator first, then config, temporary, service
    const sourceOrder = { coordinator: 0, config: 1, temporary: 2, service: 3 };
    const sortedRules = [...data.rules].sort((a, b) => {
        const orderA = sourceOrder[a.source] ?? 99;
        const orderB = sourceOrder[b.source] ?? 99;
        if (orderA !== orderB) return orderA - orderB;
        // Within same source, sort by port
        return a.port - b.port;
    });

    dom.filterRulesBody.innerHTML = sortedRules
        .map((rule) => {
            const sourcePeerDisplay = rule.source_peer ? rule.source_peer : '<span class="text-muted">Any</span>';
            const sourcePeerEscaped = rule.source_peer || '';
            const expiresDisplay =
                rule.expires === 0
                    ? '<span class="text-muted">Permanent</span>'
                    : TM.format.formatExpiry(new Date(rule.expires * 1000));
            return `
        <tr>
            <td>${rule.port}</td>
            <td>${rule.protocol.toUpperCase()}</td>
            <td><span class="action-badge ${rule.action}">${rule.action}</span></td>
            <td>${sourcePeerDisplay}</td>
            <td>${rule.source}</td>
            <td>${expiresDisplay}</td>
            <td>
                ${
                    rule.source === 'temporary'
                        ? `<button class="btn-danger" onclick="removeFilterRule('${data.peer}', ${rule.port}, '${rule.protocol}', '${sourcePeerEscaped}')">Remove</button>`
                        : '<span class="text-muted">-</span>'
                }
            </td>
        </tr>
    `;
        })
        .join('');
}

// Populate the source peer dropdown in the filter modal
function populateSourcePeerSelect() {
    const select = document.getElementById('filter-rule-source-peer');
    if (!select) return;

    select.innerHTML = '<option value="">Any peer (global rule)</option>';

    // Add all known peers
    const peers = state.currentPeers || [];
    const sortedPeers = [...peers].sort((a, b) => a.name.localeCompare(b.name));
    sortedPeers.forEach((peer) => {
        const opt = document.createElement('option');
        opt.value = peer.name;
        opt.textContent = `${peer.name} (${peer.mesh_ip})`;
        select.appendChild(opt);
    });
}

// Populate the destination peer dropdown in the filter modal
function populateDestPeerSelect() {
    const select = document.getElementById('filter-rule-dest-peer');
    if (!select) return;

    select.innerHTML = '<option value="__all__">All peers</option>';

    // Add all known peers
    const peers = state.currentPeers || [];
    const sortedPeers = [...peers].sort((a, b) => a.name.localeCompare(b.name));
    sortedPeers.forEach((peer) => {
        const opt = document.createElement('option');
        opt.value = peer.name;
        opt.textContent = `${peer.name} (${peer.mesh_ip})`;
        select.appendChild(opt);
    });
}

// Open filter rule modal
function openFilterModal() {
    // Populate peer dropdowns
    populateSourcePeerSelect();
    populateDestPeerSelect();
    filterModal.open();
}
window.openFilterModal = openFilterModal;

// Close filter rule modal
function closeFilterModal() {
    filterModal.close();
}
window.closeFilterModal = closeFilterModal;

// Add a filter rule
async function addFilterRule() {
    const destPeer = document.getElementById('filter-rule-dest-peer')?.value || '__all__';
    const port = parseInt(document.getElementById('filter-rule-port')?.value, 10);
    const protocol = document.getElementById('filter-rule-protocol')?.value;
    const action = document.getElementById('filter-rule-action')?.value;
    const sourcePeer = document.getElementById('filter-rule-source-peer')?.value || '';
    const ttl = parseInt(document.getElementById('filter-rule-ttl')?.value || '0', 10);

    if (!port || !protocol || !action) {
        showToast('Please fill in all fields', 'warning');
        return;
    }

    if (port < 1 || port > 65535) {
        showToast('Port must be between 1 and 65535', 'warning');
        return;
    }

    // Prevent self-targeting: a peer can't filter traffic from itself
    if (sourcePeer && destPeer !== '__all__' && sourcePeer === destPeer) {
        showToast('A peer cannot filter traffic from itself', 'warning');
        return;
    }

    // Confirm before pushing to all peers
    if (destPeer === '__all__') {
        const sourceDesc = sourcePeer ? ` from ${sourcePeer}` : '';
        if (!confirm(`Push "${action} ${port}/${protocol.toUpperCase()}${sourceDesc}" to ALL connected peers?`)) {
            return;
        }
    }

    try {
        const resp = await fetch('api/filter/rules', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ peer: destPeer, port, protocol, action, source_peer: sourcePeer, ttl }),
        });

        if (!resp.ok) {
            const err = await resp.json();
            showToast(`Failed to add rule: ${err.message || 'Unknown error'}`, 'error');
            return;
        }

        const sourceDesc = sourcePeer ? ` from ${sourcePeer}` : '';
        const destDesc = destPeer === '__all__' ? 'all peers' : destPeer;
        showToast(
            `Filter rule sent to ${destDesc}: ${action} ${port}/${protocol.toUpperCase()}${sourceDesc}`,
            'success',
        );
        closeFilterModal();
        loadFilterRules();
        events.emit('panelDataChanged', 'filter');
    } catch (err) {
        console.error('Failed to add filter rule:', err);
        showToast('Failed to add filter rule', 'error');
    }
}
window.addFilterRule = addFilterRule;

// Remove a filter rule
async function removeFilterRule(peerName, port, protocol, sourcePeer) {
    const sourceDesc = sourcePeer ? ` from ${sourcePeer}` : '';
    if (!confirm(`Remove filter rule for port ${port}/${protocol.toUpperCase()}${sourceDesc}?`)) {
        return;
    }

    try {
        const resp = await fetch('api/filter/rules', {
            method: 'DELETE',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ peer: peerName, port, protocol, source_peer: sourcePeer || '' }),
        });

        if (!resp.ok) {
            showToast('Failed to remove rule', 'error');
            return;
        }

        showToast('Filter rule removed', 'success');
        loadFilterRules();
        events.emit('panelDataChanged', 'filter');
    } catch (err) {
        console.error('Failed to remove filter rule:', err);
        showToast('Failed to remove filter rule', 'error');
    }
}
window.removeFilterRule = removeFilterRule;

// ============= END PACKET FILTER FUNCTIONS =============

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

// Initialize filter panel
function initFilterPanel() {
    // Subscribe filter to selection events
    events.on('nodeSelected', (nodeId) => {
        if (dom.filterPeerSelect && nodeId) {
            // Check if the peer exists in the dropdown
            const options = Array.from(dom.filterPeerSelect.options);
            if (options.some((opt) => opt.value === nodeId)) {
                dom.filterPeerSelect.value = nodeId;
                loadFilterRules();
            }
        }
    });
}

// Handle filter peer selection change - emit global selection event
function onFilterPeerChange() {
    const peerName = dom.filterPeerSelect?.value;
    if (peerName) {
        selectNode(peerName);
    }
    loadFilterRules();
}
window.onFilterPeerChange = onFilterPeerChange;

// Register built-in panels with the panel system
function registerBuiltinPanels() {
    if (!TM.panel) return;

    const panels = [
        {
            id: 'visualizer',
            sectionId: 'visualizer-section',
            tab: 'mesh',
            title: 'Network Topology',
            category: 'network',
            collapsible: true,
            sortOrder: 10,
        },
        {
            id: 'map',
            sectionId: 'map-section',
            tab: 'mesh',
            title: 'Node Locations',
            category: 'network',
            collapsible: true,
            sortOrder: 20,
        },
        {
            id: 'charts',
            sectionId: 'charts-section',
            tab: 'mesh',
            title: 'Network Activity',
            category: 'network',
            collapsible: true,
            sortOrder: 30,
            onShow: () => {
                // When panel becomes visible after permission load, resize charts once
                console.log('[DEBUG] Charts onShow callback, throughput exists:', !!state.charts.throughput, ', packets exists:', !!state.charts.packets);
                if (state.charts.throughput) {
                    state.charts.throughput.resize();
                    console.log('[DEBUG] Throughput chart resized to:', state.charts.throughput.width, 'x', state.charts.throughput.height);
                }
                if (state.charts.packets) {
                    state.charts.packets.resize();
                    console.log('[DEBUG] Packets chart resized to:', state.charts.packets.width, 'x', state.charts.packets.height);
                }
            },
        },
        {
            id: 'alerts',
            sectionId: 'alerts-section',
            tab: 'mesh',
            title: 'Active Alerts',
            category: 'monitoring',
            collapsible: false,
            sortOrder: 35,
        },
        {
            id: 'peers',
            sectionId: 'peers-section',
            tab: 'mesh',
            title: 'Connected Peers',
            category: 'network',
            collapsible: true,
            sortOrder: 40,
        },
        {
            id: 'logs',
            sectionId: 'logs-section',
            tab: 'mesh',
            title: 'Peer Logs',
            category: 'monitoring',
            collapsible: true,
            resizable: true,
            sortOrder: 50,
        },
        {
            id: 'wireguard',
            sectionId: 'wireguard-section',
            tab: 'mesh',
            title: 'WireGuard Peers',
            category: 'network',
            hasActionButton: true,
            sortOrder: 60,
        },
        {
            id: 'filter',
            sectionId: 'filter-section',
            tab: 'mesh',
            title: 'Packet Filter',
            category: 'admin',
            hasActionButton: true,
            sortOrder: 70,
        },
        { id: 'dns', sectionId: 'dns-section', tab: 'data', title: 'DNS Records', category: 'data', sortOrder: 80 },
        {
            id: 's3',
            sectionId: 's3-section',
            tab: 'app',
            title: 'Object Viewer',
            category: 'storage',
            resizable: true,
            sortOrder: 10,
        },
        {
            id: 'shares',
            sectionId: 'shares-section',
            tab: 'app',
            title: 'Shares',
            category: 'storage',
            hasActionButton: true,
            sortOrder: 20,
        },
        {
            id: 'peers-mgmt',
            sectionId: 'peers-mgmt-section',
            tab: 'data',
            title: 'Peers',
            category: 'admin',
            sortOrder: 30,
        },
        {
            id: 'groups',
            sectionId: 'groups-section',
            tab: 'data',
            title: 'Groups',
            category: 'admin',
            hasActionButton: true,
            sortOrder: 40,
        },
        {
            id: 'bindings',
            sectionId: 'bindings-section',
            tab: 'data',
            title: 'Role Bindings',
            category: 'admin',
            hasActionButton: true,
            sortOrder: 50,
        },
        {
            id: 'docker',
            sectionId: 'docker-section',
            tab: 'app',
            title: 'Docker Containers',
            category: 'admin',
            hasActionButton: true,
            sortOrder: 30,
        },
    ];

    panels.forEach((p) => {
        try {
            TM.panel.registerPanel(p);
        } catch (err) {
            console.warn(`Failed to register panel ${p.id}:`, err);
        }
    });
}

// Load external panels from API
async function loadExternalPanels() {
    if (!TM.panel) return;

    try {
        const resp = await fetch('/api/panels?external=true');
        if (resp.ok) {
            const data = await resp.json();
            if (data.panels) {
                for (const p of data.panels) {
                    TM.panel.registerExternalPanel(p);
                }
            }
        }
    } catch (err) {
        console.warn('Failed to load external panels:', err);
    }
}

// Initialize
document.addEventListener('DOMContentLoaded', async () => {
    // Cache DOM elements first
    initDOMCache();

    // Register panels and initialize panel system
    registerBuiltinPanels();
    await loadExternalPanels();
    if (TM.panel) {
        await TM.panel.init();
    }

    // Initialize tab navigation
    initTabs();

    // Initialize refresh coordinator
    initRefreshCoordinator();

    // Initialize visualizer
    initVisualizer();

    // Initialize map (for geolocation display)
    initMap();

    // Initialize filter panel
    initFilterPanel();

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

    // Check if Loki is available for logs
    checkLokiAvailable();

    // Load peer management (peers, groups, shares)
    checkPeerManagement();

    // Add client button handler
    if (dom.addWgClientBtn) {
        dom.addWgClientBtn.addEventListener('click', showAddWGClientModal);
    }

    // Add filter rule button handler
    if (dom.addFilterRuleBtn) {
        dom.addFilterRuleBtn.addEventListener('click', openFilterModal);
    }

    // Setup modal background click handlers
    wgModal.setupBackgroundClose();
    filterModal.setupBackgroundClose();

    // Setup background click for group modal
    const groupModal = document.getElementById('group-modal');
    if (groupModal) {
        groupModal.addEventListener('click', (e) => {
            if (e.target === groupModal) closeGroupModal();
        });
    }

    // Setup background click for share modal
    const shareModal = document.getElementById('share-modal');
    if (shareModal) {
        shareModal.addEventListener('click', (e) => {
            if (e.target === shareModal) closeShareModal();
        });
    }

    // Setup background click for binding modal
    const bindingModal = document.getElementById('binding-modal');
    if (bindingModal) {
        bindingModal.addEventListener('click', (e) => {
            if (e.target === bindingModal) closeBindingModal();
        });
    }

    // Panel resize handles
    if (dom.logsResizeHandle && dom.logsContainer) {
        initPanelResize(dom.logsResizeHandle, dom.logsContainer);
    }
    if (dom.s3ResizeHandle && dom.s3Content) {
        initPanelResize(dom.s3ResizeHandle, dom.s3Content);
    }

    // Trigger visualizer resize after DOM is fully rendered
    // This ensures the visualizer displays correctly on initial page load
    requestAnimationFrame(() => {
        if (state.visualizer) {
            state.visualizer.resize();
        }
    });

    // Apply initial tab filtering to hide panels not in active tab
    // This must happen BEFORE refresh triggers to avoid showing all panels briefly
    const activeTab = document.querySelector('#main-tabs .tab.active');
    if (activeTab) {
        const tabName = activeTab.dataset.tab;
        // Apply tab filtering immediately
        switchTab(tabName);
    }

    // Trigger initial refresh for the active tab
    // This ensures panels show their content on page load, not just after tab switch
    if (activeTab && TM.refresh) {
        const tabName = activeTab.dataset.tab;
        if (tabName === 'app') {
            TM.refresh.triggerMultiple(PANELS_APP_TAB, { cascade: false });
        } else if (tabName === 'data') {
            TM.refresh.triggerMultiple(PANELS_DATA_TAB, { cascade: false });
        } else if (tabName === 'mesh') {
            // Mesh tab panels are updated via SSE/polling from fetchData(true) called above
            // No explicit refresh trigger needed here
        }
    }
});

// Initialize panel resize functionality (shared by logs and S3 panels)
function initPanelResize(handle, container) {
    const MIN_HEIGHT = 100;
    const MAX_HEIGHT = 800;

    let isResizing = false;
    let startY = 0;
    let startHeight = 0;

    handle.addEventListener('mousedown', (e) => {
        isResizing = true;
        startY = e.clientY;
        startHeight = container.offsetHeight;
        document.body.style.cursor = 'ns-resize';
        document.body.style.userSelect = 'none';
        e.preventDefault();
    });

    document.addEventListener('mousemove', (e) => {
        if (!isResizing) return;
        const delta = e.clientY - startY;
        const newHeight = Math.min(MAX_HEIGHT, Math.max(MIN_HEIGHT, startHeight + delta));
        container.style.height = `${newHeight}px`;
    });

    document.addEventListener('mouseup', () => {
        if (isResizing) {
            isResizing = false;
            document.body.style.cursor = '';
            document.body.style.userSelect = '';
        }
    });

    // Touch support for mobile
    handle.addEventListener(
        'touchstart',
        (e) => {
            isResizing = true;
            startY = e.touches[0].clientY;
            startHeight = container.offsetHeight;
            e.preventDefault();
        },
        { passive: false },
    );

    document.addEventListener(
        'touchmove',
        (e) => {
            if (!isResizing) return;
            const delta = e.touches[0].clientY - startY;
            const newHeight = Math.min(MAX_HEIGHT, Math.max(MIN_HEIGHT, startHeight + delta));
            container.style.height = `${newHeight}px`;
        },
        { passive: true },
    );

    document.addEventListener('touchend', () => {
        isResizing = false;
    });
}

// --- Users, Groups, and Shares Management ---

// Check if S3/peer management is enabled and show sections
async function checkPeerManagement() {
    // Check if S3/peer management is enabled
    // Panel visibility is managed by the panel system, not here
    // Data loading is handled by refresh coordinator when data tab is accessed
    try {
        const resp = await fetch('api/users');
        if (!resp.ok) {
            // S3 not enabled - panels will be hidden by panel system
        }
    } catch (_err) {
        // Peer management not enabled
    }
}

async function fetchPeersMgmt() {
    try {
        const resp = await fetch('api/users');
        if (resp.ok) {
            const peers = await resp.json();
            state.currentPeersMgmt = peers || [];
            renderPeersMgmtTable();
        }
    } catch (err) {
        console.error('Failed to fetch peers:', err);
    }
}

function renderPeersMgmtTable() {
    const tbody = document.getElementById('peers-mgmt-body');
    const noPeers = document.getElementById('no-peers-mgmt');
    const peers = state.currentPeersMgmt;

    if (!peers || peers.length === 0) {
        tbody.innerHTML = '';
        noPeers.style.display = 'block';
        document.getElementById('peers-mgmt-pagination').style.display = 'none';
        return;
    }

    noPeers.style.display = 'none';
    const visiblePeers = peersMgmtPagination.getVisibleItems();
    tbody.innerHTML = visiblePeers
        .map(
            (p) => `
        <tr>
            <td>${escapeHtml(p.name || '-')}</td>
            <td><span class="status-badge ${p.is_service ? 'service' : 'user'}">${p.is_service ? 'Service' : 'Peer'}</span></td>
            <td>${p.groups && p.groups.length > 0 ? p.groups.map((g) => escapeHtml(g)).join(', ') : '-'}</td>
            <td>${p.last_seen ? formatLastSeen(p.last_seen) : '-'}</td>
            <td>${p.is_service ? 'Never' : p.expires_at ? formatExpiry(p.expires_at) : '-'}</td>
            <td><span class="status-badge ${p.expired ? 'expired' : 'active'}">${p.expired ? 'Expired' : 'Active'}</span></td>
        </tr>
    `,
        )
        .join('');

    updateSectionPagination('peers-mgmt', peersMgmtPagination);
}

// Alias for backward compat
function _updatePeersMgmtTable(peers) {
    state.currentPeersMgmt = peers || [];
    renderPeersMgmtTable();
}

async function fetchGroups() {
    try {
        const resp = await fetch('api/groups');
        if (resp.ok) {
            const groups = await resp.json();
            state.currentGroups = groups || [];
            renderGroupsTable();
        }
    } catch (err) {
        console.error('Failed to fetch groups:', err);
    }
}

function renderGroupsTable() {
    const tbody = document.getElementById('groups-body');
    const noGroups = document.getElementById('no-groups');
    const groups = state.currentGroups;

    if (!groups || groups.length === 0) {
        tbody.innerHTML = '';
        noGroups.style.display = 'block';
        document.getElementById('groups-pagination').style.display = 'none';
        return;
    }

    noGroups.style.display = 'none';
    const visibleGroups = groupsPagination.getVisibleItems();
    tbody.innerHTML = visibleGroups
        .map(
            (g) => `
        <tr>
            <td><strong>${escapeHtml(g.name)}</strong></td>
            <td>${escapeHtml(g.description || '-')}</td>
            <td>${g.members ? g.members.length : 0}</td>
            <td>
                ${g.builtin ? '<span class="text-muted" title="Built-in group">Protected</span>' : `<button class="btn-small btn-danger" onclick="deleteGroup('${escapeHtml(g.name)}')">Delete</button>`}
            </td>
        </tr>
    `,
        )
        .join('');

    updateSectionPagination('groups', groupsPagination);
}

function _updateGroupsTable(groups) {
    state.currentGroups = groups || [];
    renderGroupsTable();
}

function openGroupModal() {
    document.getElementById('group-modal').style.display = 'flex';
    document.getElementById('group-name').value = '';
    document.getElementById('group-description').value = '';
    document.getElementById('group-name').focus();
}
window.openGroupModal = openGroupModal;

function closeGroupModal() {
    document.getElementById('group-modal').style.display = 'none';
}
window.closeGroupModal = closeGroupModal;

async function createGroup() {
    const name = document.getElementById('group-name').value.trim();
    const description = document.getElementById('group-description').value.trim();

    if (!name) {
        showToast('Group name is required', 'error');
        return;
    }

    try {
        const resp = await fetch('api/groups', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name, description }),
        });

        if (resp.ok) {
            showToast(`Group "${name}" created`, 'success');
            closeGroupModal();
            TM.refresh.trigger('groups');
            events.emit('panelDataChanged', 'groups');
        } else {
            const data = await resp.json();
            showToast(data.error || 'Failed to create group', 'error');
        }
    } catch (err) {
        showToast(`Failed to create group: ${err.message}`, 'error');
    }
}
window.createGroup = createGroup;

async function deleteGroup(name) {
    if (!confirm(`Delete group "${name}"?`)) return;

    try {
        const resp = await fetch(`api/groups/${name}`, { method: 'DELETE' });
        if (resp.ok) {
            showToast(`Group "${name}" deleted`, 'success');
            TM.refresh.trigger('groups');
            events.emit('panelDataChanged', 'groups');
        } else {
            const data = await resp.json();
            showToast(data.error || 'Failed to delete group', 'error');
        }
    } catch (err) {
        showToast(`Failed to delete group: ${err.message}`, 'error');
    }
}
window.deleteGroup = deleteGroup;

async function fetchShares() {
    try {
        const resp = await fetch('api/shares');
        if (resp.ok) {
            const shares = await resp.json();
            state.currentShares = shares || [];
            renderSharesTable();
        }
    } catch (err) {
        console.error('Failed to fetch shares:', err);
    }
}

function renderSharesTable() {
    const tbody = document.getElementById('shares-body');
    const noShares = document.getElementById('no-shares');
    const shares = state.currentShares;

    if (!shares || shares.length === 0) {
        tbody.innerHTML = '';
        noShares.style.display = 'block';
        document.getElementById('shares-pagination').style.display = 'none';
        return;
    }

    noShares.style.display = 'none';
    const visibleShares = sharesPagination.getVisibleItems();
    tbody.innerHTML = visibleShares
        .map(
            (s) => `
        <tr>
            <td><strong>${escapeHtml(s.name)}</strong></td>
            <td>${escapeHtml(s.description || '-')}</td>
            <td>${escapeHtml(s.owner_name || s.owner || '-')}</td>
            <td>${s.quota_bytes ? formatBytes(s.quota_bytes) : 'Unlimited'}</td>
            <td>${s.created_at ? new Date(s.created_at).toLocaleDateString() : '-'}</td>
            <td>${s.expires_at ? formatExpiry(s.expires_at) : 'Never'}</td>
            <td>
                <button class="btn-small btn-danger" onclick="deleteShare('${escapeHtml(s.name)}')">Delete</button>
            </td>
        </tr>
    `,
        )
        .join('');

    updateSectionPagination('shares', sharesPagination);
}

function _updateSharesTable(shares) {
    state.currentShares = shares || [];
    renderSharesTable();
}

function openShareModal() {
    document.getElementById('share-modal').style.display = 'flex';
    document.getElementById('share-name').value = '';
    document.getElementById('share-description').value = '';
    document.getElementById('share-quota').value = '';
    document.getElementById('share-expires').value = '';
    document.getElementById('share-guest-read').checked = true;
    document.getElementById('share-name').focus();
}
window.openShareModal = openShareModal;

function closeShareModal() {
    document.getElementById('share-modal').style.display = 'none';
}
window.closeShareModal = closeShareModal;

async function createShare() {
    const name = document.getElementById('share-name').value.trim();
    const description = document.getElementById('share-description').value.trim();
    const quotaValue = document.getElementById('share-quota').value.trim();
    const expiresValue = document.getElementById('share-expires').value;
    const guestRead = document.getElementById('share-guest-read').checked;
    // Default to 100 MB if empty, but allow explicit 0 for unlimited
    const quotaMB = quotaValue === '' ? 100 : parseInt(quotaValue, 10) || 0;

    if (!name) {
        showToast('Share name is required', 'error');
        return;
    }

    // Convert MB to bytes (0 means unlimited)
    const quota_bytes = quotaMB > 0 ? quotaMB * 1024 * 1024 : 0;

    // Build request body
    const body = { name, description, quota_bytes, guest_read: guestRead };
    if (expiresValue) {
        body.expires_at = expiresValue; // Send as YYYY-MM-DD, backend will parse
    }

    try {
        const resp = await fetch('api/shares', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body),
        });

        if (resp.ok) {
            showToast(`File share "${name}" created`, 'success');
            closeShareModal();
            // Trigger refresh for shares panel (will cascade to bindings and S3)
            TM.refresh.trigger('shares');
            events.emit('panelDataChanged', 'shares');
        } else {
            const data = await resp.json();
            showToast(data.error || 'Failed to create share', 'error');
        }
    } catch (err) {
        showToast(`Failed to create share: ${err.message}`, 'error');
    }
}
window.createShare = createShare;

async function deleteShare(name) {
    if (!confirm(`Delete file share "${name}"? All files will be deleted.`)) return;

    try {
        const resp = await fetch(`api/shares/${name}`, { method: 'DELETE' });
        if (resp.ok) {
            showToast(`File share "${name}" deleted`, 'success');
            // Trigger refresh for shares panel (will cascade to bindings and S3)
            TM.refresh.trigger('shares');
            events.emit('panelDataChanged', 'shares');
        } else {
            const data = await resp.json();
            showToast(data.error || 'Failed to delete share', 'error');
        }
    } catch (err) {
        showToast(`Failed to delete share: ${err.message}`, 'error');
    }
}
window.deleteShare = deleteShare;

// =====================
// Refresh Coordinator
// =====================

function initRefreshCoordinator() {
    if (!TM.refresh) {
        console.warn('TM.refresh not loaded, panel refresh coordination disabled');
        return;
    }

    // Register all panel refresh functions
    TM.refresh.register('peers', () => fetchData(false));
    TM.refresh.register('wg-clients', fetchWGClients);
    TM.refresh.register('logs', fetchLogs);
    TM.refresh.register('alerts', fetchAlerts);
    TM.refresh.register('filter', loadFilterRules);
    TM.refresh.register('peers-mgmt', fetchPeersMgmt);
    TM.refresh.register('groups', fetchGroups);
    TM.refresh.register('shares', fetchShares);
    TM.refresh.register('bindings', fetchBindings);
    TM.refresh.register('dns', fetchDnsRecords);
    TM.refresh.register('docker', loadDockerContainers);

    // Register S3 explorer refresh (init if needed, then refresh)
    // Use promise chain instead of async/await to avoid blocking
    TM.refresh.register('s3', () => {
        initS3Explorer()
            .then(() => {
                if (TM.s3explorer) {
                    TM.s3explorer.refresh();
                }
            })
            .catch((err) => {
                console.error('Failed to initialize S3 explorer:', err);
            });
    });

    // Define dependencies: when X changes, also refresh Y
    // When shares change, refresh bindings (share creation/deletion modifies bindings)
    // and S3 explorer (shares create S3 buckets)
    TM.refresh.addDependency('shares', ['bindings', 's3']);

    // When bindings change, refresh S3 explorer (permissions might have changed)
    TM.refresh.addDependency('bindings', ['s3']);

    // When peers-mgmt (users) changes, refresh groups and bindings (user changes affect group/role assignments)
    // Note: Removed groups → peers-mgmt to avoid circular dependency
    TM.refresh.addDependency('peers-mgmt', ['groups', 'bindings']);
}

// Tab Navigation
// =====================

function initTabs() {
    const tabs = document.querySelectorAll('#main-tabs .tab');
    tabs.forEach((tab) => {
        tab.addEventListener('click', () => {
            const tabName = tab.dataset.tab;
            switchTab(tabName);
        });
    });

    // Restore tab from URL hash on initial load
    const hash = window.location.hash.slice(1);
    const params = new URLSearchParams(hash);
    const tabName = params.get('tab');

    if (tabName && ['app', 'data', 'mesh'].includes(tabName)) {
        switchTab(tabName, { skipHistory: true });
    }
}

/**
 * Update browser history with tab change
 */
function updateTabHistory(tabName) {
    // Parse current hash to preserve S3 state
    const hash = window.location.hash.slice(1);
    const params = new URLSearchParams(hash);

    // Update tab parameter
    params.set('tab', tabName);

    const newHash = params.toString();
    const url = newHash ? `#${newHash}` : '#';

    window.history.pushState(null, '', url);
}

function switchTab(tabName, options = {}) {
    // Update tab buttons
    document.querySelectorAll('#main-tabs .tab').forEach((t) => {
        t.classList.toggle('active', t.dataset.tab === tabName);
    });

    // Show/hide sections based on their data-tab attribute
    // Panel system controls base visibility (permissions), we just add tab filtering
    // Sections can belong to multiple tabs via space-separated values (e.g., data-tab="app data")
    document.querySelectorAll('section[data-tab]').forEach((section) => {
        const tabs = section.dataset.tab.split(' ');
        const belongsToTab = tabs.includes(tabName);
        // Only show if: (1) belongs to active tab, AND (2) panel system hasn't hidden it
        if (belongsToTab) {
            // Remove tab-based hiding - panel system controls final visibility
            section.classList.remove('tab-hidden');
        } else {
            // Hide because it belongs to a different tab
            section.classList.add('tab-hidden');
        }
    });

    // Handle tab-specific initialization
    if (tabName === 'mesh') {
        // Defer visualizer and map resize until after DOM updates to get correct dimensions
        // Note: Mesh panels (peers, logs, filter, etc.) are updated via SSE/polling
        // from fetchData(), not via refresh coordinator
        requestAnimationFrame(() => {
            if (state.visualizer) {
                state.visualizer.resize();
            }
            if (state.nodeMap) {
                // Use refresh() instead of just invalidateSize() to handle both
                // tile rendering and centering when returning to tab
                state.nodeMap.refresh();
            }
        });
    } else if (tabName === 'data') {
        // Refresh all data panels without cascading (we're already listing all of them)
        TM.refresh.triggerMultiple(PANELS_DATA_TAB, { cascade: false });
    } else if (tabName === 'app') {
        // Refresh app tab panels
        if (TM.refresh) {
            TM.refresh.triggerMultiple(PANELS_APP_TAB, { cascade: false });
        }
    }

    // Update browser history (unless restoring from history)
    if (!options.skipHistory) {
        updateTabHistory(tabName);
    }
}
window.switchTab = switchTab;

// =====================
// Role Bindings
// =====================

async function fetchBindings() {
    try {
        const resp = await fetch('api/bindings');
        if (!resp.ok) return;
        const bindings = await resp.json();
        // Sort by user/group name
        (bindings || []).sort((a, b) => {
            const aName = (a.user_id || a.group_name || '').toLowerCase();
            const bName = (b.user_id || b.group_name || '').toLowerCase();
            return aName.localeCompare(bName);
        });
        state.currentBindings = bindings || [];
        renderBindingsTable();
    } catch (err) {
        console.error('Failed to fetch bindings:', err);
    }
}

function renderBindingsTable() {
    const tbody = document.getElementById('bindings-body');
    const emptyState = document.getElementById('no-bindings');
    const bindings = state.currentBindings;

    if (!bindings || bindings.length === 0) {
        tbody.innerHTML = '';
        emptyState.style.display = 'block';
        document.getElementById('bindings-pagination').style.display = 'none';
        return;
    }

    emptyState.style.display = 'none';
    const visibleBindings = bindingsPagination.getVisibleItems();
    tbody.innerHTML = visibleBindings
        .map(
            (b) => `
        <tr>
            <td>${escapeHtml(b.user_id || b.group_name || '-')}</td>
            <td>${escapeHtml(b.role_name)}</td>
            <td>${escapeHtml(b.bucket_scope || 'All')}</td>
            <td>${escapeHtml(b.object_prefix || '-')}</td>
            <td>${escapeHtml(b.panel_scope || '-')}</td>
            <td>${b.created_at ? TM.format.formatRelativeTime(b.created_at) : '-'}</td>
            <td>
                ${
                    b.protected
                        ? '<span class="text-muted" title="File share owner binding">Protected</span>'
                        : `<button class="btn-small btn-danger" onclick="deleteBinding('${escapeHtml(b.name)}')">Delete</button>`
                }
            </td>
        </tr>
    `,
        )
        .join('');

    updateSectionPagination('bindings', bindingsPagination);
}

// Alias for backward compat
function _renderBindings(bindings) {
    state.currentBindings = bindings || [];
    renderBindingsTable();
}

function openBindingModal() {
    document.getElementById('binding-modal').style.display = 'flex';
    document.getElementById('binding-bucket').value = '';
    document.getElementById('binding-prefix').value = '';

    // Populate peer dropdown
    populateBindingPeers();
}
window.openBindingModal = openBindingModal;

async function populateBindingPeers() {
    const select = document.getElementById('binding-peer');
    select.innerHTML = '<option value="">Select a peer...</option>';

    try {
        const resp = await fetch('api/users');
        if (resp.ok) {
            const peers = await resp.json();
            peers.forEach((p) => {
                const opt = document.createElement('option');
                opt.value = p.id;
                opt.textContent = p.name || p.id;
                select.appendChild(opt);
            });
        }
    } catch (err) {
        console.error('Failed to fetch peers for binding:', err);
    }
}

function closeBindingModal() {
    document.getElementById('binding-modal').style.display = 'none';
}
window.closeBindingModal = closeBindingModal;

async function createBinding() {
    const peerId = document.getElementById('binding-peer').value;
    const roleName = document.getElementById('binding-role').value;
    const bucketScope = document.getElementById('binding-bucket').value.trim();
    const objectPrefix = document.getElementById('binding-prefix').value.trim();

    if (!peerId) {
        showToast('Peer is required', 'error');
        return;
    }

    try {
        const resp = await fetch('api/bindings', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                user_id: peerId,
                role_name: roleName,
                bucket_scope: bucketScope,
                object_prefix: objectPrefix,
            }),
        });

        if (resp.ok) {
            showToast('Role binding created', 'success');
            closeBindingModal();
            TM.refresh.trigger('bindings');
            events.emit('panelDataChanged', 'bindings');
        } else {
            const data = await resp.json();
            showToast(data.error || 'Failed to create binding', 'error');
        }
    } catch (err) {
        showToast(`Failed to create binding: ${err.message}`, 'error');
    }
}
window.createBinding = createBinding;

async function deleteBinding(name) {
    if (!confirm('Delete this role binding?')) return;

    try {
        const resp = await fetch(`api/bindings/${name}`, { method: 'DELETE' });
        if (resp.ok) {
            showToast('Role binding deleted', 'success');
            TM.refresh.trigger('bindings');
            events.emit('panelDataChanged', 'bindings');
        } else {
            const data = await resp.json();
            showToast(data.error || 'Failed to delete binding', 'error');
        }
    } catch (err) {
        showToast(`Failed to delete binding: ${err.message}`, 'error');
    }
}
window.deleteBinding = deleteBinding;

// Cleanup on page unload to prevent memory leaks
window.addEventListener('beforeunload', cleanup);

// =====================
// S3 Explorer Integration
// =====================

let s3ExplorerInitialized = false;

async function initS3Explorer() {
    if (s3ExplorerInitialized) return;

    // Check if S3 is available
    try {
        const resp = await fetch('api/s3/buckets');
        if (resp.ok) {
            document.getElementById('s3-section').style.display = 'block';
            if (typeof TM !== 'undefined' && TM.s3explorer) {
                await TM.s3explorer.init();
                s3ExplorerInitialized = true;
            }
        }
    } catch (err) {
        // S3 not available, section stays hidden
        console.log('S3 explorer not available:', err.message);
    }
}

// S3 Explorer global handlers
function s3NewFile() {
    if (TM.s3explorer) TM.s3explorer.openNewModal();
}
window.s3NewFile = s3NewFile;

function s3Upload() {
    if (TM.s3explorer) TM.s3explorer.openUploadDialog();
}
window.s3Upload = s3Upload;

function s3Save() {
    if (TM.s3explorer) TM.s3explorer.saveFile();
}
window.s3Save = s3Save;

function s3Download() {
    if (TM.s3explorer) TM.s3explorer.downloadFile();
}
window.s3Download = s3Download;

function s3Delete() {
    if (TM.s3explorer) TM.s3explorer.deleteFile();
}
window.s3Delete = s3Delete;

function s3ToggleEditorMode() {
    if (TM.s3explorer) TM.s3explorer.toggleEditorMode();
}
window.s3ToggleEditorMode = s3ToggleEditorMode;

function s3CloseFile() {
    if (TM.s3explorer) TM.s3explorer.closeFile();
}
window.s3CloseFile = s3CloseFile;

function s3CreateFile() {
    if (TM.s3explorer) TM.s3explorer.createFile();
}
window.s3CreateFile = s3CreateFile;

function s3CreateFolder() {
    if (TM.s3explorer) TM.s3explorer.createFolder();
}
window.s3CreateFolder = s3CreateFolder;

function closeS3Modal(modalId) {
    if (TM.s3explorer) TM.s3explorer.closeModal(modalId);
}
window.closeS3Modal = closeS3Modal;

function s3HandleFileSelect(event) {
    if (TM.s3explorer) TM.s3explorer.handleFileSelect(event);
}
window.s3HandleFileSelect = s3HandleFileSelect;

function s3ShowMore() {
    if (TM.s3explorer) TM.s3explorer.showMore();
}
window.s3ShowMore = s3ShowMore;

function s3ShowLess() {
    if (TM.s3explorer) TM.s3explorer.showLess();
}
window.s3ShowLess = s3ShowLess;

function s3Rename() {
    if (TM.s3explorer) TM.s3explorer.renameSelected();
}
window.s3Rename = s3Rename;

function s3DeleteSelected() {
    if (TM.s3explorer) TM.s3explorer.deleteSelected();
}
window.s3DeleteSelected = s3DeleteSelected;

function s3ToggleAutosave(enabled) {
    if (TM.s3explorer) TM.s3explorer.setAutosave(enabled);
}
window.s3ToggleAutosave = s3ToggleAutosave;

function s3ToggleFullscreen() {
    if (TM.s3explorer) TM.s3explorer.toggleFullscreen();
}
window.s3ToggleFullscreen = s3ToggleFullscreen;

function s3ToggleView() {
    if (TM.s3explorer) TM.s3explorer.toggleView();
}
window.s3ToggleView = s3ToggleView;
