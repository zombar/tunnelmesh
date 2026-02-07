// Tests for lib/modal.js
import { describe, test, expect, mock, beforeEach } from 'bun:test';
import modal from '../lib/modal.js';

const { createModalController, createFormModalController, confirm } = modal;

describe('createModalController', () => {
    let mockElement;

    beforeEach(() => {
        mockElement = {
            style: { display: 'none' },
            addEventListener: mock(() => {}),
        };
    });

    test('creates controller with required methods', () => {
        const controller = createModalController(mockElement);

        expect(typeof controller.open).toBe('function');
        expect(typeof controller.close).toBe('function');
        expect(typeof controller.toggle).toBe('function');
        expect(typeof controller.isOpen).toBe('function');
        expect(typeof controller.setupBackgroundClose).toBe('function');
        expect(typeof controller.getElement).toBe('function');
    });

    test('open sets display to flex', () => {
        const controller = createModalController(mockElement);
        controller.open();
        expect(mockElement.style.display).toBe('flex');
    });

    test('close sets display to none', () => {
        const controller = createModalController(mockElement);
        controller.open();
        controller.close();
        expect(mockElement.style.display).toBe('none');
    });

    test('toggle switches between open and closed', () => {
        const controller = createModalController(mockElement);

        controller.toggle();
        expect(mockElement.style.display).toBe('flex');

        controller.toggle();
        expect(mockElement.style.display).toBe('none');
    });

    test('isOpen returns correct state', () => {
        const controller = createModalController(mockElement);

        expect(controller.isOpen()).toBe(false);
        controller.open();
        expect(controller.isOpen()).toBe(true);
        controller.close();
        expect(controller.isOpen()).toBe(false);
    });

    test('calls onOpen callback when opening', () => {
        const onOpen = mock(() => {});
        const controller = createModalController(mockElement, { onOpen });

        controller.open();
        expect(onOpen).toHaveBeenCalled();
    });

    test('calls onClose callback when closing', () => {
        const onClose = mock(() => {});
        const controller = createModalController(mockElement, { onClose });

        controller.open();
        controller.close();
        expect(onClose).toHaveBeenCalled();
    });

    test('getElement returns the modal element', () => {
        const controller = createModalController(mockElement);
        expect(controller.getElement()).toBe(mockElement);
    });

    test('setupBackgroundClose adds click listener', () => {
        const controller = createModalController(mockElement);
        controller.setupBackgroundClose();
        expect(mockElement.addEventListener).toHaveBeenCalled();
    });

    test('handles string ID for element', () => {
        // In non-browser environment, getElementById returns null
        const controller = createModalController('test-modal-id');
        expect(controller.getElement()).toBe(null);
    });
});

describe('createFormModalController', () => {
    let mockElement;

    beforeEach(() => {
        mockElement = {
            style: { display: 'none' },
            addEventListener: mock(() => {}),
        };
    });

    test('creates controller with form methods', () => {
        const controller = createFormModalController(mockElement);

        expect(typeof controller.open).toBe('function');
        expect(typeof controller.close).toBe('function');
        expect(typeof controller.getForm).toBe('function');
        expect(typeof controller.submit).toBe('function');
        expect(typeof controller.clearForm).toBe('function');
    });

    test('calls onReset when closing', () => {
        const onReset = mock(() => {});
        const controller = createFormModalController(mockElement, { onReset });

        controller.open();
        controller.close();
        expect(onReset).toHaveBeenCalled();
    });

    test('submit calls onSubmit and closes modal on success', async () => {
        const onSubmit = mock(() => true);
        const controller = createFormModalController(mockElement, { onSubmit });

        controller.open();
        await controller.submit();

        expect(onSubmit).toHaveBeenCalled();
        expect(mockElement.style.display).toBe('none');
    });

    test('submit does not close modal when onSubmit returns false', async () => {
        const onSubmit = mock(() => false);
        const controller = createFormModalController(mockElement, { onSubmit });

        controller.open();
        await controller.submit();

        expect(onSubmit).toHaveBeenCalled();
        expect(mockElement.style.display).toBe('flex');
    });

    test('submit prevents default on event', async () => {
        const event = { preventDefault: mock(() => {}) };
        const controller = createFormModalController(mockElement);

        await controller.submit(event);
        expect(event.preventDefault).toHaveBeenCalled();
    });
});

describe('confirm', () => {
    test('uses window.confirm when available', () => {
        // Save original
        const original = globalThis.window?.confirm;

        // Mock window.confirm
        globalThis.window = globalThis.window || {};
        globalThis.window.confirm = mock(() => true);

        expect(confirm('Test message')).toBe(true);
        expect(globalThis.window.confirm).toHaveBeenCalledWith('Test message');

        // Restore
        if (original) {
            globalThis.window.confirm = original;
        }
    });
});
