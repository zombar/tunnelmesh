// TunnelMesh Dashboard - Format Utilities
// UMD pattern for browser + Bun compatibility

(function (root, factory) {
    if (typeof module !== 'undefined' && module.exports) {
        // Bun/Node environment
        module.exports = factory();
    } else {
        // Browser environment
        root.TM = root.TM || {};
        root.TM.format = factory();
    }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    /**
     * Format bytes into human-readable string with units
     * @param {number} bytes - Byte count
     * @returns {string} Formatted string like "1.5 KB"
     */
    function formatBytes(bytes) {
        if (bytes === 0 || bytes === undefined || bytes === null) return '0 B';
        const k = 1024;
        const sizes = ['B', 'KB', 'MB', 'GB'];
        const i = Math.floor(Math.log(Math.abs(bytes)) / Math.log(k));
        const clampedI = Math.min(i, sizes.length - 1);
        return `${parseFloat((bytes / k ** clampedI).toFixed(1))} ${sizes[clampedI]}`;
    }

    /**
     * Format bytes compactly (no space, shorter units)
     * @param {number} bytes - Byte count
     * @returns {string} Formatted string like "1.5K"
     */
    function formatBytesCompact(bytes) {
        if (bytes < 1024) return `${Math.round(bytes)}B`;
        if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)}K`;
        if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)}M`;
        return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)}G`;
    }

    /**
     * Format packet rate
     * @param {number} rate - Packets per second
     * @returns {string} Formatted rate
     */
    function formatRate(rate) {
        if (rate === 0 || rate === undefined || rate === null) return '0';
        return rate.toFixed(1);
    }

    /**
     * Format latency in milliseconds
     * @param {number} ms - Latency in milliseconds
     * @returns {string} Formatted latency like "50 ms" or "1.5 s"
     */
    function formatLatency(ms) {
        if (ms === 0 || ms === undefined || ms === null) return '-';
        if (ms < 1) return '<1 ms';
        if (ms < 1000) return `${Math.round(ms)} ms`;
        return `${(ms / 1000).toFixed(1)} s`;
    }

    /**
     * Format latency compactly (no space)
     * @param {number} ms - Latency in milliseconds
     * @returns {string} Formatted latency like "50ms"
     */
    function formatLatencyCompact(ms) {
        if (ms === 0 || ms === undefined || ms === null) return '-';
        if (ms < 1) return '<1ms';
        if (ms < 1000) return `${Math.round(ms)}ms`;
        return `${(ms / 1000).toFixed(1)}s`;
    }

    /**
     * Format timestamp as relative time
     * @param {string|Date} timestamp - ISO timestamp or Date object
     * @returns {string} Relative time like "5m ago" or "2h ago"
     */
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

    /**
     * Format number with locale-aware separators
     * @param {number} num - Number to format
     * @returns {string} Formatted number
     */
    function formatNumber(num) {
        if (num === undefined || num === null) return '0';
        return num.toLocaleString();
    }

    // Export all functions
    return {
        formatBytes,
        formatBytesCompact,
        formatRate,
        formatLatency,
        formatLatencyCompact,
        formatLastSeen,
        formatNumber,
    };
});
