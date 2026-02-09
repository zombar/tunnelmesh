package peer

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/coord"
	"github.com/tunnelmesh/tunnelmesh/internal/transport"
)

// mockAddr implements net.Addr for testing.
type mockAddr struct{}

func (a mockAddr) Network() string { return "mock" }
func (a mockAddr) String() string  { return "mock:0" }

// mockListener implements transport.Listener for testing.
type mockListener struct {
	acceptCh chan transport.Connection
	closed   bool
}

func newMockListener() *mockListener {
	return &mockListener{
		acceptCh: make(chan transport.Connection, 1),
	}
}

func (l *mockListener) Accept(ctx context.Context) (transport.Connection, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case conn := <-l.acceptCh:
		return conn, nil
	}
}

func (l *mockListener) Addr() net.Addr {
	return mockAddr{}
}

func (l *mockListener) Close() error {
	l.closed = true
	return nil
}

// mockConnection implements transport.Connection for testing.
type mockConnection struct {
	peerName    string
	closeCalled bool
}

// nolint:revive // p required by io.Reader interface but not used in mock
func (c *mockConnection) Read(p []byte) (int, error) {
	return 0, nil
}

func (c *mockConnection) Write(p []byte) (int, error) {
	return len(p), nil
}

func (c *mockConnection) Close() error {
	c.closeCalled = true
	return nil
}

func (c *mockConnection) PeerName() string {
	return c.peerName
}

func (c *mockConnection) Type() transport.TransportType {
	return transport.TransportSSH
}

func (c *mockConnection) LocalAddr() net.Addr {
	return mockAddr{}
}

func (c *mockConnection) RemoteAddr() net.Addr {
	return mockAddr{}
}

func (c *mockConnection) IsHealthy() bool {
	return !c.closeCalled
}

func TestMeshNode_HandleIncomingSSH_ContextCancel(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	listener := newMockListener()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		node.HandleIncomingSSH(ctx, listener)
		close(done)
	}()

	// Cancel context
	cancel()

	// Should exit when context is cancelled
	select {
	case <-done:
		// OK - exited properly
	case <-time.After(2 * time.Second):
		t.Fatal("HandleIncomingSSH did not exit on context cancel")
	}
}

func TestMeshNode_HandleIncomingConnection_EmptyPeerName(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	// Create a connection with empty peer name
	conn := &mockConnection{peerName: ""}
	ctx := context.Background()

	// Should close connection when peer name is empty
	node.handleIncomingConnection(ctx, conn, "SSH")

	if !conn.closeCalled {
		t.Error("expected connection to be closed when peer name is empty")
	}
}

func TestMeshNode_HandleIncomingConnection_ValidPeer(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)

	// Create a connection with valid peer name
	conn := &mockConnection{peerName: "peer-1"}
	ctx := context.Background()

	// Should add tunnel to manager
	node.handleIncomingConnection(ctx, conn, "SSH")

	// Verify tunnel was added
	tunnels := node.tunnelMgr.List()
	found := false
	for _, name := range tunnels {
		if name == "peer-1" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected tunnel to be added to manager")
	}
}
