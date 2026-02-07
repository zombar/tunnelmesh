// Tests for lib/api.js
import { describe, test, expect, mock, beforeEach } from 'bun:test';
import api from '../lib/api.js';

describe('api', () => {
    beforeEach(() => {
        // Reset handlers before each test
        api.setAuthErrorHandler(null);
        api.setToastHandler(null);
    });

    describe('apiFetch', () => {
        test('returns ok: true with data on success', async () => {
            globalThis.fetch = mock(async () => ({
                ok: true,
                status: 200,
                text: async () => '{"message": "success"}',
            }));

            const result = await api.apiFetch('/api/test');

            expect(result.ok).toBe(true);
            expect(result.data).toEqual({ message: 'success' });
            expect(result.status).toBe(200);
        });

        test('handles empty response', async () => {
            globalThis.fetch = mock(async () => ({
                ok: true,
                status: 204,
                text: async () => '',
            }));

            const result = await api.apiFetch('/api/test');

            expect(result.ok).toBe(true);
            expect(result.data).toBe(null);
        });

        test('handles non-JSON response', async () => {
            globalThis.fetch = mock(async () => ({
                ok: true,
                status: 200,
                text: async () => 'plain text response',
            }));

            const result = await api.apiFetch('/api/test');

            expect(result.ok).toBe(true);
            expect(result.data).toBe('plain text response');
        });

        test('calls auth error handler on 401', async () => {
            const authHandler = mock(() => {});
            api.setAuthErrorHandler(authHandler);

            globalThis.fetch = mock(async () => ({
                ok: false,
                status: 401,
                text: async () => '',
            }));

            const result = await api.apiFetch('/api/test');

            expect(result.ok).toBe(false);
            expect(result.status).toBe(401);
            expect(authHandler).toHaveBeenCalled();
        });

        test('returns error on non-ok response', async () => {
            globalThis.fetch = mock(async () => ({
                ok: false,
                status: 500,
                text: async () => '',
            }));

            const result = await api.apiFetch('/api/test', { showToast: false });

            expect(result.ok).toBe(false);
            expect(result.status).toBe(500);
            expect(result.error).toBeDefined();
        });

        test('shows toast on error when enabled', async () => {
            const toastHandler = mock(() => {});
            api.setToastHandler(toastHandler);

            globalThis.fetch = mock(async () => ({
                ok: false,
                status: 500,
                text: async () => '',
            }));

            await api.apiFetch('/api/test');

            expect(toastHandler).toHaveBeenCalled();
        });

        test('suppresses toast when showToast is false', async () => {
            const toastHandler = mock(() => {});
            api.setToastHandler(toastHandler);

            globalThis.fetch = mock(async () => ({
                ok: false,
                status: 500,
                text: async () => '',
            }));

            await api.apiFetch('/api/test', { showToast: false });

            expect(toastHandler).not.toHaveBeenCalled();
        });

        test('handles network errors', async () => {
            globalThis.fetch = mock(async () => {
                throw new Error('Network error');
            });

            const result = await api.apiFetch('/api/test', { showToast: false });

            expect(result.ok).toBe(false);
            expect(result.error).toBeDefined();
            expect(result.error.message).toBe('Network error');
        });
    });

    describe('get', () => {
        test('makes GET request', async () => {
            globalThis.fetch = mock(async (url, options) => {
                expect(options.method).toBe('GET');
                return { ok: true, status: 200, text: async () => '{}' };
            });

            await api.get('/api/test');

            expect(globalThis.fetch).toHaveBeenCalled();
        });
    });

    describe('post', () => {
        test('makes POST request with JSON body', async () => {
            globalThis.fetch = mock(async (url, options) => {
                expect(options.method).toBe('POST');
                expect(options.headers['Content-Type']).toBe('application/json');
                expect(options.body).toBe('{"name":"test"}');
                return { ok: true, status: 200, text: async () => '{}' };
            });

            await api.post('/api/test', { name: 'test' });

            expect(globalThis.fetch).toHaveBeenCalled();
        });
    });

    describe('patch', () => {
        test('makes PATCH request with JSON body', async () => {
            globalThis.fetch = mock(async (url, options) => {
                expect(options.method).toBe('PATCH');
                expect(options.headers['Content-Type']).toBe('application/json');
                return { ok: true, status: 200, text: async () => '{}' };
            });

            await api.patch('/api/test', { enabled: true });

            expect(globalThis.fetch).toHaveBeenCalled();
        });
    });

    describe('del', () => {
        test('makes DELETE request', async () => {
            globalThis.fetch = mock(async (url, options) => {
                expect(options.method).toBe('DELETE');
                return { ok: true, status: 200, text: async () => '{}' };
            });

            await api.del('/api/test');

            expect(globalThis.fetch).toHaveBeenCalled();
        });

        test('makes DELETE request with body', async () => {
            globalThis.fetch = mock(async (url, options) => {
                expect(options.method).toBe('DELETE');
                expect(options.body).toBe('{"id":"123"}');
                return { ok: true, status: 200, text: async () => '{}' };
            });

            await api.del('/api/test', { id: '123' });

            expect(globalThis.fetch).toHaveBeenCalled();
        });
    });
});
