// TunnelMesh S3 Explorer
// GitHub-style single-pane file browser

(function(root, factory) {
    if (typeof module !== 'undefined' && module.exports) {
        module.exports = factory();
    } else {
        root.TM = root.TM || {};
        root.TM.s3explorer = factory();
    }
})(typeof globalThis !== 'undefined' ? globalThis : this, function() {
    'use strict';

    // =========================================================================
    // State
    // =========================================================================

    const state = {
        buckets: [],
        currentBucket: null,
        currentPath: '',      // Current folder prefix
        currentFile: null,    // { bucket, key, content, contentType }
        isDirty: false,
        originalContent: '',
    };

    // Text file extensions
    const TEXT_EXTENSIONS = new Set([
        'txt', 'md', 'json', 'yaml', 'yml', 'js', 'ts', 'jsx', 'tsx',
        'go', 'py', 'rb', 'rs', 'css', 'scss', 'html', 'htm', 'xml',
        'sh', 'bash', 'zsh', 'conf', 'ini', 'env', 'toml', 'cfg',
        'sql', 'graphql', 'proto', 'tf', 'hcl', 'makefile',
        'gitignore', 'dockerignore', 'log', 'csv'
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

    function formatBytes(bytes) {
        if (bytes === 0) return '0 B';
        const k = 1024;
        const sizes = ['B', 'KB', 'MB', 'GB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
    }

    function formatDate(isoDate) {
        if (!isoDate) return '-';
        const d = new Date(isoDate);
        const now = new Date();
        const diffMs = now - d;
        const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));

        if (diffDays === 0) return 'Today';
        if (diffDays === 1) return 'Yesterday';
        if (diffDays < 7) return `${diffDays} days ago`;

        return d.toLocaleDateString();
    }

    function getExtension(filename) {
        const parts = filename.split('.');
        if (parts.length < 2) return '';
        return parts[parts.length - 1].toLowerCase();
    }

    function isTextFile(filename) {
        const ext = getExtension(filename);
        const name = filename.toLowerCase();
        return TEXT_EXTENSIONS.has(ext) ||
               name === 'dockerfile' ||
               name === 'makefile' ||
               name.startsWith('.');
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

    // =========================================================================
    // API Functions
    // =========================================================================

    async function fetchBuckets() {
        try {
            const resp = await fetch('api/s3/buckets');
            if (!resp.ok) return [];
            return await resp.json();
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
            body: content
        });
        if (!resp.ok) throw new Error(`Failed to save: ${resp.status}`);
        return true;
    }

    async function deleteObject(bucket, key) {
        const resp = await fetch(`api/s3/buckets/${encodeURIComponent(bucket)}/objects/${encodeURIComponent(key)}`, {
            method: 'DELETE'
        });
        return resp.ok || resp.status === 204;
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
            html = '<span class="s3-breadcrumb-item current">Buckets</span>';
        } else {
            html = '<span class="s3-breadcrumb-item" onclick="TM.s3explorer.navigateTo(null, \'\')">Buckets</span>';
            html += '<span class="s3-breadcrumb-sep">/</span>';

            // Bucket
            if (!state.currentPath && !state.currentFile) {
                html += `<span class="s3-breadcrumb-item current">${escapeHtml(state.currentBucket)}</span>`;
            } else {
                html += `<span class="s3-breadcrumb-item" onclick="TM.s3explorer.navigateTo('${escapeHtml(state.currentBucket)}', '')">${escapeHtml(state.currentBucket)}</span>`;
            }

            // Path parts
            if (state.currentPath || state.currentFile) {
                const path = state.currentFile ? state.currentFile.key : state.currentPath;
                const parts = path.split('/').filter(p => p);
                let accum = '';

                for (let i = 0; i < parts.length; i++) {
                    const part = parts[i];
                    accum += part + '/';
                    html += '<span class="s3-breadcrumb-sep">/</span>';

                    const isLast = i === parts.length - 1;
                    const isFile = state.currentFile && isLast;

                    if (isLast) {
                        html += `<span class="s3-breadcrumb-item current">${escapeHtml(isFile ? part : part)}</span>`;
                    } else {
                        html += `<span class="s3-breadcrumb-item" onclick="TM.s3explorer.navigateTo('${escapeHtml(state.currentBucket)}', '${escapeHtml(accum)}')">${escapeHtml(part)}</span>`;
                    }
                }
            }
        }

        container.innerHTML = html;
    }

    async function renderFileListing() {
        const tbody = document.getElementById('s3-files-body');
        const browser = document.getElementById('s3-browser');
        const viewer = document.getElementById('s3-viewer');
        const preview = document.getElementById('s3-preview');
        const empty = document.getElementById('s3-empty');
        const browseActions = document.getElementById('s3-browse-actions');
        const fileActions = document.getElementById('s3-file-actions');

        if (!tbody) return;

        // Hide viewer/preview, show browser
        if (viewer) viewer.style.display = 'none';
        if (preview) preview.style.display = 'none';
        if (browser) browser.style.display = 'block';
        // Show browse actions, hide file actions
        if (browseActions) browseActions.style.display = 'flex';
        if (fileActions) fileActions.style.display = 'none';

        // Clear current file
        state.currentFile = null;
        state.isDirty = false;

        let items = [];

        if (!state.currentBucket) {
            // Show buckets as folders
            const buckets = await fetchBuckets();
            state.buckets = buckets;
            items = buckets.map(b => ({
                name: b.name,
                isFolder: true,
                isBucket: true,
                size: null,
                lastModified: b.created_at
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

            items = objects.map(obj => {
                if (obj.is_prefix) {
                    const name = obj.key.replace(state.currentPath, '').replace(/\/$/, '');
                    return { name, isFolder: true, key: obj.key, size: null, lastModified: null };
                } else {
                    const name = obj.key.replace(state.currentPath, '');
                    return { name, isFolder: false, key: obj.key, size: obj.size, lastModified: obj.last_modified };
                }
            }).filter(item => item.name && item.name !== '.folder');
        }

        // Always render breadcrumb for navigation
        renderBreadcrumb();

        if (items.length === 0) {
            tbody.innerHTML = '';
            if (empty) empty.style.display = 'block';
            return;
        }

        if (empty) empty.style.display = 'none';

        tbody.innerHTML = items.map(item => {
            const icon = item.isFolder
                ? '<svg class="s3-icon s3-icon-folder" width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M10 4H4c-1.1 0-1.99.9-1.99 2L2 18c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V8c0-1.1-.9-2-2-2h-8l-2-2z"/></svg>'
                : '<svg class="s3-icon s3-icon-file" width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M14 2H6c-1.1 0-1.99.9-1.99 2L4 20c0 1.1.89 2 1.99 2H18c1.1 0 2-.9 2-2V8l-6-6zm2 16H8v-2h8v2zm0-4H8v-2h8v2zm-3-5V3.5L18.5 9H13z"/></svg>';
            const nameClass = item.isFolder ? 's3-name s3-name-folder' : 's3-name';
            const onclick = item.isBucket
                ? `TM.s3explorer.navigateTo('${escapeHtml(item.name)}', '')`
                : item.isFolder
                    ? `TM.s3explorer.navigateTo('${escapeHtml(state.currentBucket)}', '${escapeHtml(item.key)}')`
                    : `TM.s3explorer.openFile('${escapeHtml(state.currentBucket)}', '${escapeHtml(item.key)}')`;

            return `
                <tr onclick="${onclick}">
                    <td><div class="s3-item-name">${icon}<span class="${nameClass}">${escapeHtml(item.name)}</span></div></td>
                    <td>${item.size !== null ? formatBytes(item.size) : '-'}</td>
                    <td>${formatDate(item.lastModified)}</td>
                </tr>
            `;
        }).join('');
    }

    async function openFile(bucket, key) {
        const browser = document.getElementById('s3-browser');
        const viewer = document.getElementById('s3-viewer');
        const preview = document.getElementById('s3-preview');
        const editor = document.getElementById('s3-editor');
        const lineNumbers = document.getElementById('s3-line-numbers');
        const browseActions = document.getElementById('s3-browse-actions');
        const fileActions = document.getElementById('s3-file-actions');

        const fileName = key.split('/').pop();

        state.currentFile = { bucket, key };
        state.isDirty = false;

        // Hide browser and browse actions
        if (browser) browser.style.display = 'none';
        if (browseActions) browseActions.style.display = 'none';

        renderBreadcrumb();

        if (isTextFile(fileName)) {
            try {
                const { content } = await getObject(bucket, key);
                state.originalContent = content;

                if (viewer) viewer.style.display = 'block';
                if (preview) preview.style.display = 'none';
                if (fileActions) fileActions.style.display = 'flex';

                if (editor) {
                    editor.value = content;
                    editor.readOnly = false;
                }
                updateLineNumbers();
            } catch (err) {
                showToast('Failed to load file: ' + err.message, 'error');
                closeFile();
            }
        } else if (isImageFile(fileName)) {
            if (viewer) viewer.style.display = 'none';
            if (preview) {
                preview.style.display = 'flex';
                preview.innerHTML = `<img src="api/s3/buckets/${encodeURIComponent(bucket)}/objects/${encodeURIComponent(key)}" alt="${escapeHtml(fileName)}">`;
            }
            if (fileActions) fileActions.style.display = 'flex';
        } else {
            // Binary file
            if (viewer) viewer.style.display = 'none';
            if (preview) {
                preview.style.display = 'flex';
                preview.innerHTML = `
                    <div class="s3-binary-info">
                        <svg class="s3-binary-icon" width="48" height="48" viewBox="0 0 24 24" fill="currentColor"><path d="M14 2H6c-1.1 0-1.99.9-1.99 2L4 20c0 1.1.89 2 1.99 2H18c1.1 0 2-.9 2-2V8l-6-6zm2 16H8v-2h8v2zm0-4H8v-2h8v2zm-3-5V3.5L18.5 9H13z"/></svg>
                        <div class="s3-binary-name">${escapeHtml(fileName)}</div>
                        <div class="s3-binary-hint">Binary file - click Download to view</div>
                    </div>
                `;
            }
            if (fileActions) fileActions.style.display = 'flex';
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
    }

    function updateSaveButton() {
        const saveBtn = document.getElementById('s3-save-btn');
        if (!saveBtn) return;

        if (state.isDirty) {
            saveBtn.classList.add('dirty');
            saveBtn.textContent = 'Save *';
        } else {
            saveBtn.classList.remove('dirty');
            saveBtn.textContent = 'Save';
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
            showToast('Failed to save: ' + err.message, 'error');
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
            showToast('Failed to delete: ' + err.message, 'error');
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
            showToast('Failed to create file: ' + err.message, 'error');
        }
    }

    async function createFolder() {
        const input = document.getElementById('s3-new-name');
        const name = input?.value.trim();

        if (!name) {
            showToast('Please enter a name', 'warning');
            return;
        }

        const key = state.currentPath + name + '/.folder';

        try {
            await putObject(state.currentBucket, key, '', 'text/plain');
            closeModal('s3-new-modal');
            showToast('Folder created', 'success');
            await navigateTo(state.currentBucket, state.currentPath + name + '/');
        } catch (err) {
            showToast('Failed to create folder: ' + err.message, 'error');
        }
    }

    function closeModal(modalId) {
        const modal = document.getElementById(modalId);
        if (modal) modal.style.display = 'none';
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
                await fetch(`api/s3/buckets/${encodeURIComponent(state.currentBucket)}/objects/${encodeURIComponent(key)}`, {
                    method: 'PUT',
                    headers: { 'Content-Type': file.type || 'application/octet-stream' },
                    body: content
                });
                showToast(`Uploaded ${file.name}`, 'success');
            } catch (err) {
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
    // Initialization
    // =========================================================================

    async function init() {
        const editor = document.getElementById('s3-editor');
        if (editor) {
            editor.addEventListener('input', onEditorInput);
            editor.addEventListener('scroll', syncScroll);
        }

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
    };
});
