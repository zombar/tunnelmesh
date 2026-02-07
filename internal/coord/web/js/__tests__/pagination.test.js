// Tests for lib/pagination.js
import { describe, test, expect, mock } from 'bun:test';
import pagination from '../lib/pagination.js';

const { DEFAULT_PAGE_SIZE, createPaginationController } = pagination;

describe('DEFAULT_PAGE_SIZE', () => {
    test('is 7', () => {
        expect(DEFAULT_PAGE_SIZE).toBe(7);
    });
});

describe('createPaginationController', () => {
    test('creates controller with required methods', () => {
        const controller = createPaginationController({
            getItems: () => [],
            getVisibleCount: () => 7,
            setVisibleCount: () => {},
        });

        expect(typeof controller.showMore).toBe('function');
        expect(typeof controller.showLess).toBe('function');
        expect(typeof controller.getVisibleItems).toBe('function');
        expect(typeof controller.getUIState).toBe('function');
        expect(typeof controller.getPageSize).toBe('function');
    });

    test('showMore increases visible count', () => {
        let visibleCount = 7;
        const renderFn = mock(() => {});

        const controller = createPaginationController({
            pageSize: 7,
            getItems: () => Array(20).fill({}),
            getVisibleCount: () => visibleCount,
            setVisibleCount: (n) => {
                visibleCount = n;
            },
            onRender: renderFn,
        });

        controller.showMore();

        expect(visibleCount).toBe(14);
        expect(renderFn).toHaveBeenCalled();
    });

    test('showLess resets to page size', () => {
        let visibleCount = 21;
        const renderFn = mock(() => {});

        const controller = createPaginationController({
            pageSize: 7,
            getItems: () => Array(20).fill({}),
            getVisibleCount: () => visibleCount,
            setVisibleCount: (n) => {
                visibleCount = n;
            },
            onRender: renderFn,
        });

        controller.showLess();

        expect(visibleCount).toBe(7);
        expect(renderFn).toHaveBeenCalled();
    });

    test('getVisibleItems returns correct slice', () => {
        const items = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10];

        const controller = createPaginationController({
            pageSize: 3,
            getItems: () => items,
            getVisibleCount: () => 5,
            setVisibleCount: () => {},
        });

        expect(controller.getVisibleItems()).toEqual([1, 2, 3, 4, 5]);
    });

    test('getUIState returns correct state when empty', () => {
        const controller = createPaginationController({
            pageSize: 7,
            getItems: () => [],
            getVisibleCount: () => 7,
            setVisibleCount: () => {},
        });

        const state = controller.getUIState();

        expect(state.isEmpty).toBe(true);
        expect(state.total).toBe(0);
        expect(state.shown).toBe(0);
        expect(state.hasMore).toBe(false);
        expect(state.canShowLess).toBe(false);
    });

    test('getUIState returns correct state with items', () => {
        const controller = createPaginationController({
            pageSize: 7,
            getItems: () => Array(20).fill({}),
            getVisibleCount: () => 7,
            setVisibleCount: () => {},
        });

        const state = controller.getUIState();

        expect(state.isEmpty).toBe(false);
        expect(state.total).toBe(20);
        expect(state.shown).toBe(7);
        expect(state.hasMore).toBe(true);
        expect(state.canShowLess).toBe(false);
    });

    test('getUIState shows canShowLess when showing more than page', () => {
        const controller = createPaginationController({
            pageSize: 7,
            getItems: () => Array(20).fill({}),
            getVisibleCount: () => 14,
            setVisibleCount: () => {},
        });

        const state = controller.getUIState();

        expect(state.hasMore).toBe(true); // 20 > 14
        expect(state.canShowLess).toBe(true); // 14 > 7
    });

    test('getUIState hasMore is false when all shown', () => {
        const controller = createPaginationController({
            pageSize: 7,
            getItems: () => Array(5).fill({}),
            getVisibleCount: () => 7,
            setVisibleCount: () => {},
        });

        const state = controller.getUIState();

        expect(state.hasMore).toBe(false); // 5 <= 7
        expect(state.shown).toBe(5); // min(7, 5)
    });

    test('getPageSize returns configured page size', () => {
        const controller = createPaginationController({
            pageSize: 10,
            getItems: () => [],
            getVisibleCount: () => 10,
            setVisibleCount: () => {},
        });

        expect(controller.getPageSize()).toBe(10);
    });

    test('uses default page size when not specified', () => {
        let visibleCount = 7;

        const controller = createPaginationController({
            getItems: () => Array(20).fill({}),
            getVisibleCount: () => visibleCount,
            setVisibleCount: (n) => {
                visibleCount = n;
            },
        });

        controller.showMore();
        expect(visibleCount).toBe(14); // 7 + 7 default
    });
});
