// TunnelMesh S3 Explorer
// GitHub-style single-pane file browser

(function (root, factory) {
    if (typeof module !== 'undefined' && module.exports) {
        module.exports = factory();
    } else {
        root.TM = root.TM || {};
        root.TM.s3explorer = factory();
    }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    // =========================================================================
    // State
    // =========================================================================

    const PAGE_SIZE = 7;

    const state = {
        buckets: [],
        currentBucket: null,
        currentPath: '', // Current folder prefix
        currentFile: null, // { bucket, key, content, contentType }
        isDirty: false,
        originalContent: '',
        // Pagination
        currentItems: [], // All items in current view
        visibleCount: PAGE_SIZE,
        // Selection
        selectedItems: new Set(), // Set of item keys/names
        // Permissions
        writable: true, // Whether current bucket is writable
        // Editor options
        autosave: false,
        autosaveTimer: null,
        isFullscreen: false,
        // Quota info
        quota: null,
        // View mode
        viewMode: 'list', // 'list' or 'icon'
        defaultViewByTab: {
            app: 'icon',
            data: 'list'
        }
    };

    // Text file extensions
    const TEXT_EXTENSIONS = new Set([
        'txt',
        'md',
        'json',
        'yaml',
        'yml',
        'js',
        'ts',
        'jsx',
        'tsx',
        'go',
        'py',
        'rb',
        'rs',
        'css',
        'scss',
        'html',
        'htm',
        'xml',
        'sh',
        'bash',
        'zsh',
        'conf',
        'ini',
        'env',
        'toml',
        'cfg',
        'sql',
        'graphql',
        'proto',
        'tf',
        'hcl',
        'makefile',
        'gitignore',
        'dockerignore',
        'log',
        'csv',
    ]);

    const IMAGE_EXTENSIONS = new Set(['png', 'jpg', 'jpeg', 'gif', 'svg', 'webp', 'ico']);

    // =========================================================================
    // Helpers
    // =========================================================================

    function escapeHtml(str) {
        if (!str) return '';
        const div = document.createElement('div');
        div.textContent = str;
        return div.innerHTML;
    }

    // Escape string for use in JavaScript string literals within HTML attributes
    // Prevents XSS when building onclick handlers with user-controlled data
    function escapeJsString(str) {
        if (!str) return '';
        return str
            .replace(/\\/g, '\\\\') // Escape backslashes first
            .replace(/'/g, "\\'") // Escape single quotes
            .replace(/"/g, '\\"') // Escape double quotes
            .replace(/\n/g, '\\n') // Escape newlines
            .replace(/\r/g, '\\r'); // Escape carriage returns
    }

    function formatBytes(bytes) {
        if (bytes === 0) return '0 B';
        const k = 1024;
        const sizes = ['B', 'KB', 'MB', 'GB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return `${parseFloat((bytes / k ** i).toFixed(1))} ${sizes[i]}`;
    }

    function formatDate(isoDate) {
        if (!isoDate) return '-';
        const d = new Date(isoDate);
        const now = new Date();
        const diffMs = now - d;
        const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));

        if (diffDays === 0) return 'Today';
        if (diffDays === 1) return 'Yesterday';
        if (diffDays > 0 && diffDays < 7) return `${diffDays} days ago`;

        return d.toLocaleDateString();
    }

    // Format date for version history - shows relative time + readable timestamp
    // Uses shared TM.format utilities for DRY code
    function formatVersionDate(isoDate) {
        if (!isoDate) return '-';

        // Use shared format utilities if available
        let relative, timestamp;
        if (typeof TM !== 'undefined' && TM.format) {
            relative = TM.format.formatRelativeTime(isoDate);
            timestamp = TM.format.formatDateTime(isoDate);
        } else {
            // Fallback for standalone use
            const d = new Date(isoDate);
            relative = d.toLocaleDateString();
            timestamp = d.toLocaleString();
        }

        return `<span class="s3-version-relative">${relative}</span><span class="s3-version-timestamp">${timestamp}</span>`;
    }

    // Use formatExpiry from TM.format utility
    function formatExpiry(isoDate) {
        if (!isoDate) return '-';
        // Delegate to shared utility (handles null check internally too)
        if (typeof TM !== 'undefined' && TM.format && TM.format.formatExpiry) {
            return TM.format.formatExpiry(isoDate);
        }
        // Fallback if utility not loaded
        return new Date(isoDate).toLocaleDateString();
    }

    function getExtension(filename) {
        const parts = filename.split('.');
        if (parts.length < 2) return '';
        return parts[parts.length - 1].toLowerCase();
    }

    function _isTextFile(filename) {
        const ext = getExtension(filename);
        const name = filename.toLowerCase();
        return TEXT_EXTENSIONS.has(ext) || name === 'dockerfile' || name === 'makefile' || name.startsWith('.');
    }

    function isImageFile(filename) {
        return IMAGE_EXTENSIONS.has(getExtension(filename));
    }

    function getContentType(filename) {
        const ext = getExtension(filename);
        const types = {
            json: 'application/json',
            yaml: 'text/yaml',
            yml: 'text/yaml',
            md: 'text/markdown',
            js: 'application/javascript',
            html: 'text/html',
            css: 'text/css',
            xml: 'application/xml',
        };
        return types[ext] || 'text/plain';
    }

    function showToast(message, type = 'info') {
        if (typeof window.showToast === 'function') {
            window.showToast(message, type);
        } else {
            console.log(`[${type}] ${message}`);
        }
    }

    // Icon rendering utilities
    function getItemIcon(item) {
        if (item.isBucket) {
            const isShare = item.name.startsWith('fs+');
            return isShare ? 'share' : 'bucket';
        }
        if (item.isFolder) return 'folder';
        return 'file';
    }

    function getItemDisplayName(item) {
        if (item.isBucket && item.name.startsWith('fs+')) {
            return item.name.substring(3); // Strip "fs+" prefix for shares
        }
        return item.name;
    }

    function getIconSVG(iconType) {
        const svgs = {
            file: '<svg class="s3-large-icon" width="64" height="64" viewBox="0 0 24 24" fill="currentColor"><path d="M14 2H6c-1.1 0-1.99.9-1.99 2L4 20c0 1.1.89 2 1.99 2H18c1.1 0 2-.9 2-2V8l-6-6zm2 16H8v-2h8v2zm0-4H8v-2h8v2zm-3-5V3.5L18.5 9H13z"/></svg>',
            folder: '<svg class="s3-large-icon" width="64" height="64" viewBox="0 0 24 24" fill="currentColor"><path d="M10 4H4c-1.1 0-1.99.9-1.99 2L2 18c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V8c0-1.1-.9-2-2-2h-8l-2-2z"/></svg>',
            bucket: '<svg class="s3-large-icon" width="64" height="64" viewBox="0 0 24 24" fill="currentColor"><path d="M18.06 23h-12c-.72 0-1.34-.5-1.47-1.2L2 6.2C1.87 5.5 2.42 5 3.14 5h17.72c.72 0 1.27.5 1.14 1.2l-2.59 15.6c-.13.7-.75 1.2-1.47 1.2zM9 9v6h2V9h2V7H7v2h2z"/></svg>',
            share: '<svg class="s3-large-icon" width="64" height="64" viewBox="0 0 24 24" fill="currentColor"><path d="M18 16.08c-.76 0-1.44.3-1.96.77L8.91 12.7c.05-.23.09-.46.09-.7s-.04-.47-.09-.7l7.05-4.11c.54.5 1.25.81 2.04.81 1.66 0 3-1.34 3-3s-1.34-3-3-3-3 1.34-3 3c0 .24.04.47.09.7L8.04 9.81C7.5 9.31 6.79 9 6 9c-1.66 0-3 1.34-3 3s1.34 3 3 3c.79 0 1.5-.31 2.04-.81l7.12 4.16c-.05.21-.08.43-.08.65 0 1.61 1.31 2.92 2.92 2.92 1.61 0 2.92-1.31 2.92-2.92s-1.31-2.92-2.92-2.92z"/></svg>'
        };
        return svgs[iconType] || svgs.file;
    }

    function buildItemMetadata(item) {
        const parts = [];

        // Size/quota
        if (item.size !== null && item.size !== undefined && !item.isFolder) {
            parts.push(formatBytes(item.size));
        } else if (item.quota) {
            parts.push(`${formatBytes(item.size || 0)} / ${formatBytes(item.quota)}`);
        }

        // Date
        if (item.lastModified) {
            parts.push(formatDate(item.lastModified));
        }

        return parts.join(' • ');
    }

    function buildOnclickHandler(item) {
        if (item.isBucket) {
            return `TM.s3explorer.navigateTo('${escapeJsString(item.name)}', '')`;
        }
        if (item.isFolder) {
            return `TM.s3explorer.navigateTo('${escapeJsString(state.currentBucket)}', '${escapeJsString(item.key)}')`;
        }
        return `TM.s3explorer.openFile('${escapeJsString(state.currentBucket)}', '${escapeJsString(item.key)}')`;
    }

    // =========================================================================
    // API Functions
    // =========================================================================

    async function fetchBuckets() {
        try {
            const resp = await fetch('api/s3/buckets');
            if (!resp.ok) return { buckets: [], quota: null };
            const data = await resp.json();
            // Store quota info in state
            state.quota = data.quota || null;
            return data.buckets || [];
        } catch (err) {
            console.error('Failed to fetch buckets:', err);
            return [];
        }
    }

    async function fetchObjects(bucket, prefix = '') {
        try {
            const params = new URLSearchParams({ prefix, delimiter: '/' });
            const resp = await fetch(`api/s3/buckets/${encodeURIComponent(bucket)}/objects?${params}`);
            if (!resp.ok) return [];
            return await resp.json();
        } catch (err) {
            console.error('Failed to fetch objects:', err);
            return [];
        }
    }

    async function getObject(bucket, key) {
        const resp = await fetch(`api/s3/buckets/${encodeURIComponent(bucket)}/objects/${encodeURIComponent(key)}`);
        if (!resp.ok) throw new Error(`Failed to get object: ${resp.status}`);
        return {
            content: await resp.text(),
            contentType: resp.headers.get('Content-Type'),
            size: parseInt(resp.headers.get('Content-Length'), 10) || 0,
        };
    }

    async function putObject(bucket, key, content, contentType) {
        const resp = await fetch(`api/s3/buckets/${encodeURIComponent(bucket)}/objects/${encodeURIComponent(key)}`, {
            method: 'PUT',
            headers: { 'Content-Type': contentType },
            body: content,
        });
        if (!resp.ok) throw new Error(`Failed to save: ${resp.status}`);
        return true;
    }

    async function deleteObject(bucket, key) {
        const resp = await fetch(`api/s3/buckets/${encodeURIComponent(bucket)}/objects/${encodeURIComponent(key)}`, {
            method: 'DELETE',
        });
        return resp.ok || resp.status === 204;
    }

    async function fetchVersions(bucket, key) {
        try {
            const resp = await fetch(
                `api/s3/buckets/${encodeURIComponent(bucket)}/objects/${encodeURIComponent(key)}/versions`,
            );
            if (!resp.ok) return [];
            return await resp.json();
        } catch (err) {
            console.error('Failed to fetch versions:', err);
            return [];
        }
    }

    async function restoreVersion(bucket, key, versionId) {
        const resp = await fetch(
            `api/s3/buckets/${encodeURIComponent(bucket)}/objects/${encodeURIComponent(key)}/restore`,
            {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ version_id: versionId }),
            },
        );
        if (!resp.ok) {
            const err = await resp.json().catch(() => ({}));
            throw new Error(err.error || `Failed to restore: ${resp.status}`);
        }
        return await resp.json();
    }

    // =========================================================================
    // Rendering
    // =========================================================================

    function renderBreadcrumb() {
        const container = document.getElementById('s3-breadcrumb');
        if (!container) return;

        let html = '';

        // Root (buckets view)
        if (!state.currentBucket) {
            html = '<span class="s3-breadcrumb-item current">All</span>';
        } else {
            html = '<span class="s3-breadcrumb-item" onclick="TM.s3explorer.navigateTo(null, \'\')">All</span>';
            html += '<span class="s3-breadcrumb-sep">/</span>';

            // Bucket
            if (!state.currentPath && !state.currentFile) {
                html += `<span class="s3-breadcrumb-item current">${escapeHtml(state.currentBucket)}</span>`;
            } else {
                html += `<span class="s3-breadcrumb-item" onclick="TM.s3explorer.navigateTo('${escapeJsString(state.currentBucket)}', '')">${escapeHtml(state.currentBucket)}</span>`;
            }

            // Path parts
            if (state.currentPath || state.currentFile) {
                const path = state.currentFile ? state.currentFile.key : state.currentPath;
                const parts = path.split('/').filter((p) => p);
                let accum = '';

                for (let i = 0; i < parts.length; i++) {
                    const part = parts[i];
                    accum += `${part}/`;
                    html += '<span class="s3-breadcrumb-sep">/</span>';

                    const isLast = i === parts.length - 1;
                    const isFile = state.currentFile && isLast;

                    if (isLast) {
                        html += `<span class="s3-breadcrumb-item current">${escapeHtml(isFile ? part : part)}</span>`;
                    } else {
                        html += `<span class="s3-breadcrumb-item" onclick="TM.s3explorer.navigateTo('${escapeJsString(state.currentBucket)}', '${escapeJsString(accum)}')">${escapeHtml(part)}</span>`;
                    }
                }
            }
        }

        container.innerHTML = html;
    }

    async function renderFileListing(resetPagination = true) {
        const tbody = document.getElementById('s3-files-body');
        const table = document.getElementById('s3-files');
        const iconGrid = document.getElementById('s3-icons');
        const browser = document.getElementById('s3-browser');
        const viewer = document.getElementById('s3-viewer');
        const preview = document.getElementById('s3-preview');
        const empty = document.getElementById('s3-empty');
        const browseActions = document.getElementById('s3-browse-actions');
        const fileActions = document.getElementById('s3-file-actions');
        const paginationEl = document.getElementById('s3-pagination');

        if (!tbody) return;

        // Hide viewer/preview, show browser
        if (viewer) viewer.style.display = 'none';
        if (preview) preview.style.display = 'none';
        if (browser) browser.style.display = 'block';
        // Show browse actions (hide for read-only buckets), hide file actions
        if (browseActions) browseActions.style.display = state.writable ? 'flex' : 'none';
        if (fileActions) fileActions.style.display = 'none';

        // Clear current file
        state.currentFile = null;
        state.isDirty = false;

        // Reset pagination and selection when navigating to new folder
        if (resetPagination) {
            state.visibleCount = PAGE_SIZE;
            state.selectedItems.clear();
            updateSelectionUI();
        }

        let items = [];

        if (!state.currentBucket) {
            // Show buckets as folders
            const buckets = await fetchBuckets();
            state.buckets = buckets;
            items = buckets.map((b) => ({
                name: b.name,
                isFolder: true,
                isBucket: true,
                size: b.used_bytes || 0,
                quota: b.quota_bytes || 0,
                lastModified: b.created_at,
                expires: null,
                writable: b.writable,
            }));
        } else {
            // Show objects in current path
            const objects = await fetchObjects(state.currentBucket, state.currentPath);

            // Sort: folders first, then files
            objects.sort((a, b) => {
                if (a.is_prefix && !b.is_prefix) return -1;
                if (!a.is_prefix && b.is_prefix) return 1;
                return a.key.localeCompare(b.key);
            });

            items = objects
                .map((obj) => {
                    if (obj.is_prefix) {
                        const name = obj.key.replace(state.currentPath, '').replace(/\/$/, '');
                        return {
                            name,
                            isFolder: true,
                            key: obj.key,
                            size: null,
                            quota: null,
                            lastModified: null,
                            expires: null,
                        };
                    } else {
                        const name = obj.key.replace(state.currentPath, '');
                        return {
                            name,
                            isFolder: false,
                            key: obj.key,
                            size: obj.size,
                            quota: null,
                            owner: obj.owner,
                            lastModified: obj.last_modified,
                            expires: obj.expires,
                            tombstonedAt: obj.tombstoned_at,
                        };
                    }
                })
                .filter((item) => item.name && item.name !== '.folder');
        }

        // Store items for pagination
        state.currentItems = items;

        // Toggle quota column visibility (only show for bucket list, not files)
        const quotaCol = document.querySelector('.s3-col-quota');
        if (quotaCol) {
            quotaCol.style.display = state.currentBucket ? 'none' : '';
        }

        // Toggle owner column visibility (only show for files within bucket, not bucket list)
        const ownerCol = document.querySelector('.s3-col-owner');
        if (ownerCol) {
            ownerCol.style.display = state.currentBucket ? '' : 'none';
        }

        // Always render breadcrumb for navigation
        renderBreadcrumb();

        if (items.length === 0) {
            tbody.innerHTML = '';
            if (iconGrid) iconGrid.innerHTML = '';
            if (empty) empty.style.display = 'block';
            if (paginationEl) paginationEl.style.display = 'none';
            return;
        }

        if (empty) empty.style.display = 'none';

        // Only show visible items
        const visibleItems = items.slice(0, state.visibleCount);

        // Render based on view mode
        if (state.viewMode === 'icon') {
            // Hide table, show icon grid
            if (table) table.style.display = 'none';
            if (iconGrid) {
                iconGrid.style.display = 'grid';
                renderIconGrid(visibleItems);
            }
        } else {
            // Show table, hide icon grid
            if (table) table.style.display = 'table';
            if (iconGrid) iconGrid.style.display = 'none';

            tbody.innerHTML = visibleItems
            .map((item, index) => {
                const icon = item.isFolder
                    ? '<svg class="s3-icon s3-icon-folder" width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M10 4H4c-1.1 0-1.99.9-1.99 2L2 18c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V8c0-1.1-.9-2-2-2h-8l-2-2z"/></svg>'
                    : '<svg class="s3-icon s3-icon-file" width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M14 2H6c-1.1 0-1.99.9-1.99 2L4 20c0 1.1.89 2 1.99 2H18c1.1 0 2-.9 2-2V8l-6-6zm2 16H8v-2h8v2zm0-4H8v-2h8v2zm-3-5V3.5L18.5 9H13z"/></svg>';
                const isTombstoned = Boolean(item.tombstonedAt);
                const nameClass = item.isFolder ? 's3-name s3-name-folder' : 's3-name';
                const onclick = item.isBucket
                    ? `TM.s3explorer.navigateTo('${escapeJsString(item.name)}', '')`
                    : item.isFolder
                      ? `TM.s3explorer.navigateTo('${escapeJsString(state.currentBucket)}', '${escapeJsString(item.key)}')`
                      : `TM.s3explorer.openFile('${escapeJsString(state.currentBucket)}', '${escapeJsString(item.key)}')`;
                const itemId = item.key || item.name;
                const isSelected = state.selectedItems.has(itemId);
                let rowClass = isSelected ? 's3-selected' : '';
                if (isTombstoned) rowClass += ' s3-tombstoned';
                // Show checkboxes for files/folders (not buckets), disabled for read-only or tombstoned
                const checkbox = item.isBucket
                    ? ''
                    : `<input type="checkbox" class="s3-checkbox" data-item-id="${escapeHtml(itemId)}" ${isSelected ? 'checked' : ''} ${state.writable && !isTombstoned ? '' : 'disabled'} onclick="event.stopPropagation(); TM.s3explorer.toggleSelection('${escapeJsString(itemId)}')" />`;
                const tombstoneBadge = isTombstoned ? '<span class="s3-badge s3-badge-deleted">Deleted</span>' : '';

                // Only show quota column for bucket list (not when inside a bucket)
                const quotaCell = state.currentBucket ? '' : `<td>${item.quota ? formatBytes(item.quota) : '-'}</td>`;
                // Only show owner column when inside a bucket (not for bucket list)
                const ownerCell = state.currentBucket ? `<td>${escapeHtml(item.owner || '-')}</td>` : '';
                return `
                <tr class="${rowClass}" onclick="${onclick}">
                    <td>${checkbox}</td>
                    <td><div class="s3-item-name">${icon}<span class="${nameClass}">${escapeHtml(item.name)}</span>${tombstoneBadge}</div></td>
                    <td>${item.size !== null ? formatBytes(item.size) : '-'}</td>
                    ${quotaCell}
                    ${ownerCell}
                    <td>${formatDate(item.lastModified)}</td>
                    <td>${formatExpiry(item.expires)}</td>
                </tr>
            `;
            })
            .join('');
        }

        // Update pagination UI using shared helper
        const total = state.currentItems.length;
        const shown = Math.min(state.visibleCount, total);
        if (typeof window.updateSectionPagination === 'function') {
            window.updateSectionPagination('s3', {
                total,
                shown,
                hasMore: total > state.visibleCount,
                canShowLess: state.visibleCount > PAGE_SIZE,
            });
        }
    }

    function renderIconGrid(items) {
        const iconGrid = document.getElementById('s3-icons');
        if (!iconGrid) return;

        iconGrid.innerHTML = items.map((item) => {
            const iconType = getItemIcon(item);
            const displayName = getItemDisplayName(item);
            const iconSVG = getIconSVG(iconType);
            const metaHint = buildItemMetadata(item);

            const isTombstoned = Boolean(item.tombstonedAt);
            const itemId = item.key || item.name;
            const isSelected = state.selectedItems.has(itemId);
            const onclick = buildOnclickHandler(item);

            // Checkbox (not for buckets)
            const checkbox = item.isBucket ? '' :
                `<input type="checkbox" class="s3-icon-checkbox"
                        data-item-id="${escapeHtml(itemId)}"
                        ${isSelected ? 'checked' : ''}
                        ${state.writable && !isTombstoned ? '' : 'disabled'}
                        onclick="event.stopPropagation(); TM.s3explorer.toggleSelection('${escapeJsString(itemId)}')" />`;

            // Tombstone badge
            const tombstoneBadge = isTombstoned ?
                '<span class="s3-badge s3-badge-deleted">Deleted</span>' : '';

            return `
                <div class="s3-icon-item ${isSelected ? 's3-selected' : ''} ${isTombstoned ? 's3-tombstoned' : ''}"
                     onclick="${onclick}">
                    ${checkbox}
                    ${tombstoneBadge}
                    ${iconSVG}
                    <div class="s3-icon-label">${escapeHtml(displayName)}</div>
                    ${metaHint ? `<div class="s3-icon-meta">${metaHint}</div>` : ''}
                </div>
            `;
        }).join('');
    }

    function showMore() {
        state.visibleCount += PAGE_SIZE;
        renderFileListing(false);
    }

    function showLess() {
        state.visibleCount = PAGE_SIZE;
        renderFileListing(false);
    }

    function isBinaryContent(content) {
        // Check for null bytes or high ratio of non-printable characters
        const sample = content.slice(0, 8192);
        let nonPrintable = 0;
        for (let i = 0; i < sample.length; i++) {
            const code = sample.charCodeAt(i);
            if (code === 0) return true; // Null byte = definitely binary
            if (code < 32 && code !== 9 && code !== 10 && code !== 13) {
                nonPrintable++;
            }
        }
        return nonPrintable / sample.length > 0.1; // >10% non-printable = binary
    }

    async function openFile(bucket, key) {
        const browser = document.getElementById('s3-browser');
        const viewer = document.getElementById('s3-viewer');
        const preview = document.getElementById('s3-preview');
        const editor = document.getElementById('s3-editor');
        const browseActions = document.getElementById('s3-browse-actions');
        const selectionActions = document.getElementById('s3-selection-actions');
        const fileActions = document.getElementById('s3-file-actions');
        const saveBtn = document.getElementById('s3-save-btn');
        const deleteBtn = document.getElementById('s3-delete-btn');
        const readonlyBadge = document.getElementById('s3-readonly-badge');

        const fileName = key.split('/').pop();
        // Check if file is tombstoned from cached items
        const item = state.currentItems.find((i) => i.key === key);
        const isTombstoned = item?.tombstonedAt;
        const isReadOnly = !state.writable || isTombstoned;

        // Clear selection when opening file
        state.selectedItems.clear();
        updateSelectionUI();

        state.currentFile = { bucket, key };
        state.isDirty = false;

        // Hide browser, browse actions, and selection actions
        if (browser) browser.style.display = 'none';
        if (browseActions) browseActions.style.display = 'none';
        if (selectionActions) selectionActions.style.display = 'none';

        renderBreadcrumb();

        // Handle images specially (display inline)
        if (isImageFile(fileName)) {
            if (viewer) viewer.style.display = 'none';
            if (preview) {
                preview.style.display = 'flex';
                preview.innerHTML = `<img src="api/s3/buckets/${encodeURIComponent(bucket)}/objects/${encodeURIComponent(key)}" alt="${escapeHtml(fileName)}">`;
            }
            if (fileActions) fileActions.style.display = 'flex';
            if (saveBtn) saveBtn.style.display = isReadOnly ? 'none' : 'inline-flex';
            if (deleteBtn) deleteBtn.style.display = isReadOnly ? 'none' : 'inline-flex';
            return;
        }

        // Try to open all other files as text
        try {
            const { content } = await getObject(bucket, key);

            // Check if content is binary
            if (isBinaryContent(content)) {
                throw new Error('Binary file - not for human eyes');
            }

            // Auto-format JSON files for better readability
            let displayContent = content;
            const ext = getExtension(fileName);
            if (ext === 'json') {
                try {
                    const parsed = JSON.parse(content);
                    displayContent = JSON.stringify(parsed, null, 2);
                } catch (_e) {
                    // If JSON parsing fails, show original content
                    displayContent = content;
                }
            }

            state.originalContent = displayContent;

            if (viewer) viewer.style.display = 'block';
            if (preview) preview.style.display = 'none';
            if (fileActions) fileActions.style.display = 'flex';
            if (saveBtn) saveBtn.style.display = isReadOnly ? 'none' : 'inline-flex';
            if (deleteBtn) deleteBtn.style.display = isReadOnly ? 'none' : 'inline-flex';
            if (readonlyBadge) readonlyBadge.style.display = isReadOnly ? 'block' : 'none';

            if (editor) {
                editor.value = displayContent;
                editor.readOnly = isReadOnly;
            }
            updateLineNumbers();
        } catch (err) {
            showToast(`Failed to load file: ${err.message}`, 'error');
            closeFile();
        }
    }

    function closeFile() {
        if (state.isDirty) {
            if (!confirm('You have unsaved changes. Discard them?')) {
                return;
            }
        }
        state.currentFile = null;
        state.isDirty = false;
        renderFileListing();
    }

    function updateLineNumbers() {
        const editor = document.getElementById('s3-editor');
        const lineNumbers = document.getElementById('s3-line-numbers');
        if (!editor || !lineNumbers) return;

        const lines = editor.value.split('\n').length;
        let html = '';
        for (let i = 1; i <= lines; i++) {
            html += `<div>${i}</div>`;
        }
        lineNumbers.innerHTML = html;
    }

    function onEditorInput() {
        const editor = document.getElementById('s3-editor');
        state.isDirty = editor.value !== state.originalContent;
        updateLineNumbers();
        updateSaveButton();

        // Autosave with debounce
        if (state.autosave && state.isDirty) {
            if (state.autosaveTimer) clearTimeout(state.autosaveTimer);
            state.autosaveTimer = setTimeout(() => {
                saveFile();
            }, 1500);
        }
    }

    function setAutosave(enabled) {
        state.autosave = enabled;
        if (!enabled && state.autosaveTimer) {
            clearTimeout(state.autosaveTimer);
            state.autosaveTimer = null;
        }
    }

    function toggleFullscreen() {
        const section = document.getElementById('s3-section');
        const btn = document.getElementById('s3-fullscreen-btn');
        if (!section) return;

        state.isFullscreen = !state.isFullscreen;
        section.classList.toggle('s3-fullscreen', state.isFullscreen);

        // Update button icon
        if (btn) {
            btn.innerHTML = state.isFullscreen
                ? '<svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor"><path d="M5 16h3v3h2v-5H5v2zm3-8H5v2h5V5H8v3zm6 11h2v-3h3v-2h-5v5zm2-11V5h-2v5h5V8h-3z"/></svg>'
                : '<svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor"><path d="M7 14H5v5h5v-2H7v-3zm-2-4h2V7h3V5H5v5zm12 7h-3v2h5v-5h-2v3zM14 5v2h3v3h2V5h-5z"/></svg>';
        }
    }

    function toggleView() {
        state.viewMode = state.viewMode === 'list' ? 'icon' : 'list';
        updateViewToggleButton();
        renderFileListing(false);
    }

    function updateViewToggleButton() {
        const btn = document.getElementById('s3-view-toggle-btn');
        if (!btn) return;

        const listIcon = '<svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor"><path d="M4 14h4v-4H4v4zm0 5h4v-4H4v4zM4 9h4V5H4v4zm5 5h12v-4H9v4zm0 5h12v-4H9v4zM9 5v4h12V5H9z"/></svg>';
        const gridIcon = '<svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor"><path d="M3 13h8v8H3v-8zm0-10h8v8H3V3zm10 0h8v8h-8V3zm0 10h8v8h-8v-8z"/></svg>';

        btn.innerHTML = state.viewMode === 'list' ? gridIcon : listIcon;
        btn.title = state.viewMode === 'list' ? 'Switch to icon view' : 'Switch to list view';
    }

    function updateSaveButton() {
        const saveBtn = document.getElementById('s3-save-btn');
        if (!saveBtn) return;

        if (state.isDirty) {
            saveBtn.classList.add('dirty');
        } else {
            saveBtn.classList.remove('dirty');
        }
    }

    function syncScroll() {
        const editor = document.getElementById('s3-editor');
        const lineNumbers = document.getElementById('s3-line-numbers');
        if (editor && lineNumbers) {
            lineNumbers.scrollTop = editor.scrollTop;
        }
    }

    // =========================================================================
    // Navigation
    // =========================================================================

    async function navigateTo(bucket, path) {
        if (state.isDirty) {
            if (!confirm('You have unsaved changes. Discard them?')) {
                return;
            }
        }

        state.currentBucket = bucket;
        state.currentPath = path;
        state.currentFile = null;
        state.isDirty = false;

        // Look up writable state from bucket info (fetch if not cached)
        if (bucket && state.buckets.length === 0) {
            state.buckets = await fetchBuckets();
        }
        const bucketInfo = state.buckets.find((b) => b.name === bucket);
        state.writable = bucketInfo ? bucketInfo.writable : true;

        await renderFileListing();
    }

    // =========================================================================
    // File Operations
    // =========================================================================

    async function saveFile() {
        if (!state.currentFile || !state.isDirty) return;

        const editor = document.getElementById('s3-editor');
        const content = editor.value;
        const fileName = state.currentFile.key.split('/').pop();

        try {
            await putObject(state.currentFile.bucket, state.currentFile.key, content, getContentType(fileName));
            state.originalContent = content;
            state.isDirty = false;
            updateSaveButton();
            showToast('File saved', 'success');
        } catch (err) {
            showToast(`Failed to save: ${err.message}`, 'error');
        }
    }

    function downloadFile() {
        if (!state.currentFile) return;

        const url = `api/s3/buckets/${encodeURIComponent(state.currentFile.bucket)}/objects/${encodeURIComponent(state.currentFile.key)}`;
        const a = document.createElement('a');
        a.href = url;
        a.download = state.currentFile.key.split('/').pop();
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
    }

    async function deleteFile() {
        if (!state.currentFile) return;

        const fileName = state.currentFile.key.split('/').pop();
        if (!confirm(`Delete "${fileName}"?`)) return;

        try {
            await deleteObject(state.currentFile.bucket, state.currentFile.key);
            showToast('File deleted', 'success');
            state.currentFile = null;
            state.isDirty = false;
            await renderFileListing();
        } catch (err) {
            showToast(`Failed to delete: ${err.message}`, 'error');
        }
    }

    // =========================================================================
    // New File/Folder
    // =========================================================================

    function openNewModal() {
        if (!state.currentBucket) {
            showToast('Navigate into a bucket first', 'warning');
            return;
        }
        const modal = document.getElementById('s3-new-modal');
        const input = document.getElementById('s3-new-name');
        if (modal) modal.style.display = 'flex';
        if (input) {
            input.value = '';
            input.focus();
        }
    }

    async function createFile() {
        const input = document.getElementById('s3-new-name');
        const name = input?.value.trim();

        if (!name) {
            showToast('Please enter a name', 'warning');
            return;
        }

        const key = state.currentPath + name;

        try {
            await putObject(state.currentBucket, key, '', getContentType(name));
            closeModal('s3-new-modal');
            showToast('File created', 'success');
            await openFile(state.currentBucket, key);
        } catch (err) {
            showToast(`Failed to create file: ${err.message}`, 'error');
        }
    }

    async function createFolder() {
        const input = document.getElementById('s3-new-name');
        const name = input?.value.trim();

        if (!name) {
            showToast('Please enter a name', 'warning');
            return;
        }

        const key = `${state.currentPath + name}/.folder`;

        try {
            await putObject(state.currentBucket, key, '', 'text/plain');
            closeModal('s3-new-modal');
            showToast('Folder created', 'success');
            await navigateTo(state.currentBucket, `${state.currentPath + name}/`);
        } catch (err) {
            showToast(`Failed to create folder: ${err.message}`, 'error');
        }
    }

    function closeModal(modalId) {
        const modal = document.getElementById(modalId);
        if (modal) modal.style.display = 'none';
    }

    // =========================================================================
    // Version History
    // =========================================================================

    async function showVersionHistory() {
        if (!state.currentFile) {
            showToast('No file selected', 'warning');
            return;
        }

        const { bucket, key } = state.currentFile;
        const versions = await fetchVersions(bucket, key);

        if (versions.length === 0) {
            showToast('No version history available', 'info');
            return;
        }

        // Create or get modal - uses standard modal classes for consistency
        let modal = document.getElementById('s3-version-modal');
        if (!modal) {
            modal = document.createElement('div');
            modal.id = 's3-version-modal';
            modal.className = 'modal';
            modal.innerHTML = `
                <div class="modal-content modal-wide">
                    <div class="modal-header">
                        <h3>Version History</h3>
                        <button class="modal-close" onclick="TM.s3explorer.closeModal('s3-version-modal')">&times;</button>
                    </div>
                    <div class="modal-body modal-body-table">
                        <table class="s3-version-table">
                            <thead>
                                <tr>
                                    <th>File</th>
                                    <th class="text-right">Size</th>
                                    <th class="text-right">Date</th>
                                    <th class="text-center">Actions</th>
                                </tr>
                            </thead>
                            <tbody id="s3-version-list"></tbody>
                        </table>
                    </div>
                </div>
            `;
            document.body.appendChild(modal);
        }

        // Populate version list
        const tbody = document.getElementById('s3-version-list');
        const canRestore = state.writable; // Can only restore if bucket is writable

        tbody.innerHTML = versions
            .map((v) => {
                const currentBadge = v.is_current ? '<span class="s3-badge s3-badge-current">Current</span>' : '';
                // Square icon buttons with tooltips
                const downloadBtn = `<button class="btn-icon s3-version-btn" title="Download this version" onclick="TM.s3explorer.downloadVersion('${escapeJsString(v.version_id)}')">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor">
                        <path d="M19 9h-4V3H9v6H5l7 7 7-7zM5 18v2h14v-2H5z"/>
                    </svg>
                </button>`;

                let restoreBtn = '';
                if (!v.is_current) {
                    if (canRestore) {
                        restoreBtn = `<button class="btn-icon s3-version-btn" title="Restore this version" onclick="TM.s3explorer.restoreVersionAndRefresh('${escapeJsString(v.version_id)}')">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor">
                                <path d="M13 3c-4.97 0-9 4.03-9 9H1l3.89 3.89.07.14L9 12H6c0-3.87 3.13-7 7-7s7 3.13 7 7-3.13 7-7 7c-1.93 0-3.68-.79-4.94-2.06l-1.42 1.42C8.27 19.99 10.51 21 13 21c4.97 0 9-4.03 9-9s-4.03-9-9-9zm-1 5v5l4.28 2.54.72-1.21-3.5-2.08V8H12z"/>
                            </svg>
                        </button>`;
                    } else {
                        restoreBtn = `<button class="btn-icon s3-version-btn" title="Cannot restore (read-only)" disabled>
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor">
                                <path d="M13 3c-4.97 0-9 4.03-9 9H1l3.89 3.89.07.14L9 12H6c0-3.87 3.13-7 7-7s7 3.13 7 7-3.13 7-7 7c-1.93 0-3.68-.79-4.94-2.06l-1.42 1.42C8.27 19.99 10.51 21 13 21c4.97 0 9-4.03 9-9s-4.03-9-9-9zm-1 5v5l4.28 2.54.72-1.21-3.5-2.08V8H12z"/>
                            </svg>
                        </button>`;
                    }
                }

                const fileName = key.split('/').pop();
                return `
                    <tr class="${v.is_current ? 's3-version-current' : ''}">
                        <td>
                            <div class="s3-version-filename">${escapeHtml(fileName)} ${currentBadge}</div>
                            <div class="s3-version-id">${escapeHtml(v.version_id)}</div>
                        </td>
                        <td class="text-right">${formatBytes(v.size)}</td>
                        <td class="text-right">${formatVersionDate(v.last_modified)}</td>
                        <td class="text-center">
                            <div class="btn-group">
                                ${downloadBtn}
                                ${restoreBtn}
                            </div>
                        </td>
                    </tr>
                `;
            })
            .join('');

        modal.style.display = 'flex';
    }

    async function restoreVersionAndRefresh(versionId) {
        if (!state.currentFile) return;

        const { bucket, key } = state.currentFile;

        try {
            await restoreVersion(bucket, key, versionId);
            showToast('Version restored', 'success');
            closeModal('s3-version-modal');

            // Reload the file content
            await openFile(bucket, key);
        } catch (err) {
            showToast(`Failed to restore: ${err.message}`, 'error');
        }
    }

    function downloadVersion(versionId) {
        if (!state.currentFile) return;

        const { bucket, key } = state.currentFile;
        const url = `api/s3/buckets/${encodeURIComponent(bucket)}/objects/${encodeURIComponent(key)}?versionId=${encodeURIComponent(versionId)}`;
        const a = document.createElement('a');
        a.href = url;
        a.download = `${key.split('/').pop()}.${versionId.slice(0, 10)}`;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
    }

    // =========================================================================
    // Upload
    // =========================================================================

    function openUploadDialog() {
        if (!state.currentBucket) {
            showToast('Navigate into a bucket first', 'warning');
            return;
        }
        document.getElementById('s3-file-input')?.click();
    }

    async function handleFileSelect(event) {
        const files = event.target.files;
        await uploadFiles(files);
        event.target.value = '';
    }

    async function uploadFiles(files) {
        if (!state.currentBucket) {
            showToast('Navigate into a bucket first', 'warning');
            return;
        }

        for (const file of files) {
            const key = state.currentPath + file.name;

            try {
                const content = await file.arrayBuffer();
                await fetch(
                    `api/s3/buckets/${encodeURIComponent(state.currentBucket)}/objects/${encodeURIComponent(key)}`,
                    {
                        method: 'PUT',
                        headers: { 'Content-Type': file.type || 'application/octet-stream' },
                        body: content,
                    },
                );
                showToast(`Uploaded ${file.name}`, 'success');
            } catch (_err) {
                showToast(`Failed to upload ${file.name}`, 'error');
            }
        }

        await renderFileListing();
    }

    // =========================================================================
    // Drag and Drop
    // =========================================================================

    function initDragDrop() {
        const section = document.getElementById('s3-section');
        const dropZone = document.getElementById('s3-drop-zone');

        if (!section || !dropZone) return;

        let dragCounter = 0;

        section.addEventListener('dragenter', (e) => {
            e.preventDefault();
            dragCounter++;
            if (state.currentBucket) {
                dropZone.classList.add('active');
            }
        });

        section.addEventListener('dragleave', (e) => {
            e.preventDefault();
            dragCounter--;
            if (dragCounter === 0) {
                dropZone.classList.remove('active');
            }
        });

        section.addEventListener('dragover', (e) => {
            e.preventDefault();
        });

        section.addEventListener('drop', async (e) => {
            e.preventDefault();
            dragCounter = 0;
            dropZone.classList.remove('active');

            if (!state.currentBucket) {
                showToast('Navigate into a bucket first', 'warning');
                return;
            }

            await uploadFiles(e.dataTransfer.files);
        });
    }

    // =========================================================================
    // Keyboard Shortcuts
    // =========================================================================

    function initKeyboardShortcuts() {
        document.addEventListener('keydown', (e) => {
            const editor = document.getElementById('s3-editor');
            if (!editor || document.activeElement !== editor) return;

            if ((e.ctrlKey || e.metaKey) && e.key === 's') {
                e.preventDefault();
                saveFile();
            }
        });
    }

    // =========================================================================
    // Selection
    // =========================================================================

    function toggleSelection(itemId) {
        if (state.selectedItems.has(itemId)) {
            state.selectedItems.delete(itemId);
        } else {
            state.selectedItems.add(itemId);
        }
        updateSelectionUI();
        updateRowSelectionVisuals();
    }

    function updateSelectionUI() {
        const browseActions = document.getElementById('s3-browse-actions');
        const selectionActions = document.getElementById('s3-selection-actions');
        const renameBtn = document.getElementById('s3-rename-btn');
        const countEl = document.getElementById('s3-selection-count');

        const count = state.selectedItems.size;

        if (count > 0) {
            if (browseActions) browseActions.style.display = 'none';
            if (selectionActions) selectionActions.style.display = 'flex';
            if (renameBtn) renameBtn.style.display = count === 1 ? 'inline-flex' : 'none';
            if (countEl) countEl.textContent = `${count} selected`;
        } else {
            if (browseActions) browseActions.style.display = 'flex';
            if (selectionActions) selectionActions.style.display = 'none';
        }
    }

    function updateRowSelectionVisuals() {
        const rows = document.querySelectorAll('#s3-files-body tr');
        rows.forEach((row) => {
            const checkbox = row.querySelector('.s3-checkbox');
            if (checkbox) {
                const itemId = checkbox.dataset.itemId;
                const isSelected = state.selectedItems.has(itemId);
                checkbox.checked = isSelected;
                row.classList.toggle('s3-selected', isSelected);
            }
        });
    }

    function getSelectedItem() {
        if (state.selectedItems.size !== 1) return null;
        const itemId = [...state.selectedItems][0];
        return state.currentItems.find((item) => (item.key || item.name) === itemId);
    }

    async function renameSelected() {
        const item = getSelectedItem();
        if (!item) return;

        const oldName = item.name;
        const newName = prompt('Enter new name:', oldName);
        if (!newName || newName === oldName) return;

        // Validate name
        if (newName.includes('/')) {
            alert('Name cannot contain /');
            return;
        }

        if (item.isBucket) {
            alert('Buckets cannot be renamed');
            return;
        }

        if (item.isFolder) {
            alert('Folders cannot be renamed (would require renaming all contents)');
            return;
        }

        try {
            const oldKey = item.key || item.name;
            const newKey = state.currentPath + newName;

            // S3 rename = GET + PUT + DELETE (no native copy support)
            // First get the file content
            const getResp = await fetch(
                `api/s3/buckets/${encodeURIComponent(state.currentBucket)}/objects/${encodeURIComponent(oldKey)}`,
            );
            if (!getResp.ok) {
                throw new Error(`Failed to read file: ${getResp.status}`);
            }
            const content = await getResp.blob();

            // Put to new key
            const putResp = await fetch(
                `api/s3/buckets/${encodeURIComponent(state.currentBucket)}/objects/${encodeURIComponent(newKey)}`,
                {
                    method: 'PUT',
                    body: content,
                },
            );
            if (!putResp.ok) {
                throw new Error(`Failed to create new file: ${putResp.status}`);
            }

            // Delete old key
            const deleteResp = await fetch(
                `api/s3/buckets/${encodeURIComponent(state.currentBucket)}/objects/${encodeURIComponent(oldKey)}`,
                {
                    method: 'DELETE',
                },
            );
            if (!deleteResp.ok) {
                throw new Error(`Failed to delete old file: ${deleteResp.status}`);
            }

            state.selectedItems.clear();
            await renderFileListing();
        } catch (err) {
            console.error('Rename failed:', err);
            alert(`Rename failed: ${err.message}`);
        }
    }

    async function deleteSelected() {
        const count = state.selectedItems.size;
        if (count === 0) return;

        const confirmMsg = count === 1 ? 'Delete this item?' : `Delete ${count} items?`;

        if (!confirm(confirmMsg)) return;

        try {
            for (const itemId of state.selectedItems) {
                const item = state.currentItems.find((i) => (i.key || i.name) === itemId);
                if (!item || item.isBucket) continue;

                const key = item.key || item.name;
                await deleteObject(state.currentBucket, key);
            }

            state.selectedItems.clear();
            updateSelectionUI();
            showToast(`Deleted ${count} item${count > 1 ? 's' : ''}`, 'success');
            await renderFileListing();
        } catch (err) {
            console.error('Delete failed:', err);
            showToast(`Delete failed: ${err.message}`, 'error');
        }
    }

    // =========================================================================
    // Initialization
    // =========================================================================

    function detectActiveTab() {
        const activeTabButton = document.querySelector('#main-tabs .tab.active');
        if (activeTabButton) {
            return activeTabButton.dataset.tab; // 'app', 'data', or 'mesh'
        }
        return 'data'; // Fallback
    }

    async function init() {
        const editor = document.getElementById('s3-editor');
        if (editor) {
            editor.addEventListener('input', onEditorInput);
            editor.addEventListener('scroll', syncScroll);
        }

        // Detect active tab and set context-aware default view mode
        const activeTab = detectActiveTab();
        state.viewMode = state.defaultViewByTab[activeTab] || 'list';

        updateViewToggleButton();
        initDragDrop();
        initKeyboardShortcuts();

        await renderFileListing();
    }

    // =========================================================================
    // Public API
    // =========================================================================

    return {
        init,
        navigateTo,
        openFile,
        closeFile,
        saveFile,
        downloadFile,
        deleteFile,
        openNewModal,
        createFile,
        createFolder,
        closeModal,
        openUploadDialog,
        handleFileSelect,
        refresh: renderFileListing,
        showMore,
        showLess,
        toggleSelection,
        renameSelected,
        deleteSelected,
        setAutosave,
        toggleFullscreen,
        toggleView,
        updateViewToggleButton,
        // Version history
        showVersionHistory,
        restoreVersionAndRefresh,
        downloadVersion,
    };
});
