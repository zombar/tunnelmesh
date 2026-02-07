// Bun Test Setup - DOM Mocks for Browser API Testing
// This file is preloaded before all tests via bunfig.toml

// Mock DOM element
function createMockElement(tag = 'div') {
    let _textContent = '';
    return {
        tagName: tag.toUpperCase(),
        get textContent() {
            return _textContent;
        },
        set textContent(val) {
            _textContent = String(val);
            // Simulate browser behavior: setting textContent escapes HTML
            this.innerHTML = _textContent
                .replace(/&/g, '&amp;')
                .replace(/</g, '&lt;')
                .replace(/>/g, '&gt;')
                .replace(/"/g, '&quot;')
                .replace(/'/g, '&#39;');
        },
        innerHTML: '',
        innerText: '',
        value: '',
        style: {
            display: '',
            cssText: '',
        },
        classList: {
            _classes: new Set(),
            add(...classes) {
                for (const c of classes) this._classes.add(c);
            },
            remove(...classes) {
                for (const c of classes) this._classes.delete(c);
            },
            toggle(c) {
                this._classes.has(c) ? this._classes.delete(c) : this._classes.add(c);
            },
            contains(c) {
                return this._classes.has(c);
            },
        },
        dataset: {},
        children: [],
        childNodes: [],
        parentElement: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        appendChild(child) {
            this.children.push(child);
            return child;
        },
        removeChild(child) {
            const idx = this.children.indexOf(child);
            if (idx > -1) this.children.splice(idx, 1);
            return child;
        },
        remove() {},
        querySelector: () => null,
        querySelectorAll: () => [],
        getAttribute: () => null,
        setAttribute: () => {},
        removeAttribute: () => {},
        focus: () => {},
        blur: () => {},
        click: () => {},
        dispatchEvent: () => true,
    };
}

// Mock document
globalThis.document = {
    createElement: createMockElement,
    createTextNode: (text) => ({ textContent: text }),
    getElementById: () => null,
    getElementsByClassName: () => [],
    getElementsByTagName: () => [],
    querySelector: () => null,
    querySelectorAll: () => [],
    body: createMockElement('body'),
    head: createMockElement('head'),
    documentElement: createMockElement('html'),
    addEventListener: () => {},
    removeEventListener: () => {},
    createEvent: () => ({
        initEvent: () => {},
    }),
};

// Mock window
globalThis.window = globalThis;

// Mock fetch
globalThis.fetch = async (url, options = {}) => {
    return {
        ok: true,
        status: 200,
        statusText: 'OK',
        headers: new Map(),
        text: async () => '{}',
        json: async () => ({}),
        clone: function () {
            return this;
        },
    };
};

// Mock localStorage
globalThis.localStorage = {
    _data: {},
    getItem(key) {
        return this._data[key] || null;
    },
    setItem(key, value) {
        this._data[key] = String(value);
    },
    removeItem(key) {
        delete this._data[key];
    },
    clear() {
        this._data = {};
    },
    get length() {
        return Object.keys(this._data).length;
    },
    key(i) {
        return Object.keys(this._data)[i] || null;
    },
};

// Mock sessionStorage
globalThis.sessionStorage = { ...globalThis.localStorage, _data: {} };

// Mock requestAnimationFrame
globalThis.requestAnimationFrame = (fn) => setTimeout(fn, 16);
globalThis.cancelAnimationFrame = (id) => clearTimeout(id);

// Mock performance
globalThis.performance = {
    now: () => Date.now(),
    mark: () => {},
    measure: () => {},
    getEntriesByType: () => [],
    getEntriesByName: () => [],
};

// Mock console (already exists in Bun, but ensure methods exist)
globalThis.console = globalThis.console || {
    log: () => {},
    error: () => {},
    warn: () => {},
    info: () => {},
    debug: () => {},
};

// Mock CSS object
globalThis.CSS = {
    escape: (str) => str.replace(/([!"#$%&'()*+,./:;<=>?@[\\\]^`{|}~])/g, '\\$1'),
};

// Mock EventSource for SSE
globalThis.EventSource = class EventSource {
    constructor(url) {
        this.url = url;
        this.readyState = 0;
        this._listeners = {};
    }
    addEventListener(event, fn) {
        (this._listeners[event] ||= []).push(fn);
    }
    removeEventListener(event, fn) {
        if (this._listeners[event]) {
            this._listeners[event] = this._listeners[event].filter((f) => f !== fn);
        }
    }
    close() {
        this.readyState = 2;
    }
};

// Export helper for tests
globalThis.createMockElement = createMockElement;
