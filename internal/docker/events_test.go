package docker

import (
	"context"
	"sync"
	"testing"
	"time"
)

// mockEventStream simulates Docker events for testing.
type mockEventStream struct {
	events []ContainerEvent
	index  int
	closed bool
}

func (m *mockEventStream) Read() (*ContainerEvent, error) {
	if m.closed || m.index >= len(m.events) {
		return nil, context.Canceled
	}
	event := &m.events[m.index]
	m.index++
	return event, nil
}

func (m *mockEventStream) Close() error {
	m.closed = true
	return nil
}

func TestEventWatcher(t *testing.T) {
	now := time.Now()
	mockEvents := []ContainerEvent{
		{Type: "start", ContainerID: "abc123", Timestamp: now},
		{Type: "stop", ContainerID: "abc123", Timestamp: now.Add(time.Minute)},
		{Type: "destroy", ContainerID: "abc123", Timestamp: now.Add(2 * time.Minute)},
	}

	stream := &mockEventStream{events: mockEvents}

	var mu sync.Mutex
	eventCount := 0
	done := make(chan struct{})

	handler := func(event ContainerEvent) {
		mu.Lock()
		eventCount++
		count := eventCount
		mu.Unlock()
		if count == 3 {
			close(done)
		}
	}

	// Create watcher with test WaitGroup and context
	var wg sync.WaitGroup
	ctx := context.Background()
	watcher := newEventWatcher(handler, &wg, ctx)

	// Process events
	go func() {
		for {
			event, err := stream.Read()
			if err != nil {
				return
			}
			watcher.handleEvent(*event)
		}
	}()

	// Wait for all events to be processed
	select {
	case <-done:
		// Success
	case <-time.After(500 * time.Millisecond):
		mu.Lock()
		count := eventCount
		mu.Unlock()
		t.Fatalf("timeout waiting for events, got %d/%d", count, 3)
	}

	mu.Lock()
	count := eventCount
	mu.Unlock()
	if count != 3 {
		t.Fatalf("expected 3 events, got %d", count)
	}
}

func TestEventTypeFiltering(t *testing.T) {
	tests := []struct {
		name       string
		eventType  string
		shouldCall bool
	}{
		{"start event", "start", true},
		{"stop event", "stop", true},
		{"die event", "die", true},
		{"destroy event", "destroy", true},
		{"pause event", "pause", false},
		{"unpause event", "unpause", false},
		{"create event", "create", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			done := make(chan struct{})

			handler := func(event ContainerEvent) {
				called = true
				close(done)
			}

			var wg sync.WaitGroup
			ctx := context.Background()
			watcher := newEventWatcher(handler, &wg, ctx)
			event := ContainerEvent{Type: tt.eventType, ContainerID: "test123", Timestamp: time.Now()}

			if shouldHandleEvent(event) {
				watcher.handleEvent(event)
			}

			// Wait for async handler if should be called
			if tt.shouldCall {
				select {
				case <-done:
					// Success
				case <-time.After(200 * time.Millisecond):
					t.Error("timeout waiting for handler to be called")
				}
			} else {
				// Give it a moment to ensure it's NOT called
				time.Sleep(50 * time.Millisecond)
			}

			if called != tt.shouldCall {
				t.Errorf("expected called=%v, got %v for event type %q", tt.shouldCall, called, tt.eventType)
			}
		})
	}
}

func TestEventDebounce(t *testing.T) {
	// Test that rapid events from the same container are debounced
	var mu sync.Mutex
	receivedEvents := []ContainerEvent{}
	handler := func(event ContainerEvent) {
		mu.Lock()
		receivedEvents = append(receivedEvents, event)
		mu.Unlock()
	}

	var wg sync.WaitGroup
	ctx := context.Background()
	watcher := newEventWatcher(handler, &wg, ctx)
	watcher.debounceWindow = 50 * time.Millisecond

	now := time.Now()
	// Send 3 rapid start events for the same container
	for i := 0; i < 3; i++ {
		event := ContainerEvent{Type: "start", ContainerID: "abc123", Timestamp: now}
		watcher.handleEvent(event)
	}

	// Wait for all event handler goroutines to complete
	// This ensures debouncing logic has fully executed
	wg.Wait()

	// Should only have received 1 event due to debouncing
	mu.Lock()
	count := len(receivedEvents)
	mu.Unlock()
	if count > 1 {
		t.Errorf("expected at most 1 event due to debouncing, got %d", count)
	}
}
