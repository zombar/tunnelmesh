// Tests for lib/events.js
import { describe, test, expect, mock } from 'bun:test';
import events from '../lib/events.js';

const { createEventBus } = events;

describe('EventBus', () => {
    test('calls listeners on emit', () => {
        const bus = createEventBus();
        const callback = mock(() => {});

        bus.on('test', callback);
        bus.emit('test', { value: 42 });

        expect(callback).toHaveBeenCalledWith({ value: 42 });
    });

    test('removes listener with off', () => {
        const bus = createEventBus();
        const callback = mock(() => {});

        bus.on('test', callback);
        bus.off('test', callback);
        bus.emit('test', {});

        expect(callback).not.toHaveBeenCalled();
    });

    test('supports multiple listeners', () => {
        const bus = createEventBus();
        const cb1 = mock(() => {});
        const cb2 = mock(() => {});

        bus.on('test', cb1);
        bus.on('test', cb2);
        bus.emit('test', 'data');

        expect(cb1).toHaveBeenCalledWith('data');
        expect(cb2).toHaveBeenCalledWith('data');
    });

    test('once listener fires only once', () => {
        const bus = createEventBus();
        const callback = mock(() => {});

        bus.once('test', callback);
        bus.emit('test', 1);
        bus.emit('test', 2);

        expect(callback).toHaveBeenCalledTimes(1);
        expect(callback).toHaveBeenCalledWith(1);
    });

    test('listenerCount returns correct count', () => {
        const bus = createEventBus();
        const cb1 = () => {};
        const cb2 = () => {};

        expect(bus.listenerCount('test')).toBe(0);

        bus.on('test', cb1);
        expect(bus.listenerCount('test')).toBe(1);

        bus.on('test', cb2);
        expect(bus.listenerCount('test')).toBe(2);

        bus.off('test', cb1);
        expect(bus.listenerCount('test')).toBe(1);
    });

    test('clear removes listeners for specific event', () => {
        const bus = createEventBus();
        const cb1 = mock(() => {});
        const cb2 = mock(() => {});

        bus.on('event1', cb1);
        bus.on('event2', cb2);

        bus.clear('event1');

        bus.emit('event1', 'data');
        bus.emit('event2', 'data');

        expect(cb1).not.toHaveBeenCalled();
        expect(cb2).toHaveBeenCalled();
    });

    test('clear without args removes all listeners', () => {
        const bus = createEventBus();
        const cb1 = mock(() => {});
        const cb2 = mock(() => {});

        bus.on('event1', cb1);
        bus.on('event2', cb2);

        bus.clear();

        bus.emit('event1', 'data');
        bus.emit('event2', 'data');

        expect(cb1).not.toHaveBeenCalled();
        expect(cb2).not.toHaveBeenCalled();
    });

    test('handles errors in listeners gracefully', () => {
        const bus = createEventBus();
        const errorCallback = () => {
            throw new Error('test error');
        };
        const safeCallback = mock(() => {});

        bus.on('test', errorCallback);
        bus.on('test', safeCallback);

        // Should not throw and should continue to other listeners
        expect(() => bus.emit('test', 'data')).not.toThrow();
        expect(safeCallback).toHaveBeenCalled();
    });

    test('emitting non-existent event does not throw', () => {
        const bus = createEventBus();
        expect(() => bus.emit('nonexistent', 'data')).not.toThrow();
    });
});

describe('Default event bus instance', () => {
    test('exports default instance methods', () => {
        expect(typeof events.on).toBe('function');
        expect(typeof events.off).toBe('function');
        expect(typeof events.emit).toBe('function');
        expect(typeof events.once).toBe('function');
    });
});
