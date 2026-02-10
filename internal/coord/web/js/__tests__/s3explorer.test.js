// Tests for s3explorer.js utility functions
import { describe, test, expect } from 'bun:test';
import s3explorer from '../s3explorer.js';

const { getItemIcon, getItemDisplayName, getIconSVG, buildItemMetadata, buildOnclickHandler } = s3explorer._test;

describe('getItemIcon', () => {
    test('returns "share" for bucket with fs+ prefix', () => {
        const item = { isBucket: true, name: 'fs+myshare' };
        expect(getItemIcon(item)).toBe('share');
    });

    test('returns "bucket" for regular bucket', () => {
        const item = { isBucket: true, name: 'mybucket' };
        expect(getItemIcon(item)).toBe('bucket');
    });

    test('returns "folder" for folder', () => {
        const item = { isFolder: true, name: 'myfolder' };
        expect(getItemIcon(item)).toBe('folder');
    });

    test('returns "file" for regular file', () => {
        const item = { name: 'myfile.txt' };
        expect(getItemIcon(item)).toBe('file');
    });
});

describe('getItemDisplayName', () => {
    test('strips fs+ prefix from share buckets', () => {
        const item = { isBucket: true, name: 'fs+documents' };
        expect(getItemDisplayName(item)).toBe('documents');
    });

    test('keeps name for regular buckets', () => {
        const item = { isBucket: true, name: 'mybucket' };
        expect(getItemDisplayName(item)).toBe('mybucket');
    });

    test('keeps name for folders', () => {
        const item = { isFolder: true, name: 'myfolder' };
        expect(getItemDisplayName(item)).toBe('myfolder');
    });

    test('keeps name for files', () => {
        const item = { name: 'myfile.txt' };
        expect(getItemDisplayName(item)).toBe('myfile.txt');
    });

    test('handles empty names', () => {
        const item = { name: '' };
        expect(getItemDisplayName(item)).toBe('');
    });
});

describe('getIconSVG', () => {
    test('returns file icon SVG', () => {
        const svg = getIconSVG('file');
        expect(svg).toContain('class="s3-large-icon"');
        expect(svg).toContain('width="64"');
        expect(svg).toContain('height="64"');
        expect(svg).toContain('<path');
    });

    test('returns folder icon SVG', () => {
        const svg = getIconSVG('folder');
        expect(svg).toContain('class="s3-large-icon"');
        expect(svg).toContain('<path');
    });

    test('returns bucket icon SVG', () => {
        const svg = getIconSVG('bucket');
        expect(svg).toContain('class="s3-large-icon"');
        expect(svg).toContain('<path');
    });

    test('returns share icon SVG', () => {
        const svg = getIconSVG('share');
        expect(svg).toContain('class="s3-large-icon"');
        expect(svg).toContain('<path');
    });

    test('falls back to file icon for unknown type', () => {
        const svg = getIconSVG('unknown');
        const fileSvg = getIconSVG('file');
        expect(svg).toBe(fileSvg);
    });
});

describe('buildItemMetadata', () => {
    test('formats file size', () => {
        const item = { size: 1024 };
        const metadata = buildItemMetadata(item);
        expect(metadata).toContain('KB');
    });

    test('formats quota for buckets without size', () => {
        const item = { size: null, quota: 1024 };
        const metadata = buildItemMetadata(item);
        expect(metadata).toContain('/');
        expect(metadata).toContain('KB');
    });

    test('formats date', () => {
        const item = { lastModified: new Date().toISOString() };
        const metadata = buildItemMetadata(item);
        expect(metadata).toContain('Today');
    });

    test('combines size and date with separator', () => {
        const item = {
            size: 1024,
            lastModified: new Date().toISOString(),
        };
        const metadata = buildItemMetadata(item);
        expect(metadata).toContain('•');
        expect(metadata).toContain('KB');
        expect(metadata).toContain('Today');
    });

    test('returns empty string for folders without metadata', () => {
        const item = { isFolder: true, size: null };
        const metadata = buildItemMetadata(item);
        expect(metadata).toBe('');
    });

    test('handles null/undefined values', () => {
        const item = { size: null, lastModified: null };
        const metadata = buildItemMetadata(item);
        expect(metadata).toBe('');
    });
});

describe('buildOnclickHandler', () => {
    test('builds bucket navigation handler', () => {
        const item = { isBucket: true, name: 'mybucket' };
        const handler = buildOnclickHandler(item);
        expect(handler).toContain('TM.s3explorer.navigateTo');
        expect(handler).toContain('mybucket');
        expect(handler).toContain("''");
    });

    test('builds folder navigation handler', () => {
        const item = { isFolder: true, key: 'folder/' };
        const handler = buildOnclickHandler(item);
        expect(handler).toContain('TM.s3explorer.navigateTo');
        expect(handler).toContain('folder/');
    });

    test('builds file open handler', () => {
        const item = { key: 'file.txt' };
        const handler = buildOnclickHandler(item);
        expect(handler).toContain('TM.s3explorer.openFile');
        expect(handler).toContain('file.txt');
    });

    test('escapes special characters in names', () => {
        const item = { isBucket: true, name: "bucket'with'quotes" };
        const handler = buildOnclickHandler(item);
        expect(handler).toContain("\\'");
    });
});
