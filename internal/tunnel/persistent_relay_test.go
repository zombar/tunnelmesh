package tunnel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

var testUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// mockRelayServer simulates the coordination server's persistent relay endpoint.
type mockRelayServer struct {
	t           *testing.T
	server      *httptest.Server
	connections map[string]*websocket.Conn
	mu          sync.Mutex
	received    chan relayMessage
	heartbeats  chan heartbeatMessage
}

type heartbeatMessage struct {
	peerName string
	data     []byte
}

type relayMessage struct {
	source string
	target string
	data   []byte
}

func newMockRelayServer(t *testing.T) *mockRelayServer {
	m := &mockRelayServer{
		t:           t,
		connections: make(map[string]*websocket.Conn),
		received:    make(chan relayMessage, 10),
		heartbeats:  make(chan heartbeatMessage, 10),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/relay/persistent", m.handlePersistentRelay)
	m.server = httptest.NewServer(mux)

	return m
}

func (m *mockRelayServer) handlePersistentRelay(w http.ResponseWriter, r *http.Request) {
	// Extract peer name from auth header (simplified)
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	peerName := strings.TrimPrefix(auth, "Bearer ")

	conn, err := testUpgrader.Upgrade(w, r, nil)
	if err != nil {
		m.t.Logf("upgrade failed: %v", err)
		return
	}

	m.mu.Lock()
	m.connections[peerName] = conn
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.connections, peerName)
		m.mu.Unlock()
		conn.Close()
	}()

	// Read and route messages
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}

		if len(data) < 1 {
			continue
		}

		msgType := data[0]
		switch msgType {
		case MsgTypeSendPacket:
			if len(data) < 3 {
				continue
			}
			targetLen := int(data[1])
			if len(data) < 2+targetLen {
				continue
			}
			targetPeer := string(data[2 : 2+targetLen])
			packetData := data[2+targetLen:]

			m.received <- relayMessage{
				source: peerName,
				target: targetPeer,
				data:   packetData,
			}

			// Route to target if connected
			m.mu.Lock()
			targetConn, ok := m.connections[targetPeer]
			m.mu.Unlock()

			if ok {
				// Build recv message
				msg := make([]byte, 2+len(peerName)+len(packetData))
				msg[0] = MsgTypeRecvPacket
				msg[1] = byte(len(peerName))
				copy(msg[2:], peerName)
				copy(msg[2+len(peerName):], packetData)
				_ = targetConn.WriteMessage(websocket.BinaryMessage, msg)
			}

		case MsgTypeHeartbeat:
			// Record the heartbeat
			m.heartbeats <- heartbeatMessage{
				peerName: peerName,
				data:     data[1:], // Skip message type byte
			}
			// Send ack
			_ = conn.WriteMessage(websocket.BinaryMessage, []byte{MsgTypeHeartbeatAck})
		}
	}
}

// sendRelayNotify sends a relay notification to a connected peer.
func (m *mockRelayServer) sendRelayNotify(peerName string, waitingPeers []string) error {
	m.mu.Lock()
	conn, ok := m.connections[peerName]
	m.mu.Unlock()

	if !ok {
		return nil // Peer not connected
	}

	// Build message: [MsgTypeRelayNotify][count:1][name_len:1][name]...
	msgLen := 2 // type + count
	for _, p := range waitingPeers {
		msgLen += 1 + len(p) // name_len + name
	}

	msg := make([]byte, msgLen)
	msg[0] = MsgTypeRelayNotify
	msg[1] = byte(len(waitingPeers))
	offset := 2
	for _, p := range waitingPeers {
		msg[offset] = byte(len(p))
		copy(msg[offset+1:], p)
		offset += 1 + len(p)
	}

	return conn.WriteMessage(websocket.BinaryMessage, msg)
}

// sendHolePunchNotify sends a hole-punch notification to a connected peer.
func (m *mockRelayServer) sendHolePunchNotify(peerName string, requestingPeers []string) error {
	m.mu.Lock()
	conn, ok := m.connections[peerName]
	m.mu.Unlock()

	if !ok {
		return nil // Peer not connected
	}

	// Build message: [MsgTypeHolePunchNotify][count:1][name_len:1][name]...
	msgLen := 2 // type + count
	for _, p := range requestingPeers {
		msgLen += 1 + len(p) // name_len + name
	}

	msg := make([]byte, msgLen)
	msg[0] = MsgTypeHolePunchNotify
	msg[1] = byte(len(requestingPeers))
	offset := 2
	for _, p := range requestingPeers {
		msg[offset] = byte(len(p))
		copy(msg[offset+1:], p)
		offset += 1 + len(p)
	}

	return conn.WriteMessage(websocket.BinaryMessage, msg)
}

func (m *mockRelayServer) URL() string {
	return m.server.URL
}

func (m *mockRelayServer) Close() {
	m.server.Close()
}

func TestPersistentRelay_Connect(t *testing.T) {
	server := newMockRelayServer(t)
	defer server.Close()

	relay := NewPersistentRelay(server.URL(), "peer1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := relay.Connect(ctx)
	require.NoError(t, err)
	assert.True(t, relay.IsConnected())

	relay.Close()
	assert.False(t, relay.IsConnected())
}

func TestPersistentRelay_SendTo(t *testing.T) {
	server := newMockRelayServer(t)
	defer server.Close()

	relay := NewPersistentRelay(server.URL(), "peer1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := relay.Connect(ctx)
	require.NoError(t, err)
	defer relay.Close()

	// Send a packet
	testData := []byte("hello world")
	err = relay.SendTo("peer2", testData)
	require.NoError(t, err)

	// Verify server received it
	select {
	case msg := <-server.received:
		assert.Equal(t, "peer1", msg.source)
		assert.Equal(t, "peer2", msg.target)
		assert.Equal(t, testData, msg.data)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestPersistentRelay_ReceivePacket(t *testing.T) {
	server := newMockRelayServer(t)
	defer server.Close()

	// Connect two peers
	relay1 := NewPersistentRelay(server.URL(), "peer1")
	relay2 := NewPersistentRelay(server.URL(), "peer2")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := relay1.Connect(ctx)
	require.NoError(t, err)
	defer relay1.Close()

	err = relay2.Connect(ctx)
	require.NoError(t, err)
	defer relay2.Close()

	// Set up packet handler on peer2
	received := make(chan []byte, 1)
	relay2.SetPacketHandler(func(sourcePeer string, data []byte) {
		assert.Equal(t, "peer1", sourcePeer)
		received <- data
	})

	// Give time for both connections to be established
	time.Sleep(100 * time.Millisecond)

	// Send from peer1 to peer2
	testData := []byte("hello from peer1")
	err = relay1.SendTo("peer2", testData)
	require.NoError(t, err)

	// Verify peer2 received it
	select {
	case data := <-received:
		assert.Equal(t, testData, data)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for packet")
	}
}

func TestPersistentRelay_SendNotConnected(t *testing.T) {
	relay := NewPersistentRelay("http://localhost:9999", "peer1")

	err := relay.SendTo("peer2", []byte("test"))
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotConnected))
}

func TestPeerTunnel_ReadWrite(t *testing.T) {
	server := newMockRelayServer(t)
	defer server.Close()

	// Connect two peers
	relay1 := NewPersistentRelay(server.URL(), "peer1")
	relay2 := NewPersistentRelay(server.URL(), "peer2")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := relay1.Connect(ctx)
	require.NoError(t, err)
	defer relay1.Close()

	err = relay2.Connect(ctx)
	require.NoError(t, err)
	defer relay2.Close()

	// Give time for connections
	time.Sleep(100 * time.Millisecond)

	// Create peer tunnels
	tunnel1to2 := relay1.NewPeerTunnel("peer2")
	tunnel2to1 := relay2.NewPeerTunnel("peer1")

	// Write from tunnel1 to tunnel2
	testData := []byte("hello via peer tunnel")
	n, err := tunnel1to2.Write(testData)
	require.NoError(t, err)
	assert.Equal(t, len(testData), n)

	// Read on tunnel2
	buf := make([]byte, 100)
	n, err = tunnel2to1.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, testData, buf[:n])
}

func TestPeerTunnel_Close(t *testing.T) {
	relay := NewPersistentRelay("http://localhost:9999", "peer1")
	tunnel := relay.NewPeerTunnel("peer2")

	assert.False(t, tunnel.IsClosed())

	err := tunnel.Close()
	require.NoError(t, err)

	assert.True(t, tunnel.IsClosed())

	// Write should fail after close
	_, err = tunnel.Write([]byte("test"))
	assert.Error(t, err)
}

// --- Tests for heartbeat and push notification message types ---

func TestPersistentRelay_SendHeartbeat(t *testing.T) {
	server := newMockRelayServer(t)
	defer server.Close()

	relay := NewPersistentRelay(server.URL(), "peer1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := relay.Connect(ctx)
	require.NoError(t, err)
	defer relay.Close()

	// Send a heartbeat with stats
	stats := &proto.PeerStats{
		PacketsSent:     100,
		PacketsReceived: 50,
		BytesSent:       5000,
		BytesReceived:   2500,
		ActiveTunnels:   2,
	}
	err = relay.SendHeartbeat(stats)
	require.NoError(t, err)

	// Verify server received the heartbeat
	select {
	case msg := <-server.heartbeats:
		assert.Equal(t, "peer1", msg.peerName)
		// Data should be JSON-encoded stats
		assert.True(t, len(msg.data) > 0)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for heartbeat")
	}
}

func TestPersistentRelay_SendHeartbeat_NotConnected(t *testing.T) {
	relay := NewPersistentRelay("http://localhost:9999", "peer1")

	stats := &proto.PeerStats{PacketsSent: 100}
	err := relay.SendHeartbeat(stats)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotConnected))
}

func TestPersistentRelay_ReceiveRelayNotify(t *testing.T) {
	server := newMockRelayServer(t)
	defer server.Close()

	relay := NewPersistentRelay(server.URL(), "peer1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := relay.Connect(ctx)
	require.NoError(t, err)
	defer relay.Close()

	// Set up callback
	received := make(chan []string, 1)
	relay.SetRelayNotifyHandler(func(peers []string) {
		received <- peers
	})

	// Give time for connection to be ready
	time.Sleep(100 * time.Millisecond)

	// Server pushes relay notification
	err = server.sendRelayNotify("peer1", []string{"peer2", "peer3"})
	require.NoError(t, err)

	// Verify callback received it
	select {
	case peers := <-received:
		assert.Equal(t, []string{"peer2", "peer3"}, peers)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for relay notify")
	}
}

func TestPersistentRelay_ReceiveHolePunchNotify(t *testing.T) {
	server := newMockRelayServer(t)
	defer server.Close()

	relay := NewPersistentRelay(server.URL(), "peer1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := relay.Connect(ctx)
	require.NoError(t, err)
	defer relay.Close()

	// Set up callback
	received := make(chan []string, 1)
	relay.SetHolePunchNotifyHandler(func(peers []string) {
		received <- peers
	})

	// Give time for connection to be ready
	time.Sleep(100 * time.Millisecond)

	// Server pushes hole-punch notification
	err = server.sendHolePunchNotify("peer1", []string{"peer4"})
	require.NoError(t, err)

	// Verify callback received it
	select {
	case peers := <-received:
		assert.Equal(t, []string{"peer4"}, peers)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for hole-punch notify")
	}
}

func TestPersistentRelay_ReceiveRelayNotify_NoHandler(t *testing.T) {
	server := newMockRelayServer(t)
	defer server.Close()

	relay := NewPersistentRelay(server.URL(), "peer1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := relay.Connect(ctx)
	require.NoError(t, err)
	defer relay.Close()

	// No handler set - should not panic
	time.Sleep(100 * time.Millisecond)

	err = server.sendRelayNotify("peer1", []string{"peer2"})
	require.NoError(t, err)

	// Give time for message to be processed
	time.Sleep(100 * time.Millisecond)
	// Test passes if no panic
}
