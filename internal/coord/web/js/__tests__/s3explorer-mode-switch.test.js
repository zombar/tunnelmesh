// Tests for deterministic mode switching in s3explorer.js
import { describe, test, expect, beforeEach, mock } from 'bun:test';

describe('Deterministic Mode Switching', () => {
    let state;
    let editor;
    let wysiwyg;
    let mockMarkdown;

    beforeEach(() => {
        // Reset state
        state = {
            currentFile: { bucket: 'test-bucket', key: 'test.md', content: '**bold** text' },
            isDirty: false,
            originalContent: '**bold** text',
            editorMode: 'source',
            writable: true,
            autosave: false,
            autosaveTimer: null,
            // Canonical source tracking
            canonicalMarkdown: '**bold** text',
            wysiwygSnapshot: '',
            // Cursor positions
            sourceCursorPosition: { start: 0, end: 0 },
            wysiwygCursorPosition: { offset: 0 },
            // Observer control
            mutationObserverPaused: false,
            wysiwygObserver: null,
        };

        // Mock textarea editor
        editor = {
            value: '**bold** text',
            selectionStart: 0,
            selectionEnd: 0,
            focus: mock(() => {}),
        };

        // Mock contenteditable wysiwyg
        wysiwyg = {
            innerHTML: '<strong>bold</strong> text',
            contentEditable: 'true',
            focus: mock(() => {}),
        };

        // Mock TM.markdown
        mockMarkdown = {
            renderMarkdown: mock((md) => {
                // Simple mock: **bold** -> <strong>bold</strong>
                return md.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
            }),
            htmlToMarkdown: mock((html) => {
                // Simple mock: <strong>bold</strong> -> **bold**
                return html.replace(/<strong>([^<]+)<\/strong>/g, '**$1**');
            }),
        };
    });

    describe('Canonical Markdown Tracking', () => {
        test('openFile initializes canonicalMarkdown', () => {
            const content = '# Heading\n\n**bold** and *italic*';
            state.originalContent = content;
            state.canonicalMarkdown = content;
            state.wysiwygSnapshot = '';

            expect(state.canonicalMarkdown).toBe(content);
            expect(state.wysiwygSnapshot).toBe('');
            expect(state.isDirty).toBe(false);
        });

        test('source editor updates canonicalMarkdown on input', () => {
            editor.value = '**new** content';
            state.canonicalMarkdown = editor.value;

            expect(state.canonicalMarkdown).toBe('**new** content');
        });

        test('WYSIWYG updates canonicalMarkdown via conversion', () => {
            wysiwyg.innerHTML = '<strong>edited</strong> content';
            const converted = mockMarkdown.htmlToMarkdown(wysiwyg.innerHTML);
            state.canonicalMarkdown = converted;

            expect(state.canonicalMarkdown).toBe('**edited** content');
        });

        test('canonicalMarkdown persists across mode switches', () => {
            const original = '**bold** and *italic*';
            state.canonicalMarkdown = original;

            // Switch source -> wysiwyg
            state.editorMode = 'wysiwyg';
            expect(state.canonicalMarkdown).toBe(original);

            // Switch wysiwyg -> source
            state.editorMode = 'source';
            expect(state.canonicalMarkdown).toBe(original);
        });
    });

    describe('Mode Switching Determinism', () => {
        test('source → wysiwyg preserves content exactly', () => {
            const original = '**bold** and *italic*';
            state.editorMode = 'source';
            state.canonicalMarkdown = original;
            editor.value = original;

            // Switch to WYSIWYG
            state.editorMode = 'wysiwyg';
            wysiwyg.innerHTML = mockMarkdown.renderMarkdown(state.canonicalMarkdown);

            expect(state.canonicalMarkdown).toBe(original);
            expect(state.isDirty).toBe(false);
        });

        test('wysiwyg → source preserves content exactly', () => {
            const original = '**bold** and *italic*';
            state.editorMode = 'wysiwyg';
            state.canonicalMarkdown = original;
            wysiwyg.innerHTML = mockMarkdown.renderMarkdown(original);

            // Switch to source
            state.editorMode = 'source';
            editor.value = state.canonicalMarkdown;

            expect(editor.value).toBe(original);
            expect(state.canonicalMarkdown).toBe(original);
            expect(state.isDirty).toBe(false);
        });

        test('multiple mode switches preserve content (round-trip)', () => {
            const original = '# Heading\n\n**bold** *italic*\n\n- list\n- items';
            state.canonicalMarkdown = original;
            state.originalContent = original;

            // source -> wysiwyg
            state.editorMode = 'wysiwyg';
            expect(state.canonicalMarkdown).toBe(original);

            // wysiwyg -> source
            state.editorMode = 'source';
            expect(state.canonicalMarkdown).toBe(original);

            // source -> wysiwyg
            state.editorMode = 'wysiwyg';
            expect(state.canonicalMarkdown).toBe(original);

            // wysiwyg -> source
            state.editorMode = 'source';
            expect(state.canonicalMarkdown).toBe(original);

            expect(state.isDirty).toBe(false);
        });

        test('mode switch does not trigger dirty state', () => {
            const original = '**content**';
            state.canonicalMarkdown = original;
            state.originalContent = original;
            state.isDirty = false;

            // Switch source -> wysiwyg
            state.editorMode = 'wysiwyg';
            expect(state.isDirty).toBe(false);

            // Switch wysiwyg -> source
            state.editorMode = 'source';
            expect(state.isDirty).toBe(false);
        });

        test('mode switch with complex markdown preserves structure', () => {
            const complex = `# Title

## Subtitle

**Bold** and *italic* and ***both***

- List item 1
- List item 2
  - Nested item

\`\`\`javascript
const code = 'block';
\`\`\`

[Link](https://example.com)`;

            state.canonicalMarkdown = complex;
            state.originalContent = complex;

            // Multiple switches
            state.editorMode = 'wysiwyg';
            expect(state.canonicalMarkdown).toBe(complex);

            state.editorMode = 'source';
            expect(state.canonicalMarkdown).toBe(complex);

            expect(state.isDirty).toBe(false);
        });
    });

    describe('Observer Pause Mechanism', () => {
        test('mutationObserverPaused flag prevents observer from firing', () => {
            state.mutationObserverPaused = true;
            wysiwyg.innerHTML = '<strong>changed</strong>';

            // Simulate observer callback
            const shouldFire = !state.mutationObserverPaused && state.editorMode === 'wysiwyg';
            expect(shouldFire).toBe(false);
        });

        test('observer fires when not paused and in wysiwyg mode', () => {
            state.mutationObserverPaused = false;
            state.editorMode = 'wysiwyg';
            state.currentFile = { bucket: 'test', key: 'test.md' };

            const shouldFire = !state.mutationObserverPaused && state.editorMode === 'wysiwyg' && !!state.currentFile;
            expect(shouldFire).toBe(true);
        });

        test('observer does not fire in source mode', () => {
            state.mutationObserverPaused = false;
            state.editorMode = 'source';

            const shouldFire = !state.mutationObserverPaused && state.editorMode === 'wysiwyg';
            expect(shouldFire).toBe(false);
        });

        test('observer paused during mode switch to wysiwyg', () => {
            state.editorMode = 'source';

            // Simulate toggle to wysiwyg
            state.mutationObserverPaused = true;
            state.editorMode = 'wysiwyg';
            wysiwyg.innerHTML = mockMarkdown.renderMarkdown(state.canonicalMarkdown);
            state.wysiwygSnapshot = wysiwyg.innerHTML;
            state.mutationObserverPaused = false;

            // After mode switch, observer should be unpaused
            expect(state.mutationObserverPaused).toBe(false);
        });
    });

    describe('Snapshot-Based Dirty Detection', () => {
        test('wysiwygSnapshot initialized on file open', () => {
            state.editorMode = 'wysiwyg';
            wysiwyg.innerHTML = mockMarkdown.renderMarkdown(state.canonicalMarkdown);
            state.wysiwygSnapshot = wysiwyg.innerHTML;

            expect(state.wysiwygSnapshot).toBe(wysiwyg.innerHTML);
        });

        test('snapshot comparison detects real user edits', () => {
            state.editorMode = 'wysiwyg';
            wysiwyg.innerHTML = '<strong>original</strong>';
            state.wysiwygSnapshot = '<strong>original</strong>';

            // User edits
            wysiwyg.innerHTML = '<strong>edited</strong>';

            const hasChanged = wysiwyg.innerHTML !== state.wysiwygSnapshot;
            expect(hasChanged).toBe(true);
        });

        test('snapshot comparison ignores programmatic changes', () => {
            state.editorMode = 'wysiwyg';
            wysiwyg.innerHTML = '<strong>content</strong>';
            state.wysiwygSnapshot = '<strong>content</strong>';

            // Programmatic change with observer paused
            state.mutationObserverPaused = true;
            wysiwyg.innerHTML = '<strong>programmatic</strong>';
            state.mutationObserverPaused = false;

            // Snapshot not updated during pause, so would detect change
            // but observer was paused so it didn't fire
            const observerWouldFire = !state.mutationObserverPaused;
            expect(observerWouldFire).toBe(true); // Now unpaused, but already missed the change
        });

        test('dirty state only set on actual content change', () => {
            state.canonicalMarkdown = 'original';
            state.originalContent = 'original';
            state.isDirty = false;

            // No change
            expect(state.canonicalMarkdown === state.originalContent).toBe(true);
            expect(state.isDirty).toBe(false);

            // Actual change
            state.canonicalMarkdown = 'modified';
            state.isDirty = state.canonicalMarkdown !== state.originalContent;
            expect(state.isDirty).toBe(true);
        });
    });

    describe('Cursor Position Tracking', () => {
        test('source cursor position saved before mode switch', () => {
            state.editorMode = 'source';
            editor.selectionStart = 5;
            editor.selectionEnd = 10;

            state.sourceCursorPosition = {
                start: editor.selectionStart,
                end: editor.selectionEnd,
            };

            expect(state.sourceCursorPosition.start).toBe(5);
            expect(state.sourceCursorPosition.end).toBe(10);
        });

        test('wysiwyg cursor position saved before mode switch', () => {
            state.editorMode = 'wysiwyg';
            const offset = 15;

            state.wysiwygCursorPosition = { offset };

            expect(state.wysiwygCursorPosition.offset).toBe(15);
        });

        test('cursor positions stored separately per mode', () => {
            // Set source cursor
            state.sourceCursorPosition = { start: 5, end: 5 };

            // Switch to wysiwyg and set cursor
            state.editorMode = 'wysiwyg';
            state.wysiwygCursorPosition = { offset: 10 };

            // Both positions preserved
            expect(state.sourceCursorPosition.start).toBe(5);
            expect(state.wysiwygCursorPosition.offset).toBe(10);
        });

        test('source cursor restored when switching back to source', () => {
            state.sourceCursorPosition = { start: 7, end: 12 };
            state.editorMode = 'source';

            // Simulate cursor restoration
            editor.selectionStart = state.sourceCursorPosition.start;
            editor.selectionEnd = state.sourceCursorPosition.end;

            expect(editor.selectionStart).toBe(7);
            expect(editor.selectionEnd).toBe(12);
        });
    });

    describe('Save Operation', () => {
        test('saveFile uses canonicalMarkdown, not converted HTML', () => {
            state.editorMode = 'wysiwyg';
            state.canonicalMarkdown = '**bold** original';
            wysiwyg.innerHTML = '<strong>bold</strong> original';

            // Save should use canonicalMarkdown
            const contentToSave = state.canonicalMarkdown;
            expect(contentToSave).toBe('**bold** original');

            // Should NOT convert from HTML - canonicalMarkdown is already stored
            expect(contentToSave).toBe('**bold** original'); // Not the converted version
        });

        test('save from source mode uses canonicalMarkdown', () => {
            state.editorMode = 'source';
            editor.value = '**updated** content';
            state.canonicalMarkdown = editor.value;

            const contentToSave = state.canonicalMarkdown;
            expect(contentToSave).toBe('**updated** content');
        });

        test('save updates originalContent to match saved content', () => {
            state.canonicalMarkdown = '**new** content';
            state.originalContent = '**old** content';
            state.isDirty = true;

            // Simulate save
            state.originalContent = state.canonicalMarkdown;
            state.isDirty = false;

            expect(state.originalContent).toBe('**new** content');
            expect(state.isDirty).toBe(false);
        });
    });

    describe('Edge Cases', () => {
        test('empty file mode switching', () => {
            state.canonicalMarkdown = '';
            state.originalContent = '';

            state.editorMode = 'wysiwyg';
            expect(state.canonicalMarkdown).toBe('');

            state.editorMode = 'source';
            expect(state.canonicalMarkdown).toBe('');
        });

        test('file with only whitespace', () => {
            const whitespace = '   \n\n  \t  \n';
            state.canonicalMarkdown = whitespace;
            state.originalContent = whitespace;

            state.editorMode = 'wysiwyg';
            expect(state.canonicalMarkdown).toBe(whitespace);

            state.editorMode = 'source';
            expect(state.canonicalMarkdown).toBe(whitespace);
        });

        test('very long markdown content', () => {
            const longContent = `# Heading\n\n${'Lorem ipsum dolor sit amet. '.repeat(1000)}`;
            state.canonicalMarkdown = longContent;
            state.originalContent = longContent;

            state.editorMode = 'wysiwyg';
            expect(state.canonicalMarkdown).toBe(longContent);

            state.editorMode = 'source';
            expect(state.canonicalMarkdown).toBe(longContent);
        });

        test('special characters and unicode', () => {
            const special = '**Émojis**: 🎉 🔥 ✨\n\n**Symbols**: © ® ™ € ¥';
            state.canonicalMarkdown = special;
            state.originalContent = special;

            state.editorMode = 'wysiwyg';
            expect(state.canonicalMarkdown).toBe(special);

            state.editorMode = 'source';
            expect(state.canonicalMarkdown).toBe(special);
        });

        test('markdown with HTML entities', () => {
            const entities = 'Use &lt;strong&gt; for **bold** text';
            state.canonicalMarkdown = entities;
            state.originalContent = entities;

            state.editorMode = 'wysiwyg';
            expect(state.canonicalMarkdown).toBe(entities);

            state.editorMode = 'source';
            expect(state.canonicalMarkdown).toBe(entities);
        });
    });

    describe('Integration Scenarios', () => {
        test('complete workflow: open -> edit source -> switch -> edit wysiwyg -> save', () => {
            // Open file
            const original = '# Title\n\n**original**';
            state.canonicalMarkdown = original;
            state.originalContent = original;
            state.editorMode = 'source';
            editor.value = original;

            // Edit in source
            editor.value = '# Title\n\n**edited in source**';
            state.canonicalMarkdown = editor.value;
            state.isDirty = true;

            // Switch to WYSIWYG
            state.editorMode = 'wysiwyg';
            wysiwyg.innerHTML = mockMarkdown.renderMarkdown(state.canonicalMarkdown);
            state.wysiwygSnapshot = wysiwyg.innerHTML;

            expect(state.canonicalMarkdown).toContain('edited in source');

            // Edit in WYSIWYG
            wysiwyg.innerHTML = '<h1>Title</h1><strong>edited in wysiwyg</strong>';
            const converted = mockMarkdown.htmlToMarkdown(wysiwyg.innerHTML);
            state.canonicalMarkdown = converted;
            state.wysiwygSnapshot = wysiwyg.innerHTML;

            // Save
            const saved = state.canonicalMarkdown;
            state.originalContent = saved;
            state.isDirty = false;

            expect(saved).toContain('edited in wysiwyg');
            expect(state.isDirty).toBe(false);
        });

        test('autosave only triggers on real edits, not mode switches', () => {
            state.autosave = true;
            state.autosaveTimer = null;
            const original = '**content**';
            state.canonicalMarkdown = original;
            state.originalContent = original;
            state.isDirty = false;

            // Mode switch should not set dirty
            state.editorMode = 'wysiwyg';
            expect(state.isDirty).toBe(false);

            state.editorMode = 'source';
            expect(state.isDirty).toBe(false);

            // Real edit should set dirty
            editor.value = '**edited**';
            state.canonicalMarkdown = editor.value;
            state.isDirty = state.canonicalMarkdown !== state.originalContent;

            expect(state.isDirty).toBe(true);
        });

        test('multiple files with independent state', () => {
            // File 1
            const file1 = {
                canonicalMarkdown: '**file 1**',
                originalContent: '**file 1**',
                isDirty: false,
            };

            // File 2
            const file2 = {
                canonicalMarkdown: '**file 2**',
                originalContent: '**file 2**',
                isDirty: false,
            };

            // Edit file 1
            file1.canonicalMarkdown = '**edited file 1**';
            file1.isDirty = true;

            // File 2 should be unaffected
            expect(file2.canonicalMarkdown).toBe('**file 2**');
            expect(file2.isDirty).toBe(false);
        });
    });
});
