package coord

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

// Helper to create a WebSocket connection to the relay endpoint
func connectRelay(t *testing.T, serverURL, peerName, jwtToken string) *websocket.Conn {
	// Convert http:// to ws://
	wsURL := strings.Replace(serverURL, "http://", "ws://", 1) + "/api/v1/relay/persistent"

	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+jwtToken)

	conn, _, err := dialer.Dial(wsURL, headers)
	require.NoError(t, err, "failed to connect to relay")

	return conn
}

func TestRelayManager_HandleHeartbeat(t *testing.T) {
	cfg := &config.ServerConfig{
		Listen:       ":0",
		AuthToken:    "test-token",
		MeshCIDR:     "172.30.0.0/16",
		DomainSuffix: ".tunnelmesh",
		Relay:        config.RelayConfig{Enabled: true},
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)

	// Start test server
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Register a peer first to get a JWT token
	peerName := "test-peer"
	jwtToken := registerPeerAndGetToken(t, ts.URL, peerName, cfg.AuthToken)

	// Connect to persistent relay
	conn := connectRelay(t, ts.URL, peerName, jwtToken)
	defer func() { _ = conn.Close() }()

	// Give server time to register the connection
	time.Sleep(50 * time.Millisecond)

	// Send heartbeat with stats
	stats := &proto.PeerStats{
		PacketsSent:     100,
		PacketsReceived: 50,
		BytesSent:       5000,
		BytesReceived:   2500,
		ActiveTunnels:   2,
	}
	statsJSON, _ := json.Marshal(stats)

	// Build message: [MsgTypeHeartbeat][stats_len:2][stats JSON]
	msg := make([]byte, 1+2+len(statsJSON))
	msg[0] = MsgTypeHeartbeat
	msg[1] = byte(len(statsJSON) >> 8)
	msg[2] = byte(len(statsJSON))
	copy(msg[3:], statsJSON)

	err = conn.WriteMessage(websocket.BinaryMessage, msg)
	require.NoError(t, err)

	// Read heartbeat ack
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, ackData, err := conn.ReadMessage()
	require.NoError(t, err)

	assert.Equal(t, MsgTypeHeartbeatAck, ackData[0], "should receive heartbeat ack")

	// Verify peer stats were updated
	srv.peersMu.RLock()
	peer, exists := srv.peers[peerName]
	srv.peersMu.RUnlock()

	require.True(t, exists, "peer should exist")
	assert.WithinDuration(t, time.Now(), peer.peer.LastSeen, 2*time.Second, "LastSeen should be updated")
	if peer.stats != nil {
		assert.Equal(t, uint64(100), peer.stats.PacketsSent, "stats should be updated")
	}
}

func TestRelayManager_NotifyRelayRequest(t *testing.T) {
	cfg := &config.ServerConfig{
		Listen:       ":0",
		AuthToken:    "test-token",
		MeshCIDR:     "172.30.0.0/16",
		DomainSuffix: ".tunnelmesh",
		Relay:        config.RelayConfig{Enabled: true},
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Register peer
	peerName := "test-peer"
	jwtToken := registerPeerAndGetToken(t, ts.URL, peerName, cfg.AuthToken)

	// Connect to persistent relay
	conn := connectRelay(t, ts.URL, peerName, jwtToken)
	defer func() { _ = conn.Close() }()

	// Give server time to register the connection
	time.Sleep(50 * time.Millisecond)

	// Call NotifyRelayRequest on the server
	waitingPeers := []string{"peer2", "peer3"}
	srv.relay.NotifyRelayRequest(peerName, waitingPeers)

	// Read notification
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, data, err := conn.ReadMessage()
	require.NoError(t, err)

	// Parse notification
	assert.Equal(t, MsgTypeRelayNotify, data[0], "should receive relay notify")
	count := int(data[1])
	assert.Equal(t, 2, count, "should have 2 peers")

	// Parse peer names
	peers := parsePeerList(data[2:], count)
	assert.Equal(t, []string{"peer2", "peer3"}, peers)
}

func TestRelayManager_NotifyHolePunch(t *testing.T) {
	cfg := &config.ServerConfig{
		Listen:       ":0",
		AuthToken:    "test-token",
		MeshCIDR:     "172.30.0.0/16",
		DomainSuffix: ".tunnelmesh",
		Relay:        config.RelayConfig{Enabled: true},
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Register peer
	peerName := "test-peer"
	jwtToken := registerPeerAndGetToken(t, ts.URL, peerName, cfg.AuthToken)

	// Connect to persistent relay
	conn := connectRelay(t, ts.URL, peerName, jwtToken)
	defer func() { _ = conn.Close() }()

	time.Sleep(50 * time.Millisecond)

	// Call NotifyHolePunch
	requestingPeers := []string{"peer4"}
	srv.relay.NotifyHolePunch(peerName, requestingPeers)

	// Read notification
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, data, err := conn.ReadMessage()
	require.NoError(t, err)

	assert.Equal(t, MsgTypeHolePunchNotify, data[0], "should receive hole-punch notify")
	count := int(data[1])
	assert.Equal(t, 1, count, "should have 1 peer")

	peers := parsePeerList(data[2:], count)
	assert.Equal(t, []string{"peer4"}, peers)
}

func TestRelayManager_NotifyPeerNotConnected(t *testing.T) {
	cfg := &config.ServerConfig{
		Listen:       ":0",
		AuthToken:    "test-token",
		MeshCIDR:     "172.30.0.0/16",
		DomainSuffix: ".tunnelmesh",
		Relay:        config.RelayConfig{Enabled: true},
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)

	// Notify a peer that's not connected - should not panic
	srv.relay.NotifyRelayRequest("nonexistent-peer", []string{"peer2"})
	srv.relay.NotifyHolePunch("nonexistent-peer", []string{"peer2"})
	// Test passes if no panic
}

func TestRelayManager_HeartbeatUpdatesStats(t *testing.T) {
	cfg := &config.ServerConfig{
		Listen:       ":0",
		AuthToken:    "test-token",
		MeshCIDR:     "172.30.0.0/16",
		DomainSuffix: ".tunnelmesh",
		Relay:        config.RelayConfig{Enabled: true},
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	peerName := "test-peer"
	jwtToken := registerPeerAndGetToken(t, ts.URL, peerName, cfg.AuthToken)

	conn := connectRelay(t, ts.URL, peerName, jwtToken)
	defer func() { _ = conn.Close() }()

	time.Sleep(50 * time.Millisecond)

	// Send multiple heartbeats with different stats
	for i := 1; i <= 3; i++ {
		stats := &proto.PeerStats{
			PacketsSent: uint64(i * 100),
			BytesSent:   uint64(i * 1000),
		}
		statsJSON, _ := json.Marshal(stats)

		msg := make([]byte, 1+2+len(statsJSON))
		msg[0] = MsgTypeHeartbeat
		msg[1] = byte(len(statsJSON) >> 8)
		msg[2] = byte(len(statsJSON))
		copy(msg[3:], statsJSON)

		err = conn.WriteMessage(websocket.BinaryMessage, msg)
		require.NoError(t, err)

		// Read ack
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
		_, _, err = conn.ReadMessage()
		require.NoError(t, err)
	}

	// Verify final stats
	srv.peersMu.RLock()
	peer := srv.peers[peerName]
	srv.peersMu.RUnlock()

	if peer.stats != nil {
		assert.Equal(t, uint64(300), peer.stats.PacketsSent, "stats should reflect latest heartbeat")
	}
}

// --- Helper functions ---

func registerPeerAndGetToken(t *testing.T, serverURL, peerName, authToken string) string {
	regReq := proto.RegisterRequest{
		Name:       peerName,
		PublicKey:  "SHA256:abc123",
		PublicIPs:  []string{"1.2.3.4"},
		PrivateIPs: []string{"192.168.1.100"},
		SSHPort:    2222,
	}
	body, _ := json.Marshal(regReq)

	req, _ := http.NewRequest(http.MethodPost, serverURL+"/api/v1/register", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var regResp proto.RegisterResponse
	err = json.NewDecoder(resp.Body).Decode(&regResp)
	require.NoError(t, err)

	return regResp.Token
}

func parsePeerList(data []byte, count int) []string {
	peers := make([]string, 0, count)
	offset := 0
	for i := 0; i < count; i++ {
		if offset >= len(data) {
			break
		}
		nameLen := int(data[offset])
		if offset+1+nameLen > len(data) {
			break
		}
		peers = append(peers, string(data[offset+1:offset+1+nameLen]))
		offset += 1 + nameLen
	}
	return peers
}

// Ensure sync.WaitGroup is used (for compiler)
var _ = sync.WaitGroup{}

// --- RTT and latency tests ---

func TestRelayManager_HeartbeatAckEchoesTimestamp(t *testing.T) {
	cfg := &config.ServerConfig{
		Listen:       ":0",
		AuthToken:    "test-token",
		MeshCIDR:     "172.30.0.0/16",
		DomainSuffix: ".tunnelmesh",
		Relay:        config.RelayConfig{Enabled: true},
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	peerName := "test-peer"
	jwtToken := registerPeerAndGetToken(t, ts.URL, peerName, cfg.AuthToken)

	conn := connectRelay(t, ts.URL, peerName, jwtToken)
	defer func() { _ = conn.Close() }()

	time.Sleep(50 * time.Millisecond)

	// Send heartbeat with HeartbeatSentAt timestamp
	sentAt := time.Now().UnixNano()
	stats := &proto.PeerStats{
		PacketsSent:     100,
		ActiveTunnels:   2,
		HeartbeatSentAt: sentAt,
	}
	statsJSON, _ := json.Marshal(stats)

	msg := make([]byte, 1+2+len(statsJSON))
	msg[0] = MsgTypeHeartbeat
	msg[1] = byte(len(statsJSON) >> 8)
	msg[2] = byte(len(statsJSON))
	copy(msg[3:], statsJSON)

	err = conn.WriteMessage(websocket.BinaryMessage, msg)
	require.NoError(t, err)

	// Read heartbeat ack
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, ackData, err := conn.ReadMessage()
	require.NoError(t, err)

	// Ack should be 9 bytes: [MsgTypeHeartbeatAck][timestamp:8]
	assert.Equal(t, MsgTypeHeartbeatAck, ackData[0], "should receive heartbeat ack")
	require.Len(t, ackData, 9, "ack should include echoed timestamp")

	// Parse echoed timestamp
	echoedTimestamp := int64(binary.BigEndian.Uint64(ackData[1:9]))
	assert.Equal(t, sentAt, echoedTimestamp, "echoed timestamp should match sent timestamp")
}

func TestRelayManager_HeartbeatAckWithoutTimestamp(t *testing.T) {
	cfg := &config.ServerConfig{
		Listen:       ":0",
		AuthToken:    "test-token",
		MeshCIDR:     "172.30.0.0/16",
		DomainSuffix: ".tunnelmesh",
		Relay:        config.RelayConfig{Enabled: true},
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	peerName := "test-peer"
	jwtToken := registerPeerAndGetToken(t, ts.URL, peerName, cfg.AuthToken)

	conn := connectRelay(t, ts.URL, peerName, jwtToken)
	defer func() { _ = conn.Close() }()

	time.Sleep(50 * time.Millisecond)

	// Send heartbeat WITHOUT HeartbeatSentAt (simulating old client)
	stats := &proto.PeerStats{
		PacketsSent:   100,
		ActiveTunnels: 2,
		// HeartbeatSentAt is 0 (not set)
	}
	statsJSON, _ := json.Marshal(stats)

	msg := make([]byte, 1+2+len(statsJSON))
	msg[0] = MsgTypeHeartbeat
	msg[1] = byte(len(statsJSON) >> 8)
	msg[2] = byte(len(statsJSON))
	copy(msg[3:], statsJSON)

	err = conn.WriteMessage(websocket.BinaryMessage, msg)
	require.NoError(t, err)

	// Read heartbeat ack
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, ackData, err := conn.ReadMessage()
	require.NoError(t, err)

	// Ack should be 1 byte for backwards compatibility
	assert.Equal(t, MsgTypeHeartbeatAck, ackData[0], "should receive heartbeat ack")
	assert.Len(t, ackData, 1, "ack should be 1 byte for old clients without timestamp")
}

func TestRelayManager_StoresReportedLatency(t *testing.T) {
	cfg := &config.ServerConfig{
		Listen:       ":0",
		AuthToken:    "test-token",
		MeshCIDR:     "172.30.0.0/16",
		DomainSuffix: ".tunnelmesh",
		Relay:        config.RelayConfig{Enabled: true},
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	peerName := "test-peer"
	jwtToken := registerPeerAndGetToken(t, ts.URL, peerName, cfg.AuthToken)

	conn := connectRelay(t, ts.URL, peerName, jwtToken)
	defer func() { _ = conn.Close() }()

	time.Sleep(50 * time.Millisecond)

	// Send heartbeat with RTT and peer latencies
	stats := &proto.PeerStats{
		PacketsSent:      100,
		ActiveTunnels:    2,
		HeartbeatSentAt:  time.Now().UnixNano(),
		CoordinatorRTTMs: 42,
		PeerLatencies: map[string]int64{
			"peer-a": 15,
			"peer-b": 28,
		},
	}
	statsJSON, _ := json.Marshal(stats)

	msg := make([]byte, 1+2+len(statsJSON))
	msg[0] = MsgTypeHeartbeat
	msg[1] = byte(len(statsJSON) >> 8)
	msg[2] = byte(len(statsJSON))
	copy(msg[3:], statsJSON)

	err = conn.WriteMessage(websocket.BinaryMessage, msg)
	require.NoError(t, err)

	// Read ack
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, _, err = conn.ReadMessage()
	require.NoError(t, err)

	// Verify peer info stores the latency data
	srv.peersMu.RLock()
	peer := srv.peers[peerName]
	srv.peersMu.RUnlock()

	require.NotNil(t, peer, "peer should exist")
	assert.Equal(t, int64(42), peer.coordinatorRTT, "coordinator RTT should be stored")
	require.NotNil(t, peer.peerLatencies, "peer latencies should be stored")
	assert.Equal(t, int64(15), peer.peerLatencies["peer-a"])
	assert.Equal(t, int64(28), peer.peerLatencies["peer-b"])
}
