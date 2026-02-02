package tunnel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
)

// RelayTunnel implements io.ReadWriteCloser over a WebSocket relay connection.
type RelayTunnel struct {
	conn       *websocket.Conn
	peerName   string
	serverURL  string
	jwtToken   string
	reader     io.Reader
	mu         sync.Mutex
	closed     bool
	closedChan chan struct{}
}

// NewRelayTunnel establishes a relay tunnel to the target peer through the coordination server.
func NewRelayTunnel(ctx context.Context, serverURL, targetPeer, jwtToken string) (*RelayTunnel, error) {
	// Convert HTTP URL to WebSocket URL
	wsURL, err := httpToWSURL(serverURL)
	if err != nil {
		return nil, fmt.Errorf("convert URL: %w", err)
	}

	relayURL := wsURL + "/api/v1/relay/" + targetPeer

	log.Debug().
		Str("url", relayURL).
		Str("target", targetPeer).
		Msg("connecting to relay")

	// Create dialer with context
	dialer := websocket.Dialer{
		HandshakeTimeout: 30 * time.Second,
	}

	// Set up headers with JWT auth
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+jwtToken)

	// Dial with context
	conn, resp, err := dialer.DialContext(ctx, relayURL, headers)
	if err != nil {
		if resp != nil {
			body := make([]byte, 256)
			n, _ := resp.Body.Read(body)
			return nil, fmt.Errorf("relay connection failed: %s - %s", resp.Status, string(body[:n]))
		}
		return nil, fmt.Errorf("relay connection failed: %w", err)
	}

	log.Info().
		Str("target", targetPeer).
		Msg("relay tunnel established")

	return &RelayTunnel{
		conn:       conn,
		peerName:   targetPeer,
		serverURL:  serverURL,
		jwtToken:   jwtToken,
		closedChan: make(chan struct{}),
	}, nil
}

// Read reads data from the relay tunnel.
func (t *RelayTunnel) Read(p []byte) (int, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return 0, io.EOF
	}

	// If we have a partial message from previous read, continue reading it
	if t.reader != nil {
		t.mu.Unlock()
		n, err := t.reader.Read(p)
		if err == io.EOF {
			t.mu.Lock()
			t.reader = nil
			t.mu.Unlock()
			if n > 0 {
				return n, nil
			}
			// Get next message
			return t.Read(p)
		}
		return n, err
	}
	t.mu.Unlock()

	// Read next WebSocket message
	messageType, reader, err := t.conn.NextReader()
	if err != nil {
		if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
			return 0, io.EOF
		}
		return 0, fmt.Errorf("read message: %w", err)
	}

	if messageType != websocket.BinaryMessage {
		// Skip non-binary messages
		return t.Read(p)
	}

	n, err := reader.Read(p)
	if err == io.EOF {
		// Message fully read
		return n, nil
	}
	if err != nil {
		return n, err
	}

	// Save reader for remaining data
	t.mu.Lock()
	t.reader = reader
	t.mu.Unlock()

	return n, nil
}

// Write writes data to the relay tunnel.
func (t *RelayTunnel) Write(p []byte) (int, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	t.mu.Unlock()

	err := t.conn.WriteMessage(websocket.BinaryMessage, p)
	if err != nil {
		return 0, fmt.Errorf("write message: %w", err)
	}

	return len(p), nil
}

// Close closes the relay tunnel.
func (t *RelayTunnel) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}
	t.closed = true
	close(t.closedChan)

	// Send close message (ignore error since we're closing anyway)
	_ = t.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(5*time.Second),
	)

	log.Debug().
		Str("peer", t.peerName).
		Msg("relay tunnel closed")

	return t.conn.Close()
}

// PeerName returns the name of the peer at the other end.
func (t *RelayTunnel) PeerName() string {
	return t.peerName
}

// IsClosed returns true if the relay tunnel has been closed.
func (t *RelayTunnel) IsClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

// httpToWSURL converts an HTTP(S) URL to a WebSocket URL.
func httpToWSURL(httpURL string) (string, error) {
	u, err := url.Parse(httpURL)
	if err != nil {
		return "", err
	}

	switch strings.ToLower(u.Scheme) {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}

	return u.String(), nil
}
