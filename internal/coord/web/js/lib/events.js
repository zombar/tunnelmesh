// TunnelMesh Dashboard - Event Bus
// UMD pattern for browser + Bun compatibility

(function (root, factory) {
    if (typeof module !== 'undefined' && module.exports) {
        module.exports = factory();
    } else {
        root.TM = root.TM || {};
        root.TM.events = factory();
    }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    /**
     * Create a new event bus instance
     * @returns {Object} Event bus with on, off, emit, once methods
     */
    function createEventBus() {
        const listeners = {};

        return {
            /**
             * Subscribe to an event
             * @param {string} event - Event name
             * @param {Function} fn - Callback function
             */
            on(event, fn) {
                (listeners[event] ||= []).push(fn);
            },

            /**
             * Unsubscribe from an event
             * @param {string} event - Event name
             * @param {Function} fn - Callback function to remove
             */
            off(event, fn) {
                if (listeners[event]) {
                    listeners[event] = listeners[event].filter((f) => f !== fn);
                }
            },

            /**
             * Emit an event to all subscribers
             * @param {string} event - Event name
             * @param {*} data - Data to pass to listeners
             */
            emit(event, data) {
                listeners[event]?.forEach((fn) => {
                    try {
                        fn(data);
                    } catch (err) {
                        console.error(`Event listener error (${event}):`, err);
                    }
                });
            },

            /**
             * Subscribe to an event once (auto-unsubscribe after first call)
             * @param {string} event - Event name
             * @param {Function} fn - Callback function
             */
            once(event, fn) {
                const wrapper = (data) => {
                    this.off(event, wrapper);
                    fn(data);
                };
                this.on(event, wrapper);
            },

            /**
             * Get count of listeners for an event
             * @param {string} event - Event name
             * @returns {number} Number of listeners
             */
            listenerCount(event) {
                return listeners[event]?.length || 0;
            },

            /**
             * Clear all listeners for an event (or all events if no event specified)
             * @param {string} [event] - Event name (optional)
             */
            clear(event) {
                if (event) {
                    delete listeners[event];
                } else {
                    for (const key of Object.keys(listeners)) delete listeners[key];
                }
            },
        };
    }

    // Export factory and a default instance
    const defaultBus = createEventBus();

    return {
        createEventBus,
        // Default instance methods
        on: defaultBus.on.bind(defaultBus),
        off: defaultBus.off.bind(defaultBus),
        emit: defaultBus.emit.bind(defaultBus),
        once: defaultBus.once.bind(defaultBus),
        listenerCount: defaultBus.listenerCount.bind(defaultBus),
        clear: defaultBus.clear.bind(defaultBus),
    };
});
