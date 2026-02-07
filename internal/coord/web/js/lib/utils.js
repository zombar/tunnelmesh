// TunnelMesh Dashboard - Common Utilities
// UMD pattern for browser + Bun compatibility

(function (root, factory) {
    if (typeof module !== 'undefined' && module.exports) {
        module.exports = factory();
    } else {
        root.TM = root.TM || {};
        root.TM.utils = factory();
    }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    // =========================================================================
    // Constants
    // =========================================================================

    const CONSTANTS = {
        HEARTBEAT_INTERVAL_MS: 10000, // 10 seconds between heartbeats
        POLL_INTERVAL_MS: 10000, // Polling fallback interval
        SSE_RETRY_DELAY_MS: 2000, // Base delay for SSE reconnection
        MAX_SSE_RETRIES: 3, // Max SSE reconnection attempts
        ROWS_PER_PAGE: 7, // Default pagination size
        MAX_HISTORY_POINTS: 20, // Max sparkline history points per peer
        MAX_CHART_POINTS: 360, // 1 hour at 10-second intervals
        TOAST_DURATION_MS: 4000, // Toast notification duration
        TOAST_FADE_MS: 300, // Toast fade-out animation duration
        QUANTIZE_INTERVAL_MS: 10000, // Timestamp quantization interval
    };

    // =========================================================================
    // Utility Functions
    // =========================================================================

    /**
     * Escape HTML to prevent XSS
     * Uses DOM-based approach in browser for security, fallback for tests
     * @param {string} text - Text to escape
     * @returns {string} Escaped HTML
     */
    function escapeHtml(text) {
        if (text === null || text === undefined) return '';
        const str = String(text);

        // Use DOM-based escaping in browser (more secure)
        if (typeof document !== 'undefined' && document.createElement) {
            const div = document.createElement('div');
            div.textContent = str;
            return div.innerHTML;
        }

        // Fallback for non-browser environments (Bun tests)
        return str
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#39;');
    }

    /**
     * Debounce a function
     * @param {Function} fn - Function to debounce
     * @param {number} delay - Delay in milliseconds
     * @returns {Function} Debounced function
     */
    function debounce(fn, delay) {
        let timeoutId;
        return function (...args) {
            clearTimeout(timeoutId);
            timeoutId = setTimeout(() => fn.apply(this, args), delay);
        };
    }

    /**
     * Throttle a function
     * @param {Function} fn - Function to throttle
     * @param {number} limit - Minimum time between calls in milliseconds
     * @returns {Function} Throttled function
     */
    function throttle(fn, limit) {
        let inThrottle;
        return function (...args) {
            if (!inThrottle) {
                fn.apply(this, args);
                inThrottle = true;
                setTimeout(() => (inThrottle = false), limit);
            }
        };
    }

    /**
     * Deep clone an object
     * @param {*} obj - Object to clone
     * @returns {*} Cloned object
     */
    function deepClone(obj) {
        if (obj === null || typeof obj !== 'object') return obj;
        if (Array.isArray(obj)) return obj.map(deepClone);
        return Object.fromEntries(Object.entries(obj).map(([k, v]) => [k, deepClone(v)]));
    }

    /**
     * Extract region from peer location data
     * @param {Object} peer - Peer object with location property
     * @returns {string|null} Region string or null
     */
    function extractRegion(peer) {
        if (!peer || !peer.location) return null;
        let region = peer.location.city || peer.location.region || peer.location.country || null;
        if (region && region.length > 20) {
            region = `${region.substring(0, 18)}...`;
        }
        return region;
    }

    // Export
    return {
        CONSTANTS,
        escapeHtml,
        debounce,
        throttle,
        deepClone,
        extractRegion,
    };
});
