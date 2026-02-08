// TunnelMesh S3 Explorer
// Lightweight file browser and editor for S3 storage

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
        currentPath: '',
        currentFile: null,
        expandedBuckets: new Set(),
        expandedFolders: new Set(),
        isDirty: false,
        originalContent: '',
    };

    // Text file extensions
    const TEXT_EXTENSIONS = new Set([
        'txt', 'md', 'json', 'yaml', 'yml', 'js', 'ts', 'jsx', 'tsx',
        'go', 'py', 'rb', 'rs', 'css', 'scss', 'html', 'htm', 'xml',
        'sh', 'bash', 'zsh', 'fish', 'conf', 'ini', 'env', 'toml',
        'sql', 'graphql', 'proto', 'tf', 'hcl', 'dockerfile', 'makefile',
        'gitignore', 'dockerignore', 'editorconfig', 'log', 'csv'
    ]);

    // Image extensions
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

    function getExtension(filename) {
        const parts = filename.split('.');
        if (parts.length < 2) return '';
        return parts[parts.length - 1].toLowerCase();
    }

    function isTextFile(filename) {
        const ext = getExtension(filename);
        const name = filename.toLowerCase();
        // Check extension or known text file names
        return TEXT_EXTENSIONS.has(ext) ||
               name === 'dockerfile' ||
               name === 'makefile' ||
               name === '.gitignore' ||
               name === '.dockerignore';
    }

    function isImageFile(filename) {
        return IMAGE_EXTENSIONS.has(getExtension(filename));
    }

    function getFileIcon(filename) {
        const ext = getExtension(filename);
        const icons = {
            json: '{ }',
            yaml: '---',
            yml: '---',
            md: '#',
            js: 'JS',
            ts: 'TS',
            jsx: 'JSX',
            tsx: 'TSX',
            go: 'Go',
            py: 'Py',
            rb: 'Rb',
            rs: 'Rs',
            sh: '$',
            css: '#',
            scss: '#',
            html: '<>',
            htm: '<>',
            xml: '</>',
            sql: 'SQL',
            toml: 'T',
            ini: 'ini',
            conf: 'cfg',
            env: 'env',
            png: 'IMG',
            jpg: 'IMG',
            jpeg: 'IMG',
            gif: 'IMG',
            svg: 'SVG',
        };
        return icons[ext] || '';
    }

    function getContentType(filename) {
        const ext = getExtension(filename);
        const types = {
            json: 'application/json',
            yaml: 'text/yaml',
            yml: 'text/yaml',
            md: 'text/markdown',
            js: 'application/javascript',
            ts: 'application/typescript',
            go: 'text/x-go',
            html: 'text/html',
            css: 'text/css',
            xml: 'application/xml',
            txt: 'text/plain',
            sh: 'text/x-shellscript',
        };
        return types[ext] || 'text/plain';
    }

    function showToast(message, type = 'info') {
        // Use global showToast if available
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
            const params = new URLSearchParams({
                prefix: prefix,
                delimiter: '/'
            });
            const resp = await fetch(`api/s3/buckets/${bucket}/objects?${params}`);
            if (!resp.ok) return [];
            return await resp.json();
        } catch (err) {
            console.error('Failed to fetch objects:', err);
            return [];
        }
    }

    async function getObject(bucket, key) {
        const resp = await fetch(`api/s3/buckets/${bucket}/objects/${key}`);
        if (!resp.ok) throw new Error(`Failed to get object: ${resp.status}`);
        return {
            content: await resp.text(),
            contentType: resp.headers.get('Content-Type'),
            size: parseInt(resp.headers.get('Content-Length'), 10) || 0,
        };
    }

    async function putObject(bucket, key, content, contentType) {
        const resp = await fetch(`api/s3/buckets/${bucket}/objects/${key}`, {
            method: 'PUT',
            headers: { 'Content-Type': contentType },
            body: content
        });
        if (!resp.ok) throw new Error(`Failed to put object: ${resp.status}`);
        return true;
    }

    async function deleteObject(bucket, key) {
        const resp = await fetch(`api/s3/buckets/${bucket}/objects/${key}`, {
            method: 'DELETE'
        });
        return resp.ok || resp.status === 204;
    }

    // =========================================================================
    // Tree Rendering
    // =========================================================================

    async function renderTree() {
        const container = document.getElementById('s3-tree');
        if (!container) return;

        state.buckets = await fetchBuckets();

        if (state.buckets.length === 0) {
            container.innerHTML = '<div class="s3-empty-tree">No buckets available</div>';
            return;
        }

        let html = '';
        for (const bucket of state.buckets) {
            const isExpanded = state.expandedBuckets.has(bucket.name);
            html += `
                <div class="s3-tree-bucket ${isExpanded ? 'expanded' : ''}">
                    <div class="s3-tree-item bucket" onclick="TM.s3explorer.toggleBucket('${escapeHtml(bucket.name)}')">
                        <span class="s3-tree-arrow">${isExpanded ? '&#9660;' : '&#9654;'}</span>
                        <span class="s3-tree-icon bucket-icon"></span>
                        <span class="s3-tree-name">${escapeHtml(bucket.name)}</span>
                    </div>
                    <div class="s3-tree-children" id="bucket-${escapeHtml(bucket.name)}-children" style="display: ${isExpanded ? 'block' : 'none'};"></div>
                </div>
            `;
        }
        container.innerHTML = html;

        // Load expanded buckets
        for (const bucket of state.buckets) {
            if (state.expandedBuckets.has(bucket.name)) {
                await loadBucketContents(bucket.name, '');
            }
        }
    }

    async function loadBucketContents(bucket, prefix) {
        const containerId = prefix
            ? `folder-${bucket}-${prefix.replace(/\//g, '-')}-children`
            : `bucket-${bucket}-children`;
        const container = document.getElementById(containerId);
        if (!container) return;

        const objects = await fetchObjects(bucket, prefix);

        if (objects.length === 0) {
            container.innerHTML = '<div class="s3-tree-empty">Empty</div>';
            return;
        }

        // Sort: folders first, then files
        objects.sort((a, b) => {
            if (a.is_prefix && !b.is_prefix) return -1;
            if (!a.is_prefix && b.is_prefix) return 1;
            return a.key.localeCompare(b.key);
        });

        let html = '';
        for (const obj of objects) {
            if (obj.is_prefix) {
                // Folder
                const folderPath = obj.key;
                const folderName = obj.key.replace(prefix, '').replace(/\/$/, '');
                const folderId = `${bucket}-${folderPath.replace(/\//g, '-')}`;
                const isExpanded = state.expandedFolders.has(`${bucket}:${folderPath}`);

                html += `
                    <div class="s3-tree-folder ${isExpanded ? 'expanded' : ''}">
                        <div class="s3-tree-item folder" onclick="TM.s3explorer.toggleFolder('${escapeHtml(bucket)}', '${escapeHtml(folderPath)}')">
                            <span class="s3-tree-arrow">${isExpanded ? '&#9660;' : '&#9654;'}</span>
                            <span class="s3-tree-icon folder-icon"></span>
                            <span class="s3-tree-name">${escapeHtml(folderName)}</span>
                        </div>
                        <div class="s3-tree-children" id="folder-${escapeHtml(folderId)}-children" style="display: ${isExpanded ? 'block' : 'none'};"></div>
                    </div>
                `;
            } else {
                // File
                const fileName = obj.key.replace(prefix, '');
                if (!fileName) continue;
                const isSelected = state.currentFile?.bucket === bucket && state.currentFile?.key === obj.key;
                const icon = getFileIcon(fileName);

                html += `
                    <div class="s3-tree-item file ${isSelected ? 'selected' : ''}" onclick="TM.s3explorer.selectFile('${escapeHtml(bucket)}', '${escapeHtml(obj.key)}')">
                        <span class="s3-tree-arrow"></span>
                        <span class="s3-tree-icon file-icon">${icon}</span>
                        <span class="s3-tree-name">${escapeHtml(fileName)}</span>
                        <span class="s3-tree-size">${formatBytes(obj.size)}</span>
                    </div>
                `;
            }
        }

        container.innerHTML = html;

        // Load expanded folders
        for (const obj of objects) {
            if (obj.is_prefix && state.expandedFolders.has(`${bucket}:${obj.key}`)) {
                await loadBucketContents(bucket, obj.key);
            }
        }
    }

    async function toggleBucket(bucketName) {
        if (state.expandedBuckets.has(bucketName)) {
            state.expandedBuckets.delete(bucketName);
        } else {
            state.expandedBuckets.add(bucketName);
            state.currentBucket = bucketName;
            state.currentPath = '';
        }
        await renderTree();
    }

    async function toggleFolder(bucket, folderPath) {
        const key = `${bucket}:${folderPath}`;
        if (state.expandedFolders.has(key)) {
            state.expandedFolders.delete(key);
        } else {
            state.expandedFolders.add(key);
            state.currentBucket = bucket;
            state.currentPath = folderPath;
        }
        await renderTree();
    }

    // =========================================================================
    // File Selection and Editor
    // =========================================================================

    async function selectFile(bucket, key) {
        // Check for unsaved changes
        if (state.isDirty) {
            if (!confirm('You have unsaved changes. Discard them?')) {
                return;
            }
        }

        state.currentFile = { bucket, key };
        state.currentBucket = bucket;
        state.isDirty = false;

        updateBreadcrumb(bucket, key);
        await loadFileContent(bucket, key);
        await renderTree(); // Update selection highlight
    }

    function updateBreadcrumb(bucket, key) {
        const breadcrumb = document.getElementById('s3-breadcrumb');
        if (!breadcrumb) return;

        const parts = key.split('/').filter(p => p);
        let html = `<span class="breadcrumb-bucket">${escapeHtml(bucket)}</span>`;

        for (let i = 0; i < parts.length; i++) {
            html += ` / <span class="breadcrumb-part">${escapeHtml(parts[i])}</span>`;
        }

        breadcrumb.innerHTML = html;
    }

    async function loadFileContent(bucket, key) {
        const editorContainer = document.getElementById('s3-editor-container');
        const preview = document.getElementById('s3-preview');
        const emptyState = document.getElementById('s3-empty-state');
        const actions = document.getElementById('s3-content-actions');
        const editor = document.getElementById('s3-editor');

        const fileName = key.split('/').pop();

        if (isTextFile(fileName)) {
            try {
                const { content } = await getObject(bucket, key);

                editorContainer.style.display = 'flex';
                preview.style.display = 'none';
                emptyState.style.display = 'none';
                actions.style.display = 'flex';

                editor.value = content;
                state.originalContent = content;
                updateLineNumbers();
                applySyntaxClass(fileName);
            } catch (err) {
                showToast('Failed to load file: ' + err.message, 'error');
            }
        } else if (isImageFile(fileName)) {
            editorContainer.style.display = 'none';
            preview.style.display = 'flex';
            emptyState.style.display = 'none';
            actions.style.display = 'flex';

            preview.innerHTML = `<img src="api/s3/buckets/${encodeURIComponent(bucket)}/objects/${encodeURIComponent(key)}" alt="${escapeHtml(fileName)}">`;
        } else {
            // Binary file - show info
            editorContainer.style.display = 'none';
            preview.style.display = 'flex';
            emptyState.style.display = 'none';
            actions.style.display = 'flex';

            preview.innerHTML = `
                <div class="s3-binary-info">
                    <div class="s3-binary-icon">&#128196;</div>
                    <div class="s3-binary-name">${escapeHtml(fileName)}</div>
                    <div class="s3-binary-hint">Binary file - click Download to view</div>
                </div>
            `;
        }
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

    function applySyntaxClass(filename) {
        const editor = document.getElementById('s3-editor');
        if (!editor) return;

        const ext = getExtension(filename);
        editor.className = 's3-editor';
        if (ext) {
            editor.classList.add('syntax-' + ext);
        }
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
            saveBtn.classList.add('s3-dirty');
            saveBtn.textContent = 'Save *';
        } else {
            saveBtn.classList.remove('s3-dirty');
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
    // File Operations
    // =========================================================================

    async function saveFile() {
        if (!state.currentFile || !state.isDirty) return;

        const editor = document.getElementById('s3-editor');
        const content = editor.value;
        const fileName = state.currentFile.key.split('/').pop();

        try {
            await putObject(
                state.currentFile.bucket,
                state.currentFile.key,
                content,
                getContentType(fileName)
            );
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

            // Reset state
            state.currentFile = null;
            state.isDirty = false;
            showEmptyState();
            await renderTree();
        } catch (err) {
            showToast('Failed to delete: ' + err.message, 'error');
        }
    }

    function showEmptyState() {
        const editorContainer = document.getElementById('s3-editor-container');
        const preview = document.getElementById('s3-preview');
        const emptyState = document.getElementById('s3-empty-state');
        const actions = document.getElementById('s3-content-actions');
        const breadcrumb = document.getElementById('s3-breadcrumb');

        if (editorContainer) editorContainer.style.display = 'none';
        if (preview) preview.style.display = 'none';
        if (emptyState) emptyState.style.display = 'flex';
        if (actions) actions.style.display = 'none';
        if (breadcrumb) breadcrumb.innerHTML = '';
    }

    // =========================================================================
    // New File/Folder
    // =========================================================================

    function openNewFileModal() {
        if (!state.currentBucket) {
            showToast('Select a bucket first', 'warning');
            return;
        }
        document.getElementById('s3-new-file-modal').style.display = 'flex';
        document.getElementById('s3-new-file-name').value = '';
        document.getElementById('s3-new-file-name').focus();
    }

    function openNewFolderModal() {
        if (!state.currentBucket) {
            showToast('Select a bucket first', 'warning');
            return;
        }
        document.getElementById('s3-new-folder-modal').style.display = 'flex';
        document.getElementById('s3-new-folder-name').value = '';
        document.getElementById('s3-new-folder-name').focus();
    }

    async function createFile() {
        const input = document.getElementById('s3-new-file-name');
        const fileName = input.value.trim();

        if (!fileName) {
            showToast('Please enter a file name', 'warning');
            return;
        }

        const key = state.currentPath + fileName;

        try {
            await putObject(state.currentBucket, key, '', getContentType(fileName));
            closeModal('s3-new-file-modal');
            showToast('File created', 'success');
            await renderTree();
            await selectFile(state.currentBucket, key);
        } catch (err) {
            showToast('Failed to create file: ' + err.message, 'error');
        }
    }

    async function createFolder() {
        const input = document.getElementById('s3-new-folder-name');
        const folderName = input.value.trim();

        if (!folderName) {
            showToast('Please enter a folder name', 'warning');
            return;
        }

        // Create folder marker (empty object with trailing slash)
        const key = state.currentPath + folderName + '/.folder';

        try {
            await putObject(state.currentBucket, key, '', 'text/plain');
            closeModal('s3-new-folder-modal');
            showToast('Folder created', 'success');

            // Expand the new folder
            state.expandedFolders.add(`${state.currentBucket}:${state.currentPath}${folderName}/`);
            await renderTree();
        } catch (err) {
            showToast('Failed to create folder: ' + err.message, 'error');
        }
    }

    function closeModal(modalId) {
        document.getElementById(modalId).style.display = 'none';
    }

    // =========================================================================
    // Upload
    // =========================================================================

    function openUploadDialog() {
        if (!state.currentBucket) {
            showToast('Select a bucket first', 'warning');
            return;
        }
        document.getElementById('s3-file-input').click();
    }

    async function handleFileSelect(event) {
        const files = event.target.files;
        await uploadFiles(files);
        event.target.value = ''; // Reset input
    }

    async function uploadFiles(files) {
        if (!state.currentBucket) {
            showToast('Select a bucket first', 'warning');
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

        await renderTree();
    }

    // =========================================================================
    // Drag and Drop
    // =========================================================================

    function initDragDrop() {
        const explorer = document.getElementById('s3-explorer');
        const dropZone = document.getElementById('s3-drop-zone');

        if (!explorer || !dropZone) return;

        let dragCounter = 0;

        explorer.addEventListener('dragenter', (e) => {
            e.preventDefault();
            dragCounter++;
            if (state.currentBucket) {
                dropZone.classList.add('active');
            }
        });

        explorer.addEventListener('dragleave', (e) => {
            e.preventDefault();
            dragCounter--;
            if (dragCounter === 0) {
                dropZone.classList.remove('active');
            }
        });

        explorer.addEventListener('dragover', (e) => {
            e.preventDefault();
        });

        explorer.addEventListener('drop', async (e) => {
            e.preventDefault();
            dragCounter = 0;
            dropZone.classList.remove('active');

            if (!state.currentBucket) {
                showToast('Select a bucket first', 'warning');
                return;
            }

            const files = e.dataTransfer.files;
            await uploadFiles(files);
        });
    }

    // =========================================================================
    // Resize Handle
    // =========================================================================

    function initResizeHandle() {
        const handle = document.getElementById('s3-resize-handle');
        const treePanel = document.getElementById('s3-tree-panel');

        if (!handle || !treePanel) return;

        let isResizing = false;
        let startX;
        let startWidth;

        handle.addEventListener('mousedown', (e) => {
            isResizing = true;
            startX = e.clientX;
            startWidth = treePanel.offsetWidth;
            document.body.style.cursor = 'col-resize';
            document.body.style.userSelect = 'none';
        });

        document.addEventListener('mousemove', (e) => {
            if (!isResizing) return;

            const delta = e.clientX - startX;
            const newWidth = Math.max(150, Math.min(500, startWidth + delta));
            treePanel.style.width = newWidth + 'px';
        });

        document.addEventListener('mouseup', () => {
            if (isResizing) {
                isResizing = false;
                document.body.style.cursor = '';
                document.body.style.userSelect = '';
            }
        });
    }

    // =========================================================================
    // Keyboard Shortcuts
    // =========================================================================

    function initKeyboardShortcuts() {
        document.addEventListener('keydown', (e) => {
            // Only handle when editor is focused
            const editor = document.getElementById('s3-editor');
            if (!editor || document.activeElement !== editor) return;

            // Ctrl+S / Cmd+S to save
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
        // Set up editor event listeners
        const editor = document.getElementById('s3-editor');
        if (editor) {
            editor.addEventListener('input', onEditorInput);
            editor.addEventListener('scroll', syncScroll);
        }

        // Initialize features
        initDragDrop();
        initResizeHandle();
        initKeyboardShortcuts();

        // Load initial tree
        await renderTree();
    }

    // =========================================================================
    // Public API
    // =========================================================================

    return {
        init,
        toggleBucket,
        toggleFolder,
        selectFile,
        saveFile,
        downloadFile,
        deleteFile,
        openNewFileModal,
        openNewFolderModal,
        createFile,
        createFolder,
        closeModal,
        openUploadDialog,
        handleFileSelect,
        refresh: renderTree,
    };
});
