package docker

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// eventWatcher watches Docker events and triggers callbacks.
type eventWatcher struct {
	handler        func(ContainerEvent)
	debounceWindow time.Duration
	lastEvent      map[string]time.Time // containerID -> last event time
	mu             sync.Mutex
}

// newEventWatcher creates a new event watcher with the given handler.
func newEventWatcher(handler func(ContainerEvent)) *eventWatcher {
	return &eventWatcher{
		handler:        handler,
		debounceWindow: 100 * time.Millisecond, // Default debounce window
		lastEvent:      make(map[string]time.Time),
	}
}

// handleEvent processes a Docker event.
// Events are debounced to avoid rapid repeated calls for the same container.
func (w *eventWatcher) handleEvent(event ContainerEvent) {
	if !shouldHandleEvent(event) {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// Check debounce
	key := event.ContainerID + ":" + event.Type
	lastTime, exists := w.lastEvent[key]
	if exists && time.Since(lastTime) < w.debounceWindow {
		log.Debug().
			Str("container", event.ContainerID).
			Str("type", event.Type).
			Msg("Debouncing Docker event")
		return
	}

	w.lastEvent[key] = time.Now()

	// Call handler asynchronously to avoid blocking event loop
	go w.handler(event)
}

// cleanup removes old entries from the debounce map to prevent unbounded growth.
// Should be called periodically in a background goroutine.
func (w *eventWatcher) cleanup(maxAge time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	for key, lastTime := range w.lastEvent {
		if lastTime.Before(cutoff) {
			delete(w.lastEvent, key)
		}
	}
}

// shouldHandleEvent returns true if we care about this event type.
func shouldHandleEvent(event ContainerEvent) bool {
	switch event.Type {
	case "start", "stop", "die", "destroy":
		return true
	default:
		return false
	}
}

// watchEvents watches Docker events and syncs port forwards on container lifecycle changes.
// Blocks until context is cancelled.
func (m *Manager) watchEvents(ctx context.Context) error {
	if m.client == nil {
		return nil
	}

	log.Info().Str("peer", m.peerName).Msg("Starting Docker event watcher")

	watcher := newEventWatcher(func(event ContainerEvent) {
		log.Debug().
			Str("container", event.ContainerID).
			Str("type", event.Type).
			Str("peer", m.peerName).
			Msg("Docker container event")

		// Handle port forwarding based on event type
		switch event.Type {
		case "start":
			// Container started, sync port forwards
			if err := m.syncPortForwards(ctx, event.ContainerID); err != nil {
				log.Error().Err(err).
					Str("container", event.ContainerID).
					Msg("Failed to sync port forwards on container start")
			}
		case "stop", "die", "destroy":
			// Container stopped/destroyed, port forwards will expire naturally via TTL
			log.Debug().
				Str("container", event.ContainerID).
				Str("type", event.Type).
				Msg("Container stopped, port forwards will expire via TTL")
		}
	})

	// Start periodic cleanup of debounce map to prevent memory growth
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// Clean up entries older than 10 minutes
				watcher.cleanup(10 * time.Minute)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Stream events from Docker daemon
	return m.client.WatchEvents(ctx, watcher.handleEvent)
}
