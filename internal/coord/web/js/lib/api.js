// TunnelMesh Dashboard - API Utilities
// UMD pattern for browser + Bun compatibility

(function (root, factory) {
    if (typeof module !== 'undefined' && module.exports) {
        module.exports = factory();
    } else {
        root.TM = root.TM || {};
        root.TM.api = factory();
    }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    // Callbacks for auth error and toast notifications
    // These should be set by the main app
    let onAuthError = null;
    let onToast = null;

    /**
     * Set the auth error callback
     * @param {Function} fn - Callback to call on 401 response
     */
    function setAuthErrorHandler(fn) {
        onAuthError = fn;
    }

    /**
     * Set the toast notification callback
     * @param {Function} fn - Callback to call for toast messages (message, type)
     */
    function setToastHandler(fn) {
        onToast = fn;
    }

    /**
     * Show toast notification (if handler is set)
     * @param {string} message - Message to show
     * @param {string} type - Toast type (error, warning, success, info)
     */
    function showToast(message, type = 'error') {
        if (onToast) {
            onToast(message, type);
        } else {
            console.log(`[${type.toUpperCase()}] ${message}`);
        }
    }

    /**
     * Unified API fetch with error handling
     * @param {string} url - URL to fetch
     * @param {Object} options - Fetch options plus custom options
     * @param {boolean} options.showToast - Whether to show toast on error (default true)
     * @param {string} options.errorMessage - Custom error message for toast
     * @returns {Promise<{ok: boolean, data?: any, status?: number, error?: Error}>}
     */
    async function apiFetch(url, options = {}) {
        const { showToast: showToastOnError = true, errorMessage, ...fetchOptions } = options;

        try {
            const resp = await fetch(url, fetchOptions);

            if (resp.status === 401) {
                if (onAuthError) {
                    onAuthError();
                }
                return { ok: false, status: 401, error: new Error('Unauthorized') };
            }

            if (!resp.ok) {
                const error = new Error(`HTTP ${resp.status}`);
                if (showToastOnError) {
                    showToast(errorMessage || `Request failed: ${resp.status}`, 'error');
                }
                return { ok: false, status: resp.status, error };
            }

            // Handle empty responses
            const text = await resp.text();
            if (!text) {
                return { ok: true, data: null, status: resp.status };
            }

            try {
                const data = JSON.parse(text);
                return { ok: true, data, status: resp.status };
            } catch {
                // Not JSON, return as text
                return { ok: true, data: text, status: resp.status };
            }
        } catch (err) {
            console.error(`API error (${url}):`, err);
            if (showToastOnError) {
                showToast(errorMessage || 'Network request failed', 'error');
            }
            return { ok: false, error: err };
        }
    }

    /**
     * GET request helper
     * @param {string} url - URL to fetch
     * @param {Object} options - Additional options
     * @returns {Promise<{ok: boolean, data?: any, error?: Error}>}
     */
    async function get(url, options = {}) {
        return apiFetch(url, { method: 'GET', ...options });
    }

    /**
     * POST request helper with JSON body
     * @param {string} url - URL to post to
     * @param {Object} body - JSON body
     * @param {Object} options - Additional options
     * @returns {Promise<{ok: boolean, data?: any, error?: Error}>}
     */
    async function post(url, body, options = {}) {
        return apiFetch(url, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', ...options.headers },
            body: JSON.stringify(body),
            ...options,
        });
    }

    /**
     * PATCH request helper with JSON body
     * @param {string} url - URL to patch
     * @param {Object} body - JSON body
     * @param {Object} options - Additional options
     * @returns {Promise<{ok: boolean, data?: any, error?: Error}>}
     */
    async function patch(url, body, options = {}) {
        return apiFetch(url, {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json', ...options.headers },
            body: JSON.stringify(body),
            ...options,
        });
    }

    /**
     * DELETE request helper
     * @param {string} url - URL to delete
     * @param {Object} body - Optional JSON body
     * @param {Object} options - Additional options
     * @returns {Promise<{ok: boolean, data?: any, error?: Error}>}
     */
    async function del(url, body = null, options = {}) {
        const fetchOptions = { method: 'DELETE', ...options };
        if (body) {
            fetchOptions.headers = { 'Content-Type': 'application/json', ...options.headers };
            fetchOptions.body = JSON.stringify(body);
        }
        return apiFetch(url, fetchOptions);
    }

    // Export
    return {
        setAuthErrorHandler,
        setToastHandler,
        apiFetch,
        get,
        post,
        patch,
        del,
        delete: del, // Alias for 'delete' keyword
    };
});
