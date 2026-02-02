package peer

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/coord"
)

func TestMeshNode_HandleIncomingSSH_NoSSHServer(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)
	node.SSHServer = nil

	// Create a simple listener
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot create listener for test")
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		node.HandleIncomingSSH(ctx, listener)
		close(done)
	}()

	// Make a connection - it should be rejected because SSHServer is nil
	addr := listener.Addr().String()
	conn, err := net.Dial("tcp", addr)
	if err == nil {
		conn.Close()
	}

	// Now cancel context and close listener to make HandleIncomingSSH exit
	cancel()
	listener.Close()

	// Should exit when context is cancelled and listener closed
	select {
	case <-done:
		// OK - exited properly
	case <-time.After(2 * time.Second):
		t.Fatal("HandleIncomingSSH did not exit on context cancel")
	}
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

	// Create a simple listener
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot create listener for test")
	}
	defer listener.Close()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		node.HandleIncomingSSH(ctx, listener)
		close(done)
	}()

	// Cancel context
	cancel()

	// Close listener to unblock Accept
	listener.Close()

	// Should exit when context is cancelled
	select {
	case <-done:
		// OK - exited properly
	case <-time.After(2 * time.Second):
		t.Fatal("HandleIncomingSSH did not exit on context cancel")
	}
}

func TestMeshNode_HandleSSHConnection_NilServer(t *testing.T) {
	identity := &PeerIdentity{
		Name: "test-node",
		Config: &config.PeerConfig{
			Name: "test-node",
		},
	}
	client := coord.NewClient("http://localhost:8080", "test-token")
	node := NewMeshNode(identity, client)
	node.SSHServer = nil

	// Create a pair of connected connections for testing
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	ctx := context.Background()

	// Should not panic, just close the connection
	node.handleSSHConnection(ctx, serverConn)

	// Verify connection was closed by trying to write to the client side
	// This should either succeed (connection still open) or fail (connection closed)
	// We expect it to be closed
	_ = clientConn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	_, err := clientConn.Write([]byte("test"))
	assert.Error(t, err, "connection should be closed")
}
