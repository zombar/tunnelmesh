// TunnelMesh Dashboard - Table Utilities
// UMD pattern for browser + Bun compatibility

(function (root, factory) {
    if (typeof module !== 'undefined' && module.exports) {
        module.exports = factory();
    } else {
        root.TM = root.TM || {};
        root.TM.table = factory();
    }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    /**
     * Render a table with items
     * @param {Object} config - Configuration
     * @param {HTMLElement} config.tbody - Table body element
     * @param {Array} config.items - All items
     * @param {number} config.visibleCount - Number of items to show
     * @param {HTMLElement} [config.emptyEl] - Element to show when empty
     * @param {Function} config.rowRenderer - Function (item, index) => HTML string
     * @param {Object} [config.paginationConfig] - Config for updatePaginationUI
     * @param {Function} [config.updatePaginationUI] - Pagination update function
     */
    function renderTable(config) {
        const { tbody, items, visibleCount, emptyEl, rowRenderer, paginationConfig, updatePaginationUI } = config;

        if (!tbody) return;

        if (items.length === 0) {
            tbody.innerHTML = '';
            if (emptyEl) emptyEl.style.display = 'block';
            // Hide pagination when empty
            if (paginationConfig && typeof document !== 'undefined') {
                const paginationEl = document.getElementById(paginationConfig.paginationId);
                if (paginationEl) paginationEl.style.display = 'none';
            }
            return;
        }

        if (emptyEl) emptyEl.style.display = 'none';

        const visibleItems = items.slice(0, visibleCount);
        tbody.innerHTML = visibleItems.map(rowRenderer).join('');

        if (paginationConfig && updatePaginationUI) {
            updatePaginationUI({
                ...paginationConfig,
                totalCount: items.length,
                visibleCount,
            });
        }
    }

    /**
     * Create a table renderer with pre-configured settings
     * @param {Object} config - Configuration
     * @param {string} config.tbodyId - ID of table body element
     * @param {string} config.emptyElId - ID of empty state element
     * @param {Object} config.paginationConfig - Pagination UI config
     * @param {Function} config.rowRenderer - Function (item) => HTML string
     * @param {Function} config.getItems - Function to get items array
     * @param {Function} config.getVisibleCount - Function to get visible count
     * @returns {Object} Table renderer with render() method
     */
    function createTableRenderer(config) {
        const { tbodyId, emptyElId, paginationConfig, rowRenderer, getItems, getVisibleCount } = config;

        // Import pagination update function if available
        const updatePaginationUI = typeof TM !== 'undefined' && TM.pagination ? TM.pagination.updatePaginationUI : null;

        return {
            render() {
                // Early return if no document (Bun tests)
                if (typeof document === 'undefined') return;

                const tbody = document.getElementById(tbodyId);
                const emptyEl = emptyElId ? document.getElementById(emptyElId) : null;

                renderTable({
                    tbody,
                    items: getItems(),
                    visibleCount: getVisibleCount(),
                    emptyEl,
                    rowRenderer,
                    paginationConfig,
                    updatePaginationUI,
                });
            },
        };
    }

    /**
     * Create sparkline SVG for dual-line chart (TX/RX)
     * @param {number[]} dataTx - TX data points
     * @param {number[]} dataRx - RX data points
     * @param {Object} [options] - Options
     * @param {number} [options.width=80] - SVG width
     * @param {number} [options.height=24] - SVG height
     * @param {number} [options.padding=2] - Padding
     * @returns {string} SVG HTML string
     */
    function createSparklineSVG(dataTx, dataRx, options = {}) {
        const { width = 80, height = 24, padding = 2 } = options;

        if (!dataTx || !dataTx.length) {
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

    /**
     * Create SVG path for sparkline
     * @param {number[]} data - Data points
     * @param {number} width - SVG width
     * @param {number} height - SVG height
     * @param {number} padding - Padding
     * @param {number} maxVal - Maximum value for scaling
     * @returns {string} SVG path d attribute
     */
    function createSparklinePath(data, width, height, padding, maxVal) {
        if (!data || !data.length) return '';

        const drawWidth = width - padding * 2;
        const drawHeight = height - padding * 2;
        const step = drawWidth / Math.max(data.length - 1, 1);

        const points = data.map((val, i) => {
            const x = padding + i * step;
            const y = padding + drawHeight - (val / maxVal) * drawHeight;
            return `${x.toFixed(1)},${y.toFixed(1)}`;
        });

        return `M${points.join(' L')}`;
    }

    // Export
    return {
        renderTable,
        createTableRenderer,
        createSparklineSVG,
        createSparklinePath,
    };
});
