// TunnelMesh Dashboard - Pagination Utilities
// UMD pattern for browser + Bun compatibility

(function (root, factory) {
    if (typeof module !== 'undefined' && module.exports) {
        module.exports = factory();
    } else {
        root.TM = root.TM || {};
        root.TM.pagination = factory();
    }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    const DEFAULT_PAGE_SIZE = 7;

    /**
     * Create a pagination controller
     * @param {Object} config - Configuration object
     * @param {number} [config.pageSize=7] - Number of items per page
     * @param {Function} config.getItems - Function that returns array of items
     * @param {Function} config.getVisibleCount - Function that returns current visible count
     * @param {Function} config.setVisibleCount - Function to set visible count
     * @param {Function} config.onRender - Function to call after state change
     * @returns {Object} Pagination controller
     */
    function createPaginationController(config) {
        const { pageSize = DEFAULT_PAGE_SIZE, getItems, getVisibleCount, setVisibleCount, onRender } = config;

        return {
            /**
             * Show more items
             */
            showMore() {
                const current = getVisibleCount();
                setVisibleCount(current + pageSize);
                if (onRender) onRender();
            },

            /**
             * Reset to first page
             */
            showLess() {
                setVisibleCount(pageSize);
                if (onRender) onRender();
            },

            /**
             * Get visible items (sliced from all items)
             * @returns {Array} Visible items
             */
            getVisibleItems() {
                const items = getItems();
                const count = getVisibleCount();
                return items.slice(0, count);
            },

            /**
             * Get UI state for pagination controls
             * @returns {Object} UI state
             */
            getUIState() {
                const items = getItems();
                const total = items.length;
                const visible = getVisibleCount();
                const shown = Math.min(visible, total);

                return {
                    total,
                    shown,
                    hasMore: total > visible,
                    canShowLess: visible > pageSize,
                    isEmpty: total === 0,
                };
            },

            /**
             * Get page size
             * @returns {number} Page size
             */
            getPageSize() {
                return pageSize;
            },
        };
    }

    /**
     * Update pagination UI elements
     * @param {Object} config - Configuration
     * @param {string} config.paginationId - ID of pagination container
     * @param {string} config.showMoreId - ID of "show more" button
     * @param {string} config.showLessId - ID of "show less" button
     * @param {string} config.shownCountId - ID of shown count element
     * @param {string} config.totalCountId - ID of total count element
     * @param {number} config.totalCount - Total number of items
     * @param {number} config.visibleCount - Number of visible items
     * @param {number} [config.pageSize=7] - Default page size
     */
    function updatePaginationUI(config) {
        const {
            paginationId,
            showMoreId,
            showLessId,
            shownCountId,
            totalCountId,
            totalCount,
            visibleCount,
            pageSize = DEFAULT_PAGE_SIZE,
        } = config;

        // Early return if no document (Bun tests)
        if (typeof document === 'undefined') return;

        const paginationEl = document.getElementById(paginationId);
        const showMoreEl = document.getElementById(showMoreId);
        const showLessEl = document.getElementById(showLessId);

        if (!paginationEl) return;

        const hasMore = totalCount > visibleCount;
        const canShowLess = visibleCount > pageSize;

        if (hasMore || canShowLess) {
            paginationEl.style.display = 'block';
            if (showMoreEl) showMoreEl.style.display = hasMore ? 'inline' : 'none';
            if (showLessEl) showLessEl.style.display = canShowLess ? 'inline' : 'none';
            if (hasMore && shownCountId && totalCountId) {
                const shownEl = document.getElementById(shownCountId);
                const totalEl = document.getElementById(totalCountId);
                if (shownEl) shownEl.textContent = Math.min(visibleCount, totalCount);
                if (totalEl) totalEl.textContent = totalCount;
            }
        } else {
            paginationEl.style.display = 'none';
        }
    }

    // Export
    return {
        DEFAULT_PAGE_SIZE,
        createPaginationController,
        updatePaginationUI,
    };
});
