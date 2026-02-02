package tunnel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
)

// Persistent relay message types (must match server)
const (
	MsgTypeSendPacket byte = 0x01 // Client -> Server: send packet to target peer
	MsgTypeRecvPacket byte = 0x02 // Server -> Client: received packet from source peer
	MsgTypePing       byte = 0x03 // Keepalive ping
	MsgTypePong       byte = 0x04 // Keepalive pong
)

// PersistentRelay maintains a persistent connection to the coordination server
// for DERP-like packet relay. It can route packets to any peer without
// the per-peer pairing required by the old relay mechanism.
type PersistentRelay struct {
	serverURL  string
	jwtToken   string
	conn       *websocket.Conn
	mu         sync.RWMutex
	connected  bool
	closed     bool
	closedChan chan struct{}

	// Packet routing
	incomingMu sync.Mutex
	incoming   map[string]*peerQueue // peerName -> queue of incoming packets

	// Callbacks
	onPacket func(sourcePeer string, data []byte)
}

// peerQueue holds incoming packets from a specific peer.
type peerQueue struct {
	packets chan []byte
}

// NewPersistentRelay creates a new persistent relay connection.
func NewPersistentRelay(serverURL, jwtToken string) *PersistentRelay {
	return &PersistentRelay{
		serverURL:  serverURL,
		jwtToken:   jwtToken,
		closedChan: make(chan struct{}),
		incoming:   make(map[string]*peerQueue),
	}
}

// Connect establishes the persistent relay connection to the server.
func (p *PersistentRelay) Connect(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return fmt.Errorf("relay is closed")
	}
	if p.connected {
		p.mu.Unlock()
		return nil // Already connected
	}
	p.mu.Unlock()

	wsURL, err := httpToWSURL(p.serverURL)
	if err != nil {
		return fmt.Errorf("convert URL: %w", err)
	}

	relayURL := wsURL + "/api/v1/relay/persistent"

	log.Debug().Str("url", relayURL).Msg("connecting to persistent relay")

	dialer := websocket.Dialer{
		HandshakeTimeout: 30 * time.Second,
	}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+p.jwtToken)

	conn, resp, err := dialer.DialContext(ctx, relayURL, headers)
	if err != nil {
		if resp != nil {
			body := make([]byte, 256)
			n, _ := resp.Body.Read(body)
			return fmt.Errorf("persistent relay connection failed: %s - %s", resp.Status, string(body[:n]))
		}
		return fmt.Errorf("persistent relay connection failed: %w", err)
	}

	p.mu.Lock()
	p.conn = conn
	p.connected = true
	p.mu.Unlock()

	log.Info().Msg("persistent relay connected")

	// Start message reader
	go p.readLoop()

	return nil
}

// readLoop reads messages from the server and dispatches them.
func (p *PersistentRelay) readLoop() {
	defer func() {
		p.mu.Lock()
		p.connected = false
		if p.conn != nil {
			p.conn.Close()
		}
		p.mu.Unlock()
		log.Debug().Msg("persistent relay read loop exited")
	}()

	for {
		p.mu.RLock()
		conn := p.conn
		closed := p.closed
		p.mu.RUnlock()

		if closed || conn == nil {
			return
		}

		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return
			}
			p.mu.RLock()
			if !p.closed {
				log.Debug().Err(err).Msg("persistent relay read error")
			}
			p.mu.RUnlock()
			return
		}

		p.handleMessage(data)
	}
}

// handleMessage processes an incoming message from the server.
func (p *PersistentRelay) handleMessage(data []byte) {
	if len(data) < 3 {
		return
	}

	msgType := data[0]

	switch msgType {
	case MsgTypeRecvPacket:
		// Format: [MsgTypeRecvPacket][source name len][source name][packet data]
		sourceLen := int(data[1])
		if len(data) < 2+sourceLen {
			return
		}
		sourcePeer := string(data[2 : 2+sourceLen])
		packetData := data[2+sourceLen:]

		// Dispatch to callback or queue
		if p.onPacket != nil {
			p.onPacket(sourcePeer, packetData)
		} else {
			p.queuePacket(sourcePeer, packetData)
		}

	case MsgTypePong:
		// Server pong - connection is alive
		log.Debug().Msg("persistent relay received pong")
	}
}

// queuePacket adds a packet to the incoming queue for a peer.
func (p *PersistentRelay) queuePacket(sourcePeer string, data []byte) {
	p.incomingMu.Lock()
	defer p.incomingMu.Unlock()

	q, ok := p.incoming[sourcePeer]
	if !ok {
		q = &peerQueue{packets: make(chan []byte, 64)}
		p.incoming[sourcePeer] = q
	}

	select {
	case q.packets <- data:
	default:
		// Queue full, drop oldest
		select {
		case <-q.packets:
		default:
		}
		q.packets <- data
	}
}

// SendTo sends a packet to the target peer via the relay.
func (p *PersistentRelay) SendTo(targetPeer string, data []byte) error {
	p.mu.RLock()
	conn := p.conn
	connected := p.connected
	p.mu.RUnlock()

	if !connected || conn == nil {
		return fmt.Errorf("not connected to relay")
	}

	// Build message: [MsgTypeSendPacket][target name len][target name][data]
	msg := make([]byte, 2+len(targetPeer)+len(data))
	msg[0] = MsgTypeSendPacket
	msg[1] = byte(len(targetPeer))
	copy(msg[2:], targetPeer)
	copy(msg[2+len(targetPeer):], data)

	return conn.WriteMessage(websocket.BinaryMessage, msg)
}

// SetPacketHandler sets a callback for incoming packets.
// If set, packets are dispatched to this callback instead of being queued.
func (p *PersistentRelay) SetPacketHandler(handler func(sourcePeer string, data []byte)) {
	p.onPacket = handler
}

// IsConnected returns true if the relay is connected.
func (p *PersistentRelay) IsConnected() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.connected
}

// Close closes the persistent relay connection.
func (p *PersistentRelay) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}
	p.closed = true
	close(p.closedChan)

	if p.conn != nil {
		_ = p.conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(5*time.Second),
		)
		return p.conn.Close()
	}

	return nil
}

// PeerTunnel wraps a PersistentRelay to implement io.ReadWriteCloser for a specific peer.
// This allows using the persistent relay as a tunnel to a specific peer.
type PeerTunnel struct {
	relay    *PersistentRelay
	peerName string
	readBuf  []byte
	readPos  int
	mu       sync.Mutex
	closed   bool
}

// NewPeerTunnel creates a tunnel to a specific peer via the persistent relay.
func (p *PersistentRelay) NewPeerTunnel(peerName string) *PeerTunnel {
	return &PeerTunnel{
		relay:    p,
		peerName: peerName,
	}
}

// Read reads data from the peer via the relay.
func (pt *PeerTunnel) Read(buf []byte) (int, error) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	if pt.closed {
		return 0, io.EOF
	}

	// If we have buffered data, return it
	if pt.readPos < len(pt.readBuf) {
		n := copy(buf, pt.readBuf[pt.readPos:])
		pt.readPos += n
		if pt.readPos >= len(pt.readBuf) {
			pt.readBuf = nil
			pt.readPos = 0
		}
		return n, nil
	}

	// Get queue for this peer
	pt.relay.incomingMu.Lock()
	q, ok := pt.relay.incoming[pt.peerName]
	if !ok {
		q = &peerQueue{packets: make(chan []byte, 64)}
		pt.relay.incoming[pt.peerName] = q
	}
	pt.relay.incomingMu.Unlock()

	// Wait for packet (with mutex unlocked)
	pt.mu.Unlock()
	select {
	case data, ok := <-q.packets:
		pt.mu.Lock()
		if !ok {
			return 0, io.EOF
		}
		n := copy(buf, data)
		if n < len(data) {
			pt.readBuf = data
			pt.readPos = n
		}
		return n, nil
	case <-pt.relay.closedChan:
		pt.mu.Lock()
		return 0, io.EOF
	}
}

// Write writes data to the peer via the relay.
func (pt *PeerTunnel) Write(buf []byte) (int, error) {
	pt.mu.Lock()
	if pt.closed {
		pt.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	pt.mu.Unlock()

	if err := pt.relay.SendTo(pt.peerName, buf); err != nil {
		return 0, err
	}
	return len(buf), nil
}

// Close closes the peer tunnel (but not the underlying relay).
func (pt *PeerTunnel) Close() error {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.closed = true
	return nil
}

// PeerName returns the name of the peer this tunnel connects to.
func (pt *PeerTunnel) PeerName() string {
	return pt.peerName
}

// IsClosed returns true if the tunnel is closed.
func (pt *PeerTunnel) IsClosed() bool {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	return pt.closed || !pt.relay.IsConnected()
}
