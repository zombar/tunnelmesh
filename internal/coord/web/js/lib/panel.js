// TunnelMesh Dashboard - Panel System
// UMD pattern for browser + Bun compatibility

(function (root, factory) {
    if (typeof module !== 'undefined' && module.exports) {
        module.exports = factory();
    } else {
        root.TM = root.TM || {};
        root.TM.panel = factory();
    }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    // Panel registry - maps panel IDs to configurations
    const _registry = new Map();

    // User's accessible panels (loaded from API)
    let _permissions = new Set();
    let _isAdmin = false;
    let _initialized = false;
    let _userId = '';

    // Event listeners for panel events
    const _eventListeners = new Map();

    /**
     * Register a panel
     * @param {Object} config - Panel configuration
     * @param {string} config.id - Unique panel identifier
     * @param {string} config.sectionId - DOM section element ID
     * @param {string} config.tab - Tab this panel belongs to ('mesh' | 'data')
     * @param {string} [config.title] - Display title
     * @param {string} [config.category] - Category for grouping
     * @param {boolean} [config.resizable=false] - Has resize handle
     * @param {boolean} [config.collapsible=true] - Can collapse
     * @param {boolean} [config.hasActionButton=false] - Has header action button
     * @param {number} [config.sortOrder=0] - Display order
     * @param {Function} [config.onInit] - Called on first show
     * @param {Function} [config.onShow] - Called when visible
     * @param {Function} [config.onHide] - Called when hidden
     */
    function registerPanel(config) {
        if (!config.id) {
            throw new Error('Panel ID is required');
        }
        _registry.set(config.id, {
            ...config,
            collapsible: config.collapsible !== false,
            sortOrder: config.sortOrder || 0,
            _initialized: false,
            _visible: false,
        });
    }

    /**
     * Unregister a panel
     * @param {string} id - Panel ID
     */
    function unregisterPanel(id) {
        const panel = _registry.get(id);
        panel?.onDestroy?.();
        _registry.delete(id);
    }

    /**
     * Get panel configuration
     * @param {string} id - Panel ID
     * @returns {Object|undefined}
     */
    function getPanel(id) {
        return _registry.get(id);
    }

    /**
     * List all registered panels
     * @returns {Array} Panel configurations
     */
    function listPanels() {
        return Array.from(_registry.values()).sort((a, b) => a.sortOrder - b.sortOrder);
    }

    /**
     * List panels by tab
     * @param {string} tab - Tab name ('mesh' | 'data' | 'app')
     * @returns {Array} Panel configurations
     */
    function listByTab(tab) {
        return listPanels().filter((p) => {
            // Support multiple tabs separated by spaces (e.g., "app data")
            const tabs = p.tab.split(' ');
            return tabs.includes(tab);
        });
    }

    /**
     * Load user permissions from API
     * @returns {Promise<void>}
     */
    async function loadPermissions() {
        try {
            const resp = await fetch('/api/user/permissions');
            if (resp.ok) {
                const data = await resp.json();
                _permissions = new Set(data.panels || []);
                _isAdmin = data.is_admin || false;
                _userId = data.user_id || '';
            } else {
                // Fail secure - no access on error
                _permissions = new Set();
                _isAdmin = false;
            }
        } catch (err) {
            console.error('Failed to load panel permissions:', err);
            // Fail secure - no access on error
            _permissions = new Set();
            _isAdmin = false;
        }
    }

    /**
     * Check if user can view a panel
     * @param {string} panelId - Panel ID
     * @returns {boolean}
     */
    function canView(panelId) {
        if (_isAdmin) return true;
        return _permissions.has(panelId);
    }

    /**
     * Check if current user is admin
     * @returns {boolean}
     */
    function isAdmin() {
        return _isAdmin;
    }

    /**
     * Get current user ID
     * @returns {string}
     */
    function getUserId() {
        return _userId;
    }

    /**
     * Apply visibility to all registered panels
     */
    function applyVisibility() {
        for (const [panelId, config] of _registry) {
            const section = typeof document !== 'undefined' ? document.getElementById(config.sectionId) : null;
            if (!section) continue;

            const allowed = canView(panelId);

            // Hide completely - don't gray out
            section.style.display = allowed ? '' : 'none';
            config._visible = allowed;

            if (allowed && !config._initialized) {
                config._initialized = true;
                if (config.onInit) config.onInit();
            }

            if (allowed && config.onShow) {
                config.onShow();
            } else if (!allowed && config.onHide) {
                config.onHide();
            }
        }
    }

    /**
     * Show a specific panel
     * @param {string} id - Panel ID
     */
    function showPanel(id) {
        const config = _registry.get(id);
        if (!config) return;

        const section = typeof document !== 'undefined' ? document.getElementById(config.sectionId) : null;
        if (section) {
            section.style.display = '';
            config._visible = true;
            if (config.onShow) config.onShow();
        }
    }

    /**
     * Hide a specific panel
     * @param {string} id - Panel ID
     */
    function hidePanel(id) {
        const config = _registry.get(id);
        if (!config) return;

        const section = typeof document !== 'undefined' ? document.getElementById(config.sectionId) : null;
        if (section) {
            section.style.display = 'none';
            config._visible = false;
            if (config.onHide) config.onHide();
        }
    }

    /**
     * Setup collapsible header for a panel
     * @param {HTMLElement} headerEl - Header element
     */
    function setupCollapsible(headerEl) {
        if (!headerEl) return;

        headerEl.classList.add('panel-header');
        headerEl.addEventListener('click', (e) => {
            // Don't collapse if clicking action button
            if (e.target.closest('.panel-action-btn')) return;
            if (e.target.closest('button')) return;
            toggleCollapse(headerEl);
        });
    }

    /**
     * Toggle collapsed state for a header
     * @param {HTMLElement} headerEl - Header element
     */
    function toggleCollapse(headerEl) {
        if (!headerEl) return;

        headerEl.classList.toggle('collapsed');
        const content = headerEl.nextElementSibling;
        if (content?.classList.contains('panel-content')) {
            content.classList.toggle('collapsed');
        }
        // Also support collapsible-content class for backwards compatibility
        if (content?.classList.contains('collapsible-content')) {
            content.classList.toggle('collapsed');
        }
    }

    /**
     * Setup resize handle for a panel
     * @param {HTMLElement} handleEl - Resize handle element
     * @param {HTMLElement} contentEl - Content element to resize
     * @param {Object} [options] - Options
     * @param {number} [options.minHeight=100] - Minimum height
     * @param {number} [options.maxHeight=800] - Maximum height
     * @param {string} [options.storageKey] - localStorage key for persistence
     */
    function setupResizeHandle(handleEl, contentEl, options = {}) {
        if (!handleEl || !contentEl) return;
        if (typeof document === 'undefined') return;

        const { minHeight = 100, maxHeight = 800, storageKey } = options;
        let startY, startHeight;

        handleEl.addEventListener('mousedown', (e) => {
            e.preventDefault();
            startY = e.clientY;
            startHeight = contentEl.offsetHeight;

            document.addEventListener('mousemove', onMouseMove);
            document.addEventListener('mouseup', onMouseUp);
        });

        function onMouseMove(e) {
            const delta = e.clientY - startY;
            const newHeight = Math.min(maxHeight, Math.max(minHeight, startHeight + delta));
            contentEl.style.height = `${newHeight}px`;
        }

        function onMouseUp() {
            document.removeEventListener('mousemove', onMouseMove);
            document.removeEventListener('mouseup', onMouseUp);

            if (storageKey && typeof localStorage !== 'undefined') {
                localStorage.setItem(storageKey, contentEl.offsetHeight);
            }
        }

        // Restore saved height
        if (storageKey && typeof localStorage !== 'undefined') {
            const saved = localStorage.getItem(storageKey);
            if (saved) {
                contentEl.style.height = `${saved}px`;
            }
        }
    }

    /**
     * Initialize the panel system
     * Loads permissions and applies visibility
     * @returns {Promise<void>}
     */
    async function init() {
        if (_initialized) return;
        await loadPermissions();
        applyVisibility();
        _initialized = true;
    }

    // --- External Panel Plugin Support ---

    // Allowed URL schemes for plugin URLs (security)
    const ALLOWED_PLUGIN_SCHEMES = ['https:', 'http:'];

    // Map of panel ID -> expected origin for postMessage validation
    const _pluginOrigins = new Map();

    /**
     * Validate and extract origin from plugin URL
     * @param {string} pluginURL - The plugin URL to validate
     * @returns {string|null} The origin if valid, null otherwise
     */
    function validatePluginURL(pluginURL) {
        try {
            const url = new URL(pluginURL);
            if (!ALLOWED_PLUGIN_SCHEMES.includes(url.protocol)) {
                console.error(
                    `Invalid plugin URL scheme: ${url.protocol} (allowed: ${ALLOWED_PLUGIN_SCHEMES.join(', ')})`,
                );
                return null;
            }
            return url.origin;
        } catch {
            console.error(`Invalid plugin URL: ${pluginURL}`);
            return null;
        }
    }

    /**
     * Register an external (plugin) panel
     * @param {Object} config - Panel configuration from API
     */
    function registerExternalPanel(config) {
        if (!config.id || !config.pluginURL) {
            throw new Error('External panel requires id and pluginURL');
        }

        // Validate plugin URL and extract origin for security
        const origin = validatePluginURL(config.pluginURL);
        if (!origin) {
            throw new Error(`Invalid plugin URL for panel ${config.id}: ${config.pluginURL}`);
        }

        // Store origin for postMessage validation
        _pluginOrigins.set(config.id, origin);

        // Create container for external panel
        const container = createExternalPanelContainer(config);

        registerPanel({
            ...config,
            sectionId: `external-panel-${config.id}`,
            external: true,
            onInit: () => loadExternalPanel(config, container),
        });
    }

    /**
     * Create container element for external panel
     * @param {Object} config - Panel configuration
     * @returns {HTMLElement}
     */
    function createExternalPanelContainer(config) {
        if (typeof document === 'undefined') return null;

        const container = document.getElementById('external-panels');
        if (!container) return null;

        const section = document.createElement('section');
        section.id = `external-panel-${config.id}`;
        section.className = 'panel';
        section.innerHTML = `
            <div class="panel-header section-toggle" onclick="TM.panel.toggleCollapse(this)">
                <h2>${escapeHtml(config.name || config.id)}</h2>
            </div>
            <div class="panel-content collapsible-content">
                <div class="external-panel-frame" id="frame-${config.id}"></div>
            </div>
        `;
        container.appendChild(section);
        return section;
    }

    /**
     * Load external panel content (iframe or script)
     * @param {Object} config - Panel configuration
     * @param {HTMLElement} container - Container element
     */
    function loadExternalPanel(config, container) {
        if (!container || typeof document === 'undefined') return;

        const frameContainer = document.getElementById(`frame-${config.id}`);
        if (!frameContainer) return;

        if (config.pluginType === 'script') {
            // Load as script
            const script = document.createElement('script');
            script.src = config.pluginURL;
            script.dataset.panelId = config.id;
            frameContainer.appendChild(script);
        } else {
            // Default: load as iframe
            const iframe = document.createElement('iframe');
            iframe.src = config.pluginURL;
            iframe.style.width = '100%';
            iframe.style.border = 'none';
            iframe.style.minHeight = '200px';
            iframe.dataset.panelId = config.id;
            frameContainer.appendChild(iframe);

            // Setup postMessage communication
            setupPluginCommunication(config.id, iframe);
        }
    }

    /**
     * Setup postMessage communication with plugin iframe
     * @param {string} panelId - Panel ID
     * @param {HTMLIFrameElement} iframe - Iframe element
     */
    function setupPluginCommunication(panelId, iframe) {
        if (typeof window === 'undefined') return;

        // Get the expected origin for this plugin (validated during registration)
        const expectedOrigin = _pluginOrigins.get(panelId);
        if (!expectedOrigin) {
            console.error(`No origin registered for plugin panel: ${panelId}`);
            return;
        }

        // Send init message when iframe loads
        iframe.addEventListener('load', () => {
            iframe.contentWindow.postMessage(
                {
                    type: 'tunnelmesh:init',
                    panelId,
                    theme: 'dark',
                    user: { id: _userId, isAdmin: _isAdmin },
                },
                expectedOrigin, // Use specific origin instead of wildcard
            );
        });

        // Listen for messages from plugin
        window.addEventListener('message', (event) => {
            // Validate origin matches expected origin for this plugin
            if (event.origin !== expectedOrigin) return;

            if (!event.data || !event.data.type) return;
            if (!event.data.type.startsWith('tunnelmesh:panel:')) return;
            if (event.data.panelId !== panelId) return;

            switch (event.data.type) {
                case 'tunnelmesh:panel:ready':
                    emitPanelEvent(panelId, 'ready', {});
                    break;
                case 'tunnelmesh:panel:resize':
                    iframe.style.height = `${event.data.height}px`;
                    break;
            }
        });
    }

    // --- Panel Events ---

    /**
     * Subscribe to panel events
     * @param {string} panelId - Panel ID
     * @param {string} event - Event name
     * @param {Function} callback - Event handler
     */
    function onPanelEvent(panelId, event, callback) {
        const key = `${panelId}:${event}`;
        if (!_eventListeners.has(key)) {
            _eventListeners.set(key, []);
        }
        _eventListeners.get(key).push(callback);
    }

    /**
     * Emit a panel event
     * @param {string} panelId - Panel ID
     * @param {string} event - Event name
     * @param {*} data - Event data
     */
    function emitPanelEvent(panelId, event, data) {
        const key = `${panelId}:${event}`;
        const listeners = _eventListeners.get(key) || [];
        for (const listener of listeners) {
            try {
                listener(data);
            } catch (err) {
                console.error(`Panel event listener error (${key}):`, err);
            }
        }
    }

    /**
     * Broadcast event to all plugin iframes
     * @param {string} event - Event name
     * @param {*} data - Event data
     */
    function broadcastToPlugins(event, data) {
        if (typeof document === 'undefined') return;

        const iframes = document.querySelectorAll('.external-panel-frame iframe');
        for (const iframe of iframes) {
            try {
                // Get the panel ID from the iframe's data attribute
                const panelId = iframe.dataset.panelId;
                const expectedOrigin = panelId ? _pluginOrigins.get(panelId) : null;

                if (!expectedOrigin) {
                    console.warn(`Cannot broadcast to plugin without known origin: ${panelId}`);
                    continue;
                }

                iframe.contentWindow.postMessage(
                    {
                        type: 'tunnelmesh:event',
                        event,
                        data,
                    },
                    expectedOrigin, // Use specific origin instead of wildcard
                );
            } catch {
                // Ignore cross-origin errors
            }
        }
    }

    // --- Utility ---

    /**
     * Escape HTML entities
     * @param {string} text - Text to escape
     * @returns {string}
     */
    function escapeHtml(text) {
        if (typeof document !== 'undefined') {
            const div = document.createElement('div');
            div.textContent = text;
            return div.innerHTML;
        }
        // Fallback for non-browser
        return String(text)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#039;');
    }

    // Export
    return {
        // Panel management
        registerPanel,
        unregisterPanel,
        getPanel,
        listPanels,
        listByTab,

        // Permissions
        loadPermissions,
        canView,
        isAdmin,
        getUserId,

        // Visibility
        applyVisibility,
        showPanel,
        hidePanel,

        // UI behaviors
        setupCollapsible,
        toggleCollapse,
        setupResizeHandle,

        // Initialization
        init,

        // External panels
        registerExternalPanel,

        // Events
        onPanelEvent,
        emitPanelEvent,
        broadcastToPlugins,
    };
});
