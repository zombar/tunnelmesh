// TunnelMesh Dashboard - Modal Utilities
// UMD pattern for browser + Bun compatibility

(function (root, factory) {
    if (typeof module !== 'undefined' && module.exports) {
        module.exports = factory();
    } else {
        root.TM = root.TM || {};
        root.TM.modal = factory();
    }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    /**
     * Create a modal controller
     * @param {HTMLElement|string} modalEl - Modal element or ID
     * @param {Object} [options] - Options
     * @param {Function} [options.onOpen] - Callback when modal opens
     * @param {Function} [options.onClose] - Callback when modal closes
     * @param {boolean} [options.closeOnBackgroundClick=true] - Close when clicking background
     * @returns {Object} Modal controller
     */
    function createModalController(modalEl, options = {}) {
        const { onOpen, onClose, closeOnBackgroundClick = true } = options;

        // Resolve element if string ID passed
        const getElement = () => {
            if (typeof modalEl === 'string') {
                return typeof document !== 'undefined' ? document.getElementById(modalEl) : null;
            }
            return modalEl;
        };

        let isOpen = false;

        const controller = {
            /**
             * Open the modal
             */
            open() {
                const el = getElement();
                if (el) {
                    el.style.display = 'flex';
                    isOpen = true;
                    if (onOpen) onOpen();
                }
            },

            /**
             * Close the modal
             */
            close() {
                const el = getElement();
                if (el) {
                    el.style.display = 'none';
                    isOpen = false;
                    if (onClose) onClose();
                }
            },

            /**
             * Toggle modal visibility
             */
            toggle() {
                if (isOpen) {
                    controller.close();
                } else {
                    controller.open();
                }
            },

            /**
             * Check if modal is open
             * @returns {boolean}
             */
            isOpen() {
                return isOpen;
            },

            /**
             * Setup background click to close
             * Call this once after modal element is available
             */
            setupBackgroundClose() {
                if (!closeOnBackgroundClick) return;
                const el = getElement();
                if (el) {
                    el.addEventListener('click', (e) => {
                        if (e.target === el) {
                            controller.close();
                        }
                    });
                }
            },

            /**
             * Get the modal element
             * @returns {HTMLElement|null}
             */
            getElement() {
                return getElement();
            },
        };

        return controller;
    }

    /**
     * Create a form modal controller with form handling
     * @param {HTMLElement|string} modalEl - Modal element or ID
     * @param {Object} config - Configuration
     * @param {string} [config.formId] - ID of form element inside modal
     * @param {Function} [config.onSubmit] - Callback when form is submitted
     * @param {Function} [config.onReset] - Callback to reset form state
     * @param {Object} [config.modalOptions] - Options for modal controller
     * @returns {Object} Form modal controller
     */
    function createFormModalController(modalEl, config = {}) {
        const { formId, onSubmit, onReset, modalOptions = {} } = config;

        // Create base modal controller with reset on close
        const modal = createModalController(modalEl, {
            ...modalOptions,
            onClose: () => {
                if (onReset) onReset();
                if (modalOptions.onClose) modalOptions.onClose();
            },
        });

        return {
            ...modal,

            /**
             * Get the form element
             * @returns {HTMLFormElement|null}
             */
            getForm() {
                if (!formId || typeof document === 'undefined') return null;
                return document.getElementById(formId);
            },

            /**
             * Submit the form
             * @param {Event} [event] - Submit event to prevent default
             */
            async submit(event) {
                if (event) event.preventDefault();
                if (onSubmit) {
                    const result = await onSubmit();
                    // Close modal on successful submit (if result is not false)
                    if (result !== false) {
                        modal.close();
                    }
                }
            },

            /**
             * Clear form inputs
             */
            clearForm() {
                const form = this.getForm();
                if (form) {
                    form.reset();
                }
            },
        };
    }

    /**
     * Show a confirmation dialog
     * Uses native confirm() - can be overridden for custom UI
     * @param {string} message - Confirmation message
     * @returns {boolean} True if confirmed
     */
    function confirm(message) {
        if (typeof window !== 'undefined' && window.confirm) {
            return window.confirm(message);
        }
        // In non-browser environment, default to true
        return true;
    }

    // Export
    return {
        createModalController,
        createFormModalController,
        confirm,
    };
});
