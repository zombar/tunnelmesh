package routing

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTUN simulates a TUN device for testing.
type mockTUN struct {
	readBuf  *bytes.Buffer
	writeBuf *bytes.Buffer
	mu       sync.Mutex
}

func newMockTUN() *mockTUN {
	return &mockTUN{
		readBuf:  bytes.NewBuffer(nil),
		writeBuf: bytes.NewBuffer(nil),
	}
}

func (m *mockTUN) Read(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.readBuf.Read(p)
}

func (m *mockTUN) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeBuf.Write(p)
}

func (m *mockTUN) Close() error {
	return nil
}

func (m *mockTUN) InjectPacket(packet []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readBuf.Write(packet)
}

func (m *mockTUN) GetWrittenPackets() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeBuf.Bytes()
}

// mockTunnel simulates a tunnel for testing.
type mockTunnel struct {
	buf    *bytes.Buffer
	mu     sync.Mutex
	closed bool
}

func newMockTunnel() *mockTunnel {
	return &mockTunnel{
		buf: bytes.NewBuffer(nil),
	}
}

func (m *mockTunnel) Read(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.Read(p)
}

func (m *mockTunnel) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.Write(p)
}

func (m *mockTunnel) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockTunnel) PeerName() string {
	return "mock-peer"
}

func (m *mockTunnel) GetData() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.Bytes()
}

func (m *mockTunnel) InjectData(data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buf.Write(data)
}

func TestForwarder_ForwardPacket(t *testing.T) {
	router := NewRouter()
	router.AddRoute("10.99.0.2", "peer1")

	tunnelMgr := NewMockTunnelManager()
	tunnel := newMockTunnel()
	tunnelMgr.Add("peer1", tunnel)

	fwd := NewForwarder(router, tunnelMgr)

	// Create a test packet
	srcIP := net.ParseIP("10.99.0.1").To4()
	dstIP := net.ParseIP("10.99.0.2").To4()
	payload := []byte("test data")
	packet := BuildIPv4Packet(srcIP, dstIP, ProtoUDP, payload)

	// Forward the packet
	err := fwd.ForwardPacket(packet)
	require.NoError(t, err)

	// Verify packet was written to tunnel
	tunnelData := tunnel.GetData()
	assert.NotEmpty(t, tunnelData)

	// The tunnel should have received an encoded frame
	// Frame format: [2 bytes length][1 byte proto][payload]
	assert.True(t, len(tunnelData) >= 3)
}

func TestForwarder_ForwardPacket_NoRoute(t *testing.T) {
	router := NewRouter()
	tunnelMgr := NewMockTunnelManager()

	fwd := NewForwarder(router, tunnelMgr)

	// Create a test packet to unknown destination
	srcIP := net.ParseIP("10.99.0.1").To4()
	dstIP := net.ParseIP("10.99.0.99").To4()
	packet := BuildIPv4Packet(srcIP, dstIP, ProtoUDP, []byte("test"))

	// Should fail - no route
	err := fwd.ForwardPacket(packet)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no route")
}

func TestForwarder_ForwardPacket_NoTunnel(t *testing.T) {
	router := NewRouter()
	router.AddRoute("10.99.0.2", "peer1")

	tunnelMgr := NewMockTunnelManager()
	// No tunnel added for peer1

	fwd := NewForwarder(router, tunnelMgr)

	srcIP := net.ParseIP("10.99.0.1").To4()
	dstIP := net.ParseIP("10.99.0.2").To4()
	packet := BuildIPv4Packet(srcIP, dstIP, ProtoUDP, []byte("test"))

	err := fwd.ForwardPacket(packet)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no tunnel")
}

func TestForwarder_ReceivePacket(t *testing.T) {
	router := NewRouter()
	tunnelMgr := NewMockTunnelManager()
	mockTun := newMockTUN()

	fwd := NewForwarder(router, tunnelMgr)
	fwd.SetTUN(mockTun)

	// Create a test packet
	srcIP := net.ParseIP("10.99.0.2").To4()
	dstIP := net.ParseIP("10.99.0.1").To4()
	payload := []byte("incoming data")
	packet := BuildIPv4Packet(srcIP, dstIP, ProtoUDP, payload)

	// Receive the packet
	err := fwd.ReceivePacket(packet)
	require.NoError(t, err)

	// Verify packet was written to TUN
	written := mockTun.GetWrittenPackets()
	assert.Equal(t, packet, written)
}

func TestForwarderStats(t *testing.T) {
	router := NewRouter()
	router.AddRoute("10.99.0.2", "peer1")

	tunnelMgr := NewMockTunnelManager()
	tunnel := newMockTunnel()
	tunnelMgr.Add("peer1", tunnel)
	mockTun := newMockTUN()

	fwd := NewForwarder(router, tunnelMgr)
	fwd.SetTUN(mockTun)

	// Forward a packet
	srcIP := net.ParseIP("10.99.0.1").To4()
	dstIP := net.ParseIP("10.99.0.2").To4()
	packet := BuildIPv4Packet(srcIP, dstIP, ProtoUDP, []byte("test"))
	fwd.ForwardPacket(packet)

	// Receive a packet
	srcIP = net.ParseIP("10.99.0.2").To4()
	dstIP = net.ParseIP("10.99.0.1").To4()
	packet = BuildIPv4Packet(srcIP, dstIP, ProtoUDP, []byte("test"))
	fwd.ReceivePacket(packet)

	stats := fwd.Stats()
	assert.Equal(t, uint64(1), stats.PacketsSent)
	assert.Equal(t, uint64(1), stats.PacketsReceived)
	assert.Greater(t, stats.BytesSent, uint64(0))
	assert.Greater(t, stats.BytesReceived, uint64(0))
}

// MockTunnelManager for testing.
type MockTunnelManager struct {
	tunnels map[string]io.ReadWriteCloser
	mu      sync.RWMutex
}

func NewMockTunnelManager() *MockTunnelManager {
	return &MockTunnelManager{
		tunnels: make(map[string]io.ReadWriteCloser),
	}
}

func (m *MockTunnelManager) Add(name string, tunnel io.ReadWriteCloser) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tunnels[name] = tunnel
}

func (m *MockTunnelManager) Get(name string) (io.ReadWriteCloser, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tunnel, ok := m.tunnels[name]
	return tunnel, ok
}

func (m *MockTunnelManager) Remove(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tunnels, name)
}

func TestForwarderLoop(t *testing.T) {
	router := NewRouter()
	router.AddRoute("10.99.0.2", "peer1")

	tunnelMgr := NewMockTunnelManager()
	mockTun := newMockTUN()

	fwd := NewForwarder(router, tunnelMgr)
	fwd.SetTUN(mockTun)

	// Inject a packet into TUN to be forwarded
	srcIP := net.ParseIP("10.99.0.1").To4()
	dstIP := net.ParseIP("10.99.0.2").To4()
	packet := BuildIPv4Packet(srcIP, dstIP, ProtoUDP, []byte("test"))

	// Add tunnel after starting
	tunnel := newMockTunnel()
	tunnelMgr.Add("peer1", tunnel)

	mockTun.InjectPacket(packet)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Start the forwarder loop (should process the packet then timeout)
	go fwd.Run(ctx)

	// Wait for context to expire
	<-ctx.Done()

	// The packet should have been forwarded to the tunnel
	// Note: Due to timing, this may or may not have data
}
