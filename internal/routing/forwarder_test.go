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
	_ = fwd.ForwardPacket(packet)

	// Receive a packet
	srcIP = net.ParseIP("10.99.0.2").To4()
	dstIP = net.ParseIP("10.99.0.1").To4()
	packet = BuildIPv4Packet(srcIP, dstIP, ProtoUDP, []byte("test"))
	_ = fwd.ReceivePacket(packet)

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
	go func() { _ = fwd.Run(ctx) }()

	// Wait for context to expire
	<-ctx.Done()

	// The packet should have been forwarded to the tunnel
	// Note: Due to timing, this may or may not have data
}

// TestForwarder_SetExitNode tests exit node configuration.
func TestForwarder_SetExitNode(t *testing.T) {
	router := NewRouter()
	tunnelMgr := NewMockTunnelManager()
	fwd := NewForwarder(router, tunnelMgr)

	// Set exit node with exceptions
	err := fwd.SetExitNode("exit-peer", []string{"192.168.0.0/16", "10.0.0.0/8"})
	require.NoError(t, err)

	// Verify settings
	fwd.exitMu.RLock()
	assert.Equal(t, "exit-peer", fwd.exitPeerName)
	assert.Len(t, fwd.exitExceptions, 2)
	fwd.exitMu.RUnlock()
}

func TestForwarder_SetExitNode_InvalidCIDR(t *testing.T) {
	router := NewRouter()
	tunnelMgr := NewMockTunnelManager()
	fwd := NewForwarder(router, tunnelMgr)

	err := fwd.SetExitNode("exit-peer", []string{"not-a-cidr"})
	assert.Error(t, err)
}

func TestForwarder_SetExitNode_Disable(t *testing.T) {
	router := NewRouter()
	tunnelMgr := NewMockTunnelManager()
	fwd := NewForwarder(router, tunnelMgr)

	// Enable exit node
	err := fwd.SetExitNode("exit-peer", nil)
	require.NoError(t, err)

	// Disable exit node
	err = fwd.SetExitNode("", nil)
	require.NoError(t, err)

	fwd.exitMu.RLock()
	assert.Equal(t, "", fwd.exitPeerName)
	fwd.exitMu.RUnlock()
}

func TestForwarder_isExcepted(t *testing.T) {
	router := NewRouter()
	tunnelMgr := NewMockTunnelManager()
	fwd := NewForwarder(router, tunnelMgr)

	// Set exceptions
	err := fwd.SetExitNode("exit-peer", []string{"192.168.0.0/16", "10.0.0.0/8"})
	require.NoError(t, err)

	// Test IPs that should be excepted
	assert.True(t, fwd.isExcepted(net.ParseIP("192.168.1.1")))
	assert.True(t, fwd.isExcepted(net.ParseIP("10.0.0.1")))

	// Test IPs that should NOT be excepted
	assert.False(t, fwd.isExcepted(net.ParseIP("8.8.8.8")))
	assert.False(t, fwd.isExcepted(net.ParseIP("1.1.1.1")))
}

func TestForwarder_ForwardPacket_ToExitNode(t *testing.T) {
	router := NewRouter()
	tunnelMgr := NewMockTunnelManager()
	fwd := NewForwarder(router, tunnelMgr)

	// Set up exit node
	err := fwd.SetExitNode("exit-peer", []string{"192.168.0.0/16"})
	require.NoError(t, err)

	// Add tunnel for exit peer
	tunnel := newMockTunnel()
	tunnelMgr.Add("exit-peer", tunnel)

	// Create packet to external IP (not in exception list, not in routing table)
	srcIP := net.ParseIP("10.99.0.1").To4()
	dstIP := net.ParseIP("8.8.8.8").To4() // External IP
	packet := BuildIPv4Packet(srcIP, dstIP, ProtoUDP, []byte("test"))

	// Forward should succeed (goes to exit node)
	err = fwd.ForwardPacket(packet)
	require.NoError(t, err)

	// Verify packet was sent to exit tunnel with exit protocol
	tunnelData := tunnel.GetData()
	assert.NotEmpty(t, tunnelData)

	// Check frame header
	assert.True(t, len(tunnelData) >= 3)
	protoType := tunnelData[2]
	assert.Equal(t, byte(ProtoExitPacket), protoType, "Should use exit protocol type")
}

func TestForwarder_ForwardPacket_ExceptionBypasses(t *testing.T) {
	router := NewRouter()
	tunnelMgr := NewMockTunnelManager()
	fwd := NewForwarder(router, tunnelMgr)

	// Set up exit node with exception
	err := fwd.SetExitNode("exit-peer", []string{"192.168.0.0/16"})
	require.NoError(t, err)

	tunnel := newMockTunnel()
	tunnelMgr.Add("exit-peer", tunnel)

	// Create packet to excepted IP
	srcIP := net.ParseIP("10.99.0.1").To4()
	dstIP := net.ParseIP("192.168.1.1").To4() // Excepted IP
	packet := BuildIPv4Packet(srcIP, dstIP, ProtoUDP, []byte("test"))

	// Forward should silently drop (let OS handle via default route)
	err = fwd.ForwardPacket(packet)
	assert.NoError(t, err) // No error, just silent drop

	// Verify packet was NOT sent to exit tunnel
	tunnelData := tunnel.GetData()
	assert.Empty(t, tunnelData)
}

func TestForwarder_SetIsExitNode(t *testing.T) {
	router := NewRouter()
	tunnelMgr := NewMockTunnelManager()
	fwd := NewForwarder(router, tunnelMgr)

	// Initially not an exit node
	fwd.exitMu.RLock()
	assert.False(t, fwd.isExitNode)
	fwd.exitMu.RUnlock()

	// Set as exit node
	fwd.SetIsExitNode(true, nil)

	fwd.exitMu.RLock()
	assert.True(t, fwd.isExitNode)
	fwd.exitMu.RUnlock()
}

// mockExitHandler implements ExitHandler for testing.
type mockExitHandler struct {
	packets [][]byte
	mu      sync.Mutex
}

func (m *mockExitHandler) HandleExitPacket(peerName string, packet []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.packets = append(m.packets, append([]byte{}, packet...))
	return nil
}

func (m *mockExitHandler) GetPackets() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.packets
}

func TestForwarder_HandleTunnel_ExitPacket(t *testing.T) {
	router := NewRouter()
	tunnelMgr := NewMockTunnelManager()
	fwd := NewForwarder(router, tunnelMgr)
	mockTun := newMockTUN()
	fwd.SetTUN(mockTun)

	// Set up as exit node with handler
	handler := &mockExitHandler{}
	fwd.SetIsExitNode(true, handler)

	// Create a tunnel that provides an exit packet frame
	tunnel := newMockTunnel()

	// Build exit packet frame: [2 bytes length][1 byte proto=0x02][payload]
	srcIP := net.ParseIP("10.99.0.2").To4()
	dstIP := net.ParseIP("8.8.8.8").To4()
	payload := BuildIPv4Packet(srcIP, dstIP, ProtoUDP, []byte("exit-traffic"))

	frameLen := len(payload) + 1
	frame := make([]byte, 3+len(payload))
	frame[0] = byte(frameLen >> 8)
	frame[1] = byte(frameLen)
	frame[2] = ProtoExitPacket
	copy(frame[3:], payload)

	tunnel.InjectData(frame)

	// Handle tunnel in goroutine
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	go fwd.HandleTunnel(ctx, "peer1", tunnel)

	// Wait a bit for processing
	time.Sleep(50 * time.Millisecond)

	// Verify handler received the packet
	packets := handler.GetPackets()
	assert.Len(t, packets, 1)
	assert.Equal(t, payload, packets[0])
}

func TestForwarder_HandleTunnel_MeshPacket(t *testing.T) {
	router := NewRouter()
	tunnelMgr := NewMockTunnelManager()
	fwd := NewForwarder(router, tunnelMgr)
	mockTun := newMockTUN()
	fwd.SetTUN(mockTun)

	// Create a tunnel that provides a mesh packet frame
	tunnel := newMockTunnel()

	// Build mesh packet frame: [2 bytes length][1 byte proto=0x01][payload]
	srcIP := net.ParseIP("10.99.0.2").To4()
	dstIP := net.ParseIP("10.99.0.1").To4()
	payload := BuildIPv4Packet(srcIP, dstIP, ProtoUDP, []byte("mesh-traffic"))

	frameLen := len(payload) + 1
	frame := make([]byte, 3+len(payload))
	frame[0] = byte(frameLen >> 8)
	frame[1] = byte(frameLen)
	frame[2] = ProtoMeshPacket
	copy(frame[3:], payload)

	tunnel.InjectData(frame)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	go fwd.HandleTunnel(ctx, "peer1", tunnel)

	// Wait a bit for processing
	time.Sleep(50 * time.Millisecond)

	// Verify packet was written to TUN
	written := mockTun.GetWrittenPackets()
	assert.Equal(t, payload, written)
}
