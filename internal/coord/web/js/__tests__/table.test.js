// Tests for lib/table.js
import { describe, test, expect } from 'bun:test';
import table from '../lib/table.js';

const { createSparklineSVG, createSparklinePath } = table;

describe('createSparklinePath', () => {
    test('returns empty string for empty data', () => {
        expect(createSparklinePath([], 80, 24, 2, 100)).toBe('');
        expect(createSparklinePath(null, 80, 24, 2, 100)).toBe('');
    });

    test('creates path for single point', () => {
        const path = createSparklinePath([50], 80, 24, 2, 100);
        expect(path).toMatch(/^M\d+\.\d+,\d+\.\d+$/);
    });

    test('creates path for multiple points', () => {
        const path = createSparklinePath([0, 50, 100], 80, 24, 2, 100);
        expect(path).toContain('M');
        expect(path).toContain('L');
    });

    test('scales values correctly', () => {
        // With maxVal=100, value 0 should be at bottom, 100 at top
        const path = createSparklinePath([0, 100], 80, 24, 2, 100);
        const points = path.replace('M', '').split(' L');
        expect(points.length).toBe(2);
    });
});

describe('createSparklineSVG', () => {
    test('returns empty SVG for empty data', () => {
        const svg = createSparklineSVG([], []);
        expect(svg).toContain('<svg');
        expect(svg).toContain('</svg>');
        expect(svg).not.toContain('<path');
    });

    test('returns empty SVG for null data', () => {
        const svg = createSparklineSVG(null, null);
        expect(svg).toContain('<svg');
        expect(svg).not.toContain('<path');
    });

    test('creates SVG with TX and RX paths', () => {
        const svg = createSparklineSVG([1, 2, 3], [4, 5, 6]);
        expect(svg).toContain('<svg');
        expect(svg).toContain('class="tx"');
        expect(svg).toContain('class="rx"');
    });

    test('uses correct viewBox dimensions', () => {
        const svg = createSparklineSVG([1, 2], [3, 4], { width: 100, height: 30 });
        expect(svg).toContain('viewBox="0 0 100 30"');
    });

    test('handles single dataset', () => {
        const svg = createSparklineSVG([1, 2, 3], []);
        expect(svg).toContain('class="tx"');
        expect(svg).toContain('class="rx"');
    });
});
