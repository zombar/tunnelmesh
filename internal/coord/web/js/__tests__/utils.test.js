// Tests for lib/utils.js
import { describe, test, expect } from 'bun:test';
import utils from '../lib/utils.js';

const { CONSTANTS, escapeHtml, debounce, throttle, deepClone, extractRegion } = utils;

describe('CONSTANTS', () => {
    test('has expected constants', () => {
        expect(CONSTANTS.ROWS_PER_PAGE).toBe(7);
        expect(CONSTANTS.POLL_INTERVAL_MS).toBe(10000);
        expect(CONSTANTS.TOAST_DURATION_MS).toBe(4000);
        expect(CONSTANTS.MAX_HISTORY_POINTS).toBe(20);
        expect(CONSTANTS.MAX_CHART_POINTS).toBe(360);
    });
});

describe('escapeHtml', () => {
    test('escapes HTML special characters', () => {
        expect(escapeHtml('<script>')).toBe('&lt;script&gt;');
        expect(escapeHtml('a & b')).toBe('a &amp; b');
        expect(escapeHtml('"quoted"')).toBe('&quot;quoted&quot;');
    });

    test('handles empty and null inputs', () => {
        expect(escapeHtml('')).toBe('');
        expect(escapeHtml(null)).toBe('');
        expect(escapeHtml(undefined)).toBe('');
    });

    test('handles numbers', () => {
        expect(escapeHtml(123)).toBe('123');
    });

    test('handles complex HTML', () => {
        const html = '<div onclick="alert(\'xss\')">test</div>';
        const escaped = escapeHtml(html);
        expect(escaped).toContain('&lt;');
        expect(escaped).toContain('&gt;');
        expect(escaped).not.toContain('<div');
    });
});

describe('debounce', () => {
    test('delays function execution', async () => {
        let callCount = 0;
        const fn = debounce(() => callCount++, 50);

        fn();
        fn();
        fn();

        expect(callCount).toBe(0);

        await new Promise((r) => setTimeout(r, 100));
        expect(callCount).toBe(1);
    });

    test('only calls once after delay', async () => {
        let value = 0;
        const fn = debounce((n) => {
            value = n;
        }, 20);

        fn(1);
        fn(2);
        fn(3);

        await new Promise((r) => setTimeout(r, 50));
        expect(value).toBe(3);
    });
});

describe('throttle', () => {
    test('limits function calls', async () => {
        let callCount = 0;
        const fn = throttle(() => callCount++, 50);

        fn(); // Should execute immediately
        fn(); // Should be throttled
        fn(); // Should be throttled

        expect(callCount).toBe(1);

        await new Promise((r) => setTimeout(r, 60));
        fn(); // Should execute after throttle period
        expect(callCount).toBe(2);
    });
});

describe('deepClone', () => {
    test('clones primitive values', () => {
        expect(deepClone(42)).toBe(42);
        expect(deepClone('hello')).toBe('hello');
        expect(deepClone(null)).toBe(null);
        expect(deepClone(true)).toBe(true);
    });

    test('clones arrays', () => {
        const arr = [1, 2, [3, 4]];
        const cloned = deepClone(arr);

        expect(cloned).toEqual(arr);
        expect(cloned).not.toBe(arr);
        expect(cloned[2]).not.toBe(arr[2]);
    });

    test('clones objects', () => {
        const obj = { a: 1, b: { c: 2 } };
        const cloned = deepClone(obj);

        expect(cloned).toEqual(obj);
        expect(cloned).not.toBe(obj);
        expect(cloned.b).not.toBe(obj.b);
    });

    test('handles nested structures', () => {
        const complex = {
            arr: [1, { nested: true }],
            obj: { deep: { value: 42 } },
        };
        const cloned = deepClone(complex);

        expect(cloned).toEqual(complex);
        expect(cloned.arr[1]).not.toBe(complex.arr[1]);
        expect(cloned.obj.deep).not.toBe(complex.obj.deep);
    });
});

describe('extractRegion', () => {
    test('returns null for missing location', () => {
        expect(extractRegion(null)).toBe(null);
        expect(extractRegion({})).toBe(null);
        expect(extractRegion({ location: null })).toBe(null);
    });

    test('prefers city over region', () => {
        expect(
            extractRegion({
                location: { city: 'New York', region: 'NY', country: 'USA' },
            }),
        ).toBe('New York');
    });

    test('falls back to region', () => {
        expect(
            extractRegion({
                location: { region: 'California', country: 'USA' },
            }),
        ).toBe('California');
    });

    test('falls back to country', () => {
        expect(
            extractRegion({
                location: { country: 'Germany' },
            }),
        ).toBe('Germany');
    });

    test('truncates long names', () => {
        const result = extractRegion({
            location: { city: 'This is a very long city name that should be truncated' },
        });
        expect(result.length).toBeLessThanOrEqual(21); // 18 + '...'
        expect(result).toContain('...');
    });
});
