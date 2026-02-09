// TunnelMesh Dashboard - Centralized Panel Refresh Coordinator
// UMD pattern for browser + Bun compatibility

(function (root, factory) {
    if (typeof module !== 'undefined' && module.exports) {
        module.exports = factory();
    } else {
        root.TM = root.TM || {};
        root.TM.refresh = factory();
    }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    /**
     * Centralized refresh coordinator for managing panel updates and dependencies
     *
     * Usage:
     *   TM.refresh.register('groups', fetchGroups);
     *   TM.refresh.addDependency('shares', ['bindings', 's3']); // When shares change, refresh bindings and S3
     *   TM.refresh.trigger('shares'); // Refreshes shares, bindings, and S3
     */
    function createRefreshCoordinator() {
        // Map of panel name -> refresh function
        const refreshFunctions = new Map();

        // Map of panel name -> array of dependent panel names
        const dependencies = new Map();

        // Set to track panels currently being refreshed (prevent cycles)
        const refreshing = new Set();

        return {
            /**
             * Register a panel's refresh function
             * @param {string} panel - Panel name (e.g., 'groups', 's3', 'docker')
             * @param {Function} refreshFn - Function to call to refresh the panel
             */
            register(panel, refreshFn) {
                if (typeof refreshFn !== 'function') {
                    console.error(`Refresh function for panel '${panel}' must be a function`);
                    return;
                }
                refreshFunctions.set(panel, refreshFn);
            },

            /**
             * Unregister a panel's refresh function
             * @param {string} panel - Panel name
             */
            unregister(panel) {
                refreshFunctions.delete(panel);
                dependencies.delete(panel);
            },

            /**
             * Define dependencies between panels
             * When a panel changes, its dependents will also refresh
             * @param {string} panel - Panel name
             * @param {string[]} dependents - Array of panel names that depend on this panel
             */
            addDependency(panel, dependents) {
                if (!Array.isArray(dependents)) {
                    dependents = [dependents];
                }
                dependencies.set(panel, dependents);
            },

            /**
             * Trigger a refresh for a panel and all its dependents
             * @param {string} panel - Panel name to refresh
             * @param {Object} options - Refresh options
             * @param {boolean} options.cascade - Whether to refresh dependents (default: true)
             * @param {boolean} options.force - Force refresh even if already refreshing (default: false)
             * @returns {Promise<void>}
             */
            async trigger(panel, options = {}) {
                const { cascade = true, force = false } = options;

                // Prevent infinite loops
                if (!force && refreshing.has(panel)) {
                    return;
                }

                const refreshFn = refreshFunctions.get(panel);
                if (!refreshFn) {
                    console.warn(`No refresh function registered for panel: ${panel}`);
                    return;
                }

                refreshing.add(panel);

                try {
                    // Refresh the panel
                    await refreshFn();

                    // Refresh dependents if cascade is enabled
                    if (cascade) {
                        const dependents = dependencies.get(panel) || [];
                        await Promise.all(dependents.map((dep) => this.trigger(dep, { cascade: true, force: false })));
                    }
                } catch (err) {
                    console.error(`Failed to refresh panel '${panel}':`, err);
                } finally {
                    refreshing.delete(panel);
                }
            },

            /**
             * Trigger refresh for multiple panels
             * @param {string[]} panels - Array of panel names
             * @param {Object} options - Refresh options (same as trigger)
             * @returns {Promise<void>}
             */
            async triggerMultiple(panels, options = {}) {
                await Promise.all(panels.map((panel) => this.trigger(panel, options)));
            },

            /**
             * Get all registered panel names
             * @returns {string[]} Array of panel names
             */
            getPanels() {
                return Array.from(refreshFunctions.keys());
            },

            /**
             * Get dependencies for a panel
             * @param {string} panel - Panel name
             * @returns {string[]} Array of dependent panel names
             */
            getDependencies(panel) {
                return dependencies.get(panel) || [];
            },

            /**
             * Clear all registrations and dependencies
             */
            clear() {
                refreshFunctions.clear();
                dependencies.clear();
                refreshing.clear();
            },
        };
    }

    // Export factory and a default instance
    const defaultCoordinator = createRefreshCoordinator();

    return {
        createRefreshCoordinator,
        // Default instance methods
        register: defaultCoordinator.register.bind(defaultCoordinator),
        unregister: defaultCoordinator.unregister.bind(defaultCoordinator),
        addDependency: defaultCoordinator.addDependency.bind(defaultCoordinator),
        trigger: defaultCoordinator.trigger.bind(defaultCoordinator),
        triggerMultiple: defaultCoordinator.triggerMultiple.bind(defaultCoordinator),
        getPanels: defaultCoordinator.getPanels.bind(defaultCoordinator),
        getDependencies: defaultCoordinator.getDependencies.bind(defaultCoordinator),
        clear: defaultCoordinator.clear.bind(defaultCoordinator),
    };
});
