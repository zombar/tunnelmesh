package replication

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// MeshTransport implements the Transport interface using HTTP to send messages
// between coordinators over the mesh network.
type MeshTransport struct {
	// Base URL pattern for coordinators: https://{meshIP}/api/replication/message
	// Uses standard HTTPS port 443 (admin interface), which is mesh-only (not exposed to public internet)
	httpClient *http.Client
	handler    func(from string, data []byte) error
	handlerMu  sync.RWMutex
	logger     zerolog.Logger
}

// NewMeshTransport creates a new mesh-based transport for replication.
// If tlsConfig is nil, a default TLS configuration will be used (certificate verification enabled).
func NewMeshTransport(logger zerolog.Logger, tlsConfig *tls.Config) *MeshTransport {
	// Default TLS config if none provided
	if tlsConfig == nil {
		tlsConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			// InsecureSkipVerify: false (default) - verify server certificates
		}
	}

	return &MeshTransport{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
				TLSClientConfig:     tlsConfig,
			},
		},
		logger: logger.With().Str("component", "mesh-transport").Logger(),
	}
}

// SetClientCert configures a TLS client certificate for authenticating with other coordinators.
// Must be called before any requests are sent.
func (mt *MeshTransport) SetClientCert(cert *tls.Certificate) {
	if t, ok := mt.httpClient.Transport.(*http.Transport); ok && t.TLSClientConfig != nil {
		t.TLSClientConfig.Certificates = []tls.Certificate{*cert}
	}
}

// SendToCoordinator sends a replication message to another coordinator via HTTP.
// The coordMeshIP should be the coordinator's mesh IP address (e.g., "10.42.0.1").
func (mt *MeshTransport) SendToCoordinator(ctx context.Context, coordMeshIP string, data []byte) error {
	// Build URL: https://{meshIP}/api/replication/message
	// Uses standard HTTPS port 443 (admin interface), which is only accessible from within the mesh
	url := fmt.Sprintf("https://%s/api/replication/message", coordMeshIP)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Replication-Protocol", "v1")

	mt.logger.Debug().
		Str("coordinator", coordMeshIP).
		Int("size", len(data)).
		Msg("sending replication message")

	resp, err := mt.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	mt.logger.Debug().
		Str("coordinator", coordMeshIP).
		Int("status", resp.StatusCode).
		Msg("replication message sent successfully")

	return nil
}

// RegisterHandler registers a handler for incoming replication messages.
// This is called by the coordinator's HTTP server when it receives a replication message.
func (mt *MeshTransport) RegisterHandler(handler func(from string, data []byte) error) {
	mt.handlerMu.Lock()
	defer mt.handlerMu.Unlock()
	mt.handler = handler
}

// HandleIncomingMessage is called by the HTTP handler when a replication message is received.
// The 'from' parameter should be the sender's mesh IP or node ID.
func (mt *MeshTransport) HandleIncomingMessage(from string, data []byte) error {
	mt.handlerMu.RLock()
	handler := mt.handler
	mt.handlerMu.RUnlock()

	if handler == nil {
		return fmt.Errorf("no handler registered")
	}

	mt.logger.Debug().
		Str("from", from).
		Int("size", len(data)).
		Msg("handling incoming replication message")

	return handler(from, data)
}
