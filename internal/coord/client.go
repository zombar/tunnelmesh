package coord

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

// ErrPeerNotFound is returned when the peer is not registered with the server.
var ErrPeerNotFound = errors.New("peer not found")

// Client is a client for the coordination server.
type Client struct {
	baseURL   string
	authToken string
	client    *http.Client
}

// NewClient creates a new coordination client.
func NewClient(baseURL, authToken string) *Client {
	return &Client{
		baseURL:   baseURL,
		authToken: authToken,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Register registers this peer with the coordination server.
func (c *Client) Register(name, publicKey string, publicIPs, privateIPs []string, sshPort int) (*proto.RegisterResponse, error) {
	req := proto.RegisterRequest{
		Name:       name,
		PublicKey:  publicKey,
		PublicIPs:  publicIPs,
		PrivateIPs: privateIPs,
		SSHPort:    sshPort,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := c.doRequest(http.MethodPost, "/api/v1/register", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.parseError(resp)
	}

	var result proto.RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// ListPeers returns a list of all registered peers.
func (c *Client) ListPeers() ([]proto.Peer, error) {
	resp, err := c.doRequest(http.MethodGet, "/api/v1/peers", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.parseError(resp)
	}

	var result proto.PeerListResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return result.Peers, nil
}

// Heartbeat sends a heartbeat to maintain presence.
func (c *Client) Heartbeat(name, publicKey string) error {
	return c.HeartbeatWithStats(name, publicKey, nil)
}

// HeartbeatWithStats sends a heartbeat with optional stats to maintain presence.
func (c *Client) HeartbeatWithStats(name, publicKey string, stats *proto.PeerStats) error {
	req := proto.HeartbeatRequest{
		Name:      name,
		PublicKey: publicKey,
		Stats:     stats,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	resp, err := c.doRequest(http.MethodPost, "/api/v1/heartbeat", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrPeerNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return c.parseError(resp)
	}

	return nil
}

// Deregister removes this peer from the mesh.
func (c *Client) Deregister(name string) error {
	resp, err := c.doRequest(http.MethodDelete, "/api/v1/peers/"+name, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.parseError(resp)
	}

	return nil
}

// GetDNSRecords returns the current DNS records.
func (c *Client) GetDNSRecords() ([]proto.DNSRecord, error) {
	resp, err := c.doRequest(http.MethodGet, "/api/v1/dns", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.parseError(resp)
	}

	var result proto.DNSUpdateNotification
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return result.Records, nil
}

func (c *Client) doRequest(method, path string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.authToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.client.Do(req)
}

func (c *Client) parseError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)

	var errResp proto.ErrorResponse
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Message != "" {
		return fmt.Errorf("%s: %s", errResp.Error, errResp.Message)
	}

	return fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
}
