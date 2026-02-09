// Tests for lib/panel.js
import { describe, test, expect, mock, beforeEach } from 'bun:test';
import panel from '../lib/panel.js';

describe('panel registration', () => {
    beforeEach(() => {
        // Clear registry between tests by re-importing
        // Since we can't easily clear the module state, we just test additive behavior
    });

    test('registerPanel creates panel with required fields', () => {
        panel.registerPanel({
            id: 'test-panel-1',
            sectionId: 'test-section-1',
            tab: 'mesh',
            title: 'Test Panel',
        });

        const p = panel.getPanel('test-panel-1');
        expect(p).toBeDefined();
        expect(p.id).toBe('test-panel-1');
        expect(p.sectionId).toBe('test-section-1');
        expect(p.tab).toBe('mesh');
        expect(p.collapsible).toBe(true); // Default
    });

    test('registerPanel throws without id', () => {
        expect(() => {
            panel.registerPanel({ sectionId: 'test', tab: 'mesh' });
        }).toThrow('Panel ID is required');
    });

    test('unregisterPanel removes panel', () => {
        panel.registerPanel({
            id: 'test-panel-remove',
            sectionId: 'test-section-remove',
            tab: 'data',
        });

        expect(panel.getPanel('test-panel-remove')).toBeDefined();
        panel.unregisterPanel('test-panel-remove');
        expect(panel.getPanel('test-panel-remove')).toBeUndefined();
    });

    test('listPanels returns sorted panels', () => {
        panel.registerPanel({
            id: 'test-panel-z',
            sectionId: 'test-z',
            tab: 'mesh',
            sortOrder: 100,
        });
        panel.registerPanel({
            id: 'test-panel-a',
            sectionId: 'test-a',
            tab: 'mesh',
            sortOrder: 10,
        });

        const panels = panel.listPanels();
        const testPanels = panels.filter((p) => p.id.startsWith('test-panel-'));

        // Panel with lower sortOrder should come first
        const aIndex = testPanels.findIndex((p) => p.id === 'test-panel-a');
        const zIndex = testPanels.findIndex((p) => p.id === 'test-panel-z');
        expect(aIndex).toBeLessThan(zIndex);
    });

    test('listByTab filters by tab', () => {
        panel.registerPanel({
            id: 'test-mesh-panel',
            sectionId: 'mesh-section',
            tab: 'mesh',
        });
        panel.registerPanel({
            id: 'test-data-panel',
            sectionId: 'data-section',
            tab: 'data',
        });

        const meshPanels = panel.listByTab('mesh');
        const dataPanels = panel.listByTab('data');

        expect(meshPanels.some((p) => p.id === 'test-mesh-panel')).toBe(true);
        expect(meshPanels.some((p) => p.id === 'test-data-panel')).toBe(false);
        expect(dataPanels.some((p) => p.id === 'test-data-panel')).toBe(true);
    });
});

describe('canView', () => {
    test('returns false when not initialized', () => {
        // Before any permissions are loaded
        expect(panel.canView('some-panel')).toBe(false);
    });

    test('isAdmin returns false by default', () => {
        expect(panel.isAdmin()).toBe(false);
    });
});

describe('toggleCollapse', () => {
    test('toggles collapsed class on header', () => {
        const mockHeader = {
            classList: {
                _classes: new Set(),
                toggle(cls) {
                    if (this._classes.has(cls)) {
                        this._classes.delete(cls);
                    } else {
                        this._classes.add(cls);
                    }
                },
                contains(cls) {
                    return this._classes.has(cls);
                },
            },
            nextElementSibling: {
                classList: {
                    _classes: new Set(['panel-content']),
                    toggle(cls) {
                        if (this._classes.has(cls)) {
                            this._classes.delete(cls);
                        } else {
                            this._classes.add(cls);
                        }
                    },
                    contains(cls) {
                        return this._classes.has(cls);
                    },
                },
            },
        };

        panel.toggleCollapse(mockHeader);
        expect(mockHeader.classList.contains('collapsed')).toBe(true);
        expect(mockHeader.nextElementSibling.classList.contains('collapsed')).toBe(true);

        panel.toggleCollapse(mockHeader);
        expect(mockHeader.classList.contains('collapsed')).toBe(false);
        expect(mockHeader.nextElementSibling.classList.contains('collapsed')).toBe(false);
    });

    test('handles null element gracefully', () => {
        expect(() => panel.toggleCollapse(null)).not.toThrow();
    });
});

describe('setupResizeHandle', () => {
    test('handles null elements gracefully', () => {
        expect(() => panel.setupResizeHandle(null, null)).not.toThrow();
    });
});

describe('external panels', () => {
    test('registerExternalPanel throws without required fields', () => {
        expect(() => {
            panel.registerExternalPanel({ id: 'test-ext' });
        }).toThrow('External panel requires id and pluginURL');

        expect(() => {
            panel.registerExternalPanel({ pluginURL: 'http://example.com' });
        }).toThrow('External panel requires id and pluginURL');
    });
});

describe('panel events', () => {
    test('onPanelEvent and emitPanelEvent work together', () => {
        const callback = mock(() => {});

        panel.onPanelEvent('test-event-panel', 'ready', callback);
        panel.emitPanelEvent('test-event-panel', 'ready', { data: 'test' });

        expect(callback).toHaveBeenCalledWith({ data: 'test' });
    });

    test('emitPanelEvent handles listener errors gracefully', () => {
        const badCallback = mock(() => {
            throw new Error('Test error');
        });
        const goodCallback = mock(() => {});

        panel.onPanelEvent('test-error-panel', 'test', badCallback);
        panel.onPanelEvent('test-error-panel', 'test', goodCallback);

        // Should not throw
        expect(() => {
            panel.emitPanelEvent('test-error-panel', 'test', {});
        }).not.toThrow();

        // Good callback should still be called
        expect(goodCallback).toHaveBeenCalled();
    });
});

describe('exported API', () => {
    test('exports all required functions', () => {
        expect(typeof panel.registerPanel).toBe('function');
        expect(typeof panel.unregisterPanel).toBe('function');
        expect(typeof panel.getPanel).toBe('function');
        expect(typeof panel.listPanels).toBe('function');
        expect(typeof panel.listByTab).toBe('function');
        expect(typeof panel.loadPermissions).toBe('function');
        expect(typeof panel.canView).toBe('function');
        expect(typeof panel.isAdmin).toBe('function');
        expect(typeof panel.getUserId).toBe('function');
        expect(typeof panel.applyVisibility).toBe('function');
        expect(typeof panel.showPanel).toBe('function');
        expect(typeof panel.hidePanel).toBe('function');
        expect(typeof panel.setupCollapsible).toBe('function');
        expect(typeof panel.toggleCollapse).toBe('function');
        expect(typeof panel.setupResizeHandle).toBe('function');
        expect(typeof panel.init).toBe('function');
        expect(typeof panel.registerExternalPanel).toBe('function');
        expect(typeof panel.onPanelEvent).toBe('function');
        expect(typeof panel.emitPanelEvent).toBe('function');
        expect(typeof panel.broadcastToPlugins).toBe('function');
    });
});
