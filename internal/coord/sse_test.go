package coord

import (
	"bufio"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

func TestSSEHub_RegisterUnregister(t *testing.T) {
	hub := newSSEHub()

	client := &sseClient{
		events: make(chan string, 10),
		done:   make(chan struct{}),
	}

	// Register client
	hub.register(client)
	if hub.clientCount() != 1 {
		t.Errorf("expected 1 client, got %d", hub.clientCount())
	}

	// Unregister client
	hub.unregister(client)
	if hub.clientCount() != 0 {
		t.Errorf("expected 0 clients, got %d", hub.clientCount())
	}
}

func TestSSEHub_Broadcast(t *testing.T) {
	hub := newSSEHub()

	client1 := &sseClient{
		events: make(chan string, 10),
		done:   make(chan struct{}),
	}
	client2 := &sseClient{
		events: make(chan string, 10),
		done:   make(chan struct{}),
	}

	hub.register(client1)
	hub.register(client2)

	// Broadcast event
	hub.broadcast("event: test\ndata: hello")

	// Check both clients received the event
	select {
	case event := <-client1.events:
		if event != "event: test\ndata: hello" {
			t.Errorf("unexpected event: %s", event)
		}
	case <-time.After(time.Second):
		t.Error("client1 did not receive event")
	}

	select {
	case event := <-client2.events:
		if event != "event: test\ndata: hello" {
			t.Errorf("unexpected event: %s", event)
		}
	case <-time.After(time.Second):
		t.Error("client2 did not receive event")
	}

	hub.unregister(client1)
	hub.unregister(client2)
}

func TestSSEHub_BroadcastFullBuffer(t *testing.T) {
	hub := newSSEHub()

	// Create client with small buffer
	client := &sseClient{
		events: make(chan string, 1),
		done:   make(chan struct{}),
	}
	hub.register(client)

	// Fill the buffer
	hub.broadcast("event1")

	// This should not block even though buffer is full
	done := make(chan bool)
	go func() {
		hub.broadcast("event2")
		done <- true
	}()

	select {
	case <-done:
		// Good, broadcast didn't block
	case <-time.After(time.Second):
		t.Error("broadcast blocked on full buffer")
	}

	hub.unregister(client)
}

func TestHandleSSE(t *testing.T) {
	cfg := &config.ServerConfig{
		MeshCIDR:     "10.99.0.0/16",
		DomainSuffix: ".test",
		AuthToken:    "test-token",
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	// Create test request
	req := httptest.NewRequest("GET", "/admin/api/events", nil)
	w := httptest.NewRecorder()

	// Run handler in goroutine since it blocks
	done := make(chan bool)
	go func() {
		srv.handleSSE(w, req)
		done <- true
	}()

	// Give it a moment to write initial event
	time.Sleep(100 * time.Millisecond)

	// Trigger a heartbeat notification
	srv.notifyHeartbeat("test-peer")

	// Give it a moment to write heartbeat event
	time.Sleep(100 * time.Millisecond)

	// Check response headers
	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("unexpected Content-Type: %s", w.Header().Get("Content-Type"))
	}

	// Check that we got some events
	body := w.Body.String()
	if !strings.Contains(body, "event: connected") {
		t.Errorf("missing connected event in response: %s", body)
	}
	if !strings.Contains(body, "event: heartbeat") {
		t.Errorf("missing heartbeat event in response: %s", body)
	}
	if !strings.Contains(body, "test-peer") {
		t.Errorf("missing peer name in heartbeat event: %s", body)
	}
}

func TestNotifyHeartbeat_NoClients(t *testing.T) {
	cfg := &config.ServerConfig{
		MeshCIDR:     "10.99.0.0/16",
		DomainSuffix: ".test",
		AuthToken:    "test-token",
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	// Should not panic with no clients
	srv.notifyHeartbeat("test-peer")
}

func TestSSEEventFormat(t *testing.T) {
	hub := newSSEHub()
	client := &sseClient{
		events: make(chan string, 10),
		done:   make(chan struct{}),
	}
	hub.register(client)

	// Create a server just for notification
	cfg := &config.ServerConfig{
		MeshCIDR:     "10.99.0.0/16",
		DomainSuffix: ".test",
		AuthToken:    "test-token",
	}
	srv, _ := NewServer(cfg)
	srv.sseHub = hub

	// Notify heartbeat
	srv.notifyHeartbeat("my-peer")

	// Check event format
	select {
	case event := <-client.events:
		// Parse the SSE format
		scanner := bufio.NewScanner(strings.NewReader(event))
		var eventType, data string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				eventType = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
		}

		if eventType != "heartbeat" {
			t.Errorf("expected event type 'heartbeat', got '%s'", eventType)
		}
		if !strings.Contains(data, "my-peer") {
			t.Errorf("expected data to contain peer name, got '%s'", data)
		}
	case <-time.After(time.Second):
		t.Error("did not receive heartbeat event")
	}

	hub.unregister(client)
}
