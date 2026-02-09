// Docker container orchestration panel

let dockerContainers = [];
let dockerAvailable = true;
let dockerVisibleCount = 7; // Initial page size

// Pagination controller
const dockerPagination = TM.pagination.createPaginationController({
    pageSize: 7,
    getItems: () => dockerContainers,
    getVisibleCount: () => dockerVisibleCount,
    setVisibleCount: (n) => {
        dockerVisibleCount = n;
    },
    onRender: () => renderDockerContainers(),
});

// Load Docker containers from the API
async function loadDockerContainers() {
    try {
        const response = await fetch('/api/docker/containers');

        if (response.status === 503) {
            // Docker not available - hide the entire panel
            dockerAvailable = false;
            hideDockerPanel();
            return;
        }

        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }

        const data = await response.json();
        dockerContainers = data.containers || [];
        dockerAvailable = true;

        renderDockerContainers();
    } catch (err) {
        console.error('Failed to load Docker containers:', err);
        // On error, hide the panel (likely Docker not available)
        dockerAvailable = false;
        hideDockerPanel();
    }
}

// Hide Docker panel when Docker is not available
function hideDockerPanel() {
    const sectionEl = document.getElementById('docker-section');
    if (sectionEl) {
        sectionEl.style.display = 'none';
    }
}

// Render Docker containers table
function renderDockerContainers() {
    const tbody = document.getElementById('docker-containers-body');
    const tableEl = document.getElementById('docker-containers');
    const emptyStateEl = document.getElementById('no-docker-containers');
    const sectionEl = document.getElementById('docker-section');

    if (!tbody) return;

    // Ensure panel is visible
    if (sectionEl) sectionEl.style.display = 'block';

    if (dockerContainers.length === 0) {
        if (tableEl) tableEl.style.display = 'none';
        if (emptyStateEl) emptyStateEl.style.display = 'block';
        updateDockerPagination();
        return;
    }

    if (tableEl) tableEl.style.display = 'table';
    if (emptyStateEl) emptyStateEl.style.display = 'none';

    // Sort by name
    dockerContainers.sort((a, b) => a.name.localeCompare(b.name));

    // Get visible items from pagination controller
    const pageContainers = dockerPagination.getVisibleItems();

    tbody.innerHTML = pageContainers.map(container => {
        const statusBadge = getDockerStatusBadge(container.state);
        const uptime = formatDockerUptime(container.uptime_seconds);
        const cpu = formatDockerPercent(container.cpu_percent);
        const memory = formatDockerMemory(container.memory_bytes, container.memory_percent);
        const disk = formatDockerDisk(container.disk_bytes);
        const ports = formatDockerPorts(container.ports);
        const actions = renderDockerActions(container);

        return `
            <tr>
                <td><strong>${escapeHtml(container.name)}</strong></td>
                <td>${statusBadge}</td>
                <td>${ports}</td>
                <td><code>${escapeHtml(container.network_mode)}</code></td>
                <td>${uptime}</td>
                <td>${cpu}</td>
                <td>${memory}</td>
                <td>${disk}</td>
                <td>${actions}</td>
            </tr>
        `;
    }).join('');

    updateDockerPagination();
}

// Update pagination controls
function updateDockerPagination() {
    const uiState = dockerPagination.getUIState();
    const paginationEl = document.getElementById('docker-pagination');

    if (!paginationEl) return;

    paginationEl.style.display = uiState.isEmpty ? 'none' : 'flex';

    const showMore = document.getElementById('docker-show-more');
    const showLess = document.getElementById('docker-show-less');
    const shownCount = document.getElementById('docker-shown-count');
    const totalCount = document.getElementById('docker-total-count');

    if (showMore) showMore.style.display = uiState.hasMore ? 'inline' : 'none';
    if (showLess) showLess.style.display = uiState.canShowLess ? 'inline' : 'none';
    if (shownCount) shownCount.textContent = uiState.shown;
    if (totalCount) totalCount.textContent = uiState.total;
}

// Pagination functions
function showMoreDocker() {
    dockerPagination.showMore();
}

function showLessDocker() {
    dockerPagination.showLess();
}

// Get status badge HTML for container state
function getDockerStatusBadge(state) {
    const stateMap = {
        'running': '<span class="status-badge online">running</span>',
        'exited': '<span class="status-badge offline">exited</span>',
        'paused': '<span class="status-badge">paused</span>',
        'restarting': '<span class="status-badge">restarting</span>',
        'dead': '<span class="status-badge offline">dead</span>',
    };
    return stateMap[state] || `<span class="status-badge">${escapeHtml(state)}</span>`;
}

// Format uptime in human-readable format
function formatDockerUptime(seconds) {
    if (!seconds || seconds === 0) {
        return '<span style="color: var(--color-text-secondary);">--</span>';
    }

    const days = Math.floor(seconds / 86400);
    const hours = Math.floor((seconds % 86400) / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);

    const parts = [];
    if (days > 0) parts.push(`${days}d`);
    if (hours > 0) parts.push(`${hours}h`);
    if (minutes > 0 || parts.length === 0) parts.push(`${minutes}m`);

    return parts.join(' ');
}

// Format CPU percentage
function formatDockerPercent(percent) {
    if (percent === undefined || percent === null) {
        return '<span style="color: var(--color-text-secondary);">--</span>';
    }
    return `${percent.toFixed(1)}%`;
}

// Format memory usage
function formatDockerMemory(bytes, percent) {
    if (!bytes || bytes === 0) {
        return '<span style="color: var(--color-text-secondary);">--</span>';
    }
    const mb = (bytes / (1024 * 1024)).toFixed(0);
    const pct = percent ? ` (${percent.toFixed(0)}%)` : '';
    return `${mb} MB${pct}`;
}

// Format disk usage
function formatDockerDisk(bytes) {
    if (!bytes || bytes === 0) {
        return '<span style="color: var(--color-text-secondary);">--</span>';
    }
    const gb = (bytes / (1024 * 1024 * 1024));
    if (gb >= 1) {
        return `${gb.toFixed(1)} GB`;
    }
    const mb = (bytes / (1024 * 1024)).toFixed(0);
    return `${mb} MB`;
}

// Format container ports for display
function formatDockerPorts(ports) {
    if (!ports || ports.length === 0) {
        return '<span style="color: var(--color-text-secondary);">none</span>';
    }

    const portStrings = ports.map(p => {
        if (p.host_port === 0) {
            return `${p.container_port}/${p.protocol}`;
        }
        return `${p.host_port}:${p.container_port}/${p.protocol}`;
    });

    if (portStrings.length > 2) {
        const first = portStrings.slice(0, 2).join(', ');
        const count = portStrings.length - 2;
        return `<code>${first}</code> <span style="color: var(--color-text-secondary);">+${count} more</span>`;
    }

    return `<code>${portStrings.join(', ')}</code>`;
}

// Render action buttons for container
function renderDockerActions(container) {
    const isRunning = container.state === 'running';

    if (isRunning) {
        return `
            <button class="btn-icon"
                    onclick="dockerControlContainer('${escapeHtml(container.id)}', 'stop')"
                    title="Stop container">
                ⏸
            </button>
            <button class="btn-icon"
                    onclick="dockerControlContainer('${escapeHtml(container.id)}', 'restart')"
                    title="Restart container">
                ↻
            </button>
        `;
    } else {
        return `
            <button class="btn-icon"
                    onclick="dockerControlContainer('${escapeHtml(container.id)}', 'start')"
                    title="Start container">
                ▶
            </button>
        `;
    }
}

// Control container (start/stop/restart)
async function dockerControlContainer(containerID, action) {
    const actionNames = {
        start: 'Starting',
        stop: 'Stopping',
        restart: 'Restarting'
    };

    const actionName = actionNames[action] || action;

    try {
        showToast(`${actionName} container...`, 'info');

        const response = await fetch(`/api/docker/containers/${containerID}/control`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify({ action }),
        });

        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }

        const result = await response.json();

        if (result.success) {
            showToast(`Container ${action} successful`, 'success');
            // Reload containers after a brief delay
            setTimeout(loadDockerContainers, 1000);
        } else {
            showToast(result.message || `Failed to ${action} container`, 'error');
        }
    } catch (err) {
        console.error(`Failed to ${action} container:`, err);
        showToast(`Failed to ${action} container: ${err.message}`, 'error');
    }
}

// Refresh Docker containers (called from UI button)
function refreshDockerContainers() {
    loadDockerContainers();
}

// Export functions for global scope
window.loadDockerContainers = loadDockerContainers;
window.refreshDockerContainers = refreshDockerContainers;
window.dockerControlContainer = dockerControlContainer;
window.showMoreDocker = showMoreDocker;
window.showLessDocker = showLessDocker;
