// Tests for lib/format.js
import { describe, test, expect } from 'bun:test';
import format from '../lib/format.js';

const {
    formatBytes,
    formatBytesCompact,
    formatRate,
    formatLatency,
    formatLatencyCompact,
    formatLastSeen,
    formatNumber,
} = format;

describe('formatBytes', () => {
    test('formats zero bytes', () => {
        expect(formatBytes(0)).toBe('0 B');
    });

    test('handles null and undefined', () => {
        expect(formatBytes(null)).toBe('0 B');
        expect(formatBytes(undefined)).toBe('0 B');
    });

    test('formats bytes', () => {
        expect(formatBytes(512)).toBe('512 B');
        expect(formatBytes(1)).toBe('1 B');
    });

    test('formats kilobytes', () => {
        expect(formatBytes(1024)).toBe('1 KB');
        expect(formatBytes(1536)).toBe('1.5 KB');
        expect(formatBytes(10240)).toBe('10 KB');
    });

    test('formats megabytes', () => {
        expect(formatBytes(1048576)).toBe('1 MB');
        expect(formatBytes(1572864)).toBe('1.5 MB');
    });

    test('formats gigabytes', () => {
        expect(formatBytes(1073741824)).toBe('1 GB');
    });

    test('handles negative values', () => {
        // Should still format, using absolute value for log calculation
        const result = formatBytes(-1024);
        expect(result).toContain('KB');
    });
});

describe('formatBytesCompact', () => {
    test('formats small bytes', () => {
        expect(formatBytesCompact(0)).toBe('0B');
        expect(formatBytesCompact(512)).toBe('512B');
    });

    test('formats kilobytes', () => {
        expect(formatBytesCompact(1024)).toBe('1.0K');
        expect(formatBytesCompact(2048)).toBe('2.0K');
    });

    test('formats megabytes', () => {
        expect(formatBytesCompact(1048576)).toBe('1.0M');
    });

    test('formats gigabytes', () => {
        expect(formatBytesCompact(1073741824)).toBe('1.0G');
    });
});

describe('formatRate', () => {
    test('formats zero rate', () => {
        expect(formatRate(0)).toBe('0');
    });

    test('handles null and undefined', () => {
        expect(formatRate(null)).toBe('0');
        expect(formatRate(undefined)).toBe('0');
    });

    test('formats decimal rates', () => {
        expect(formatRate(1.5)).toBe('1.5');
        expect(formatRate(10.123)).toBe('10.1');
        expect(formatRate(100)).toBe('100.0');
    });
});

describe('formatLatency', () => {
    test('formats zero as dash', () => {
        expect(formatLatency(0)).toBe('-');
    });

    test('handles null and undefined', () => {
        expect(formatLatency(null)).toBe('-');
        expect(formatLatency(undefined)).toBe('-');
    });

    test('formats sub-millisecond', () => {
        expect(formatLatency(0.5)).toBe('<1 ms');
        expect(formatLatency(0.1)).toBe('<1 ms');
    });

    test('formats milliseconds', () => {
        expect(formatLatency(1)).toBe('1 ms');
        expect(formatLatency(50)).toBe('50 ms');
        expect(formatLatency(999)).toBe('999 ms');
    });

    test('formats seconds', () => {
        expect(formatLatency(1000)).toBe('1.0 s');
        expect(formatLatency(1500)).toBe('1.5 s');
        expect(formatLatency(2000)).toBe('2.0 s');
    });
});

describe('formatLatencyCompact', () => {
    test('formats without spaces', () => {
        expect(formatLatencyCompact(0)).toBe('-');
        expect(formatLatencyCompact(0.5)).toBe('<1ms');
        expect(formatLatencyCompact(50)).toBe('50ms');
        expect(formatLatencyCompact(1500)).toBe('1.5s');
    });
});

describe('formatLastSeen', () => {
    test('formats recent time as just now', () => {
        const now = new Date();
        expect(formatLastSeen(now.toISOString())).toBe('Just now');
    });

    test('formats minutes ago', () => {
        const fiveMinAgo = new Date(Date.now() - 5 * 60 * 1000);
        expect(formatLastSeen(fiveMinAgo.toISOString())).toBe('5m ago');
    });

    test('formats hours ago', () => {
        const twoHoursAgo = new Date(Date.now() - 2 * 60 * 60 * 1000);
        expect(formatLastSeen(twoHoursAgo.toISOString())).toBe('2h ago');
    });

    test('formats days as date', () => {
        const twoDaysAgo = new Date(Date.now() - 2 * 24 * 60 * 60 * 1000);
        const result = formatLastSeen(twoDaysAgo.toISOString());
        // Should return a date string, not relative time
        expect(result).not.toContain('ago');
    });
});

describe('formatNumber', () => {
    test('handles null and undefined', () => {
        expect(formatNumber(null)).toBe('0');
        expect(formatNumber(undefined)).toBe('0');
    });

    test('formats numbers with locale separators', () => {
        // This depends on locale, but should be a string representation
        const result = formatNumber(1000);
        expect(typeof result).toBe('string');
        expect(result.length).toBeGreaterThan(0);
    });
});
