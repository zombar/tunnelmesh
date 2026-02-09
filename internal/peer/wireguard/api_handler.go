package wireguard

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/skip2/go-qrcode"
)

// APIRequest represents a parsed API request.
type APIRequest struct {
	Method string // e.g., "GET /clients", "POST /clients", "DELETE /clients/{id}"
	Body   []byte
}

// APIResponse represents an API response.
type APIResponse struct {
	StatusCode int    `json:"status_code"`
	Body       []byte `json:"body"`
}

// APIHandler handles WireGuard API requests proxied through the relay.
type APIHandler struct {
	store            *ClientStore
	serverPublicKey  string // Concentrator's public key
	serverEndpoint   string // WireGuard endpoint for clients to connect to
	meshCIDR         string
	domainSuffix     string
	onClientsChanged func(clients []Client) // Called when clients are created/updated/deleted
}

// NewAPIHandler creates a new API handler.
func NewAPIHandler(store *ClientStore, serverPublicKey, serverEndpoint, meshCIDR, domainSuffix string) *APIHandler {
	return &APIHandler{
		store:           store,
		serverPublicKey: serverPublicKey,
		serverEndpoint:  serverEndpoint,
		meshCIDR:        meshCIDR,
		domainSuffix:    domainSuffix,
	}
}

// SetOnClientsChanged sets the callback for when clients are created, updated, or deleted.
func (h *APIHandler) SetOnClientsChanged(fn func(clients []Client)) {
	h.onClientsChanged = fn
}

// notifyClientsChanged calls the callback with the current client list.
func (h *APIHandler) notifyClientsChanged() {
	if h.onClientsChanged != nil {
		h.onClientsChanged(h.store.List())
	}
}

// HandleRequest processes an API request and returns a response.
// Method format: "GET /clients", "POST /clients", "DELETE /clients/{id}", etc.
func (h *APIHandler) HandleRequest(method string, body []byte) []byte {
	parts := strings.SplitN(method, " ", 2)
	if len(parts) != 2 {
		return h.errorResponse(400, "invalid method format")
	}

	httpMethod := parts[0]
	path := parts[1]

	log.Debug().
		Str("http_method", httpMethod).
		Str("path", path).
		Int("body_len", len(body)).
		Msg("handling API request")

	// Route to appropriate handler
	switch {
	case path == "/clients" && httpMethod == "GET":
		return h.listClients()

	case path == "/clients" && httpMethod == "POST":
		return h.createClient(body)

	case strings.HasPrefix(path, "/clients/") && httpMethod == "GET":
		id := strings.TrimPrefix(path, "/clients/")
		return h.getClient(id)

	case strings.HasPrefix(path, "/clients/") && httpMethod == "PATCH":
		id := strings.TrimPrefix(path, "/clients/")
		return h.updateClient(id, body)

	case strings.HasPrefix(path, "/clients/") && httpMethod == "DELETE":
		id := strings.TrimPrefix(path, "/clients/")
		return h.deleteClient(id)

	default:
		return h.errorResponse(404, "not found")
	}
}

func (h *APIHandler) listClients() []byte {
	clients := h.store.List()

	resp := ClientListResponse{
		Clients:               clients,
		ConcentratorPublicKey: h.serverPublicKey,
	}

	return h.jsonResponse(200, resp)
}

func (h *APIHandler) createClient(body []byte) []byte {
	var req CreateClientRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return h.errorResponse(400, "invalid request body")
	}

	if err := req.Validate(); err != nil {
		return h.errorResponse(400, err.Error())
	}

	client, privateKey, err := h.store.CreateWithPrivateKey(req.Name)
	if err != nil {
		log.Error().Err(err).Msg("failed to create WireGuard client")
		return h.errorResponse(500, "failed to create client")
	}

	// Generate config
	configParams := ClientConfigParams{
		ClientPrivateKey: privateKey,
		ClientMeshIP:     client.MeshIP,
		ServerPublicKey:  h.serverPublicKey,
		ServerEndpoint:   h.serverEndpoint,
		DNSServer:        "", // Optional
		MeshCIDR:         h.meshCIDR,
		DomainSuffix:     h.domainSuffix,
	}

	configStr := GenerateClientConfig(configParams)

	qrCode, err := GenerateQRCodeDataURL(configStr, 256)
	if err != nil {
		log.Warn().Err(err).Msg("failed to generate QR code")
	}

	resp := CreateClientResponse{
		Client:     *client,
		PrivateKey: privateKey,
		Config:     configStr,
		QRCode:     qrCode,
	}

	log.Info().Str("client_id", client.ID).Str("name", client.Name).Msg("created WireGuard client")

	// Notify about client change
	h.notifyClientsChanged()

	return h.jsonResponse(201, resp)
}

func (h *APIHandler) getClient(id string) []byte {
	client, err := h.store.Get(id)
	if errors.Is(err, ErrClientNotFound) {
		return h.errorResponse(404, "client not found")
	}
	if err != nil {
		return h.errorResponse(500, "internal error")
	}

	return h.jsonResponse(200, client)
}

func (h *APIHandler) updateClient(id string, body []byte) []byte {
	var req UpdateClientRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return h.errorResponse(400, "invalid request body")
	}

	client, err := h.store.Update(id, req.Enabled)
	if errors.Is(err, ErrClientNotFound) {
		return h.errorResponse(404, "client not found")
	}
	if err != nil {
		return h.errorResponse(500, "internal error")
	}

	log.Info().Str("client_id", id).Msg("updated WireGuard client")

	// Notify about client change
	h.notifyClientsChanged()

	return h.jsonResponse(200, client)
}

func (h *APIHandler) deleteClient(id string) []byte {
	err := h.store.Delete(id)
	if errors.Is(err, ErrClientNotFound) {
		return h.errorResponse(404, "client not found")
	}
	if err != nil {
		return h.errorResponse(500, "internal error")
	}

	log.Info().Str("client_id", id).Msg("deleted WireGuard client")

	// Notify about client change
	h.notifyClientsChanged()

	return h.jsonResponse(200, map[string]string{"status": "deleted"})
}

func (h *APIHandler) jsonResponse(statusCode int, data interface{}) []byte {
	body, err := json.Marshal(data)
	if err != nil {
		return h.errorResponse(500, "marshal error")
	}

	resp := APIResponse{
		StatusCode: statusCode,
		Body:       body,
	}

	result, _ := json.Marshal(resp)
	return result
}

func (h *APIHandler) errorResponse(statusCode int, message string) []byte {
	body, _ := json.Marshal(map[string]string{"error": message})
	resp := APIResponse{
		StatusCode: statusCode,
		Body:       body,
	}
	result, _ := json.Marshal(resp)
	return result
}

// CreateClientRequest is the request body for creating a new WireGuard client.
type CreateClientRequest struct {
	Name string `json:"name"`
}

// Validate validates the create client request.
func (r *CreateClientRequest) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("name is required")
	}
	return nil
}

// CreateClientResponse is the response after creating a new WireGuard client.
type CreateClientResponse struct {
	Client     Client `json:"client"`
	PrivateKey string `json:"private_key"`
	Config     string `json:"config"`
	QRCode     string `json:"qr_code"`
}

// UpdateClientRequest is the request body for updating a WireGuard client.
type UpdateClientRequest struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// ClientConfigParams holds parameters for generating a WireGuard client config.
type ClientConfigParams struct {
	ClientPrivateKey string
	ClientMeshIP     string
	ServerPublicKey  string
	ServerEndpoint   string
	DNSServer        string
	MeshCIDR         string
	DomainSuffix     string
}

// GenerateClientConfig generates a WireGuard client configuration file.
func GenerateClientConfig(params ClientConfigParams) string {
	var sb strings.Builder
	sb.WriteString("[Interface]\n")
	sb.WriteString(fmt.Sprintf("PrivateKey = %s\n", params.ClientPrivateKey))
	sb.WriteString(fmt.Sprintf("Address = %s/32\n", params.ClientMeshIP))
	if params.DNSServer != "" {
		sb.WriteString(fmt.Sprintf("DNS = %s\n", params.DNSServer))
	}
	sb.WriteString("\n[Peer]\n")
	sb.WriteString(fmt.Sprintf("PublicKey = %s\n", params.ServerPublicKey))
	sb.WriteString(fmt.Sprintf("AllowedIPs = %s\n", params.MeshCIDR))
	if params.ServerEndpoint != "" {
		sb.WriteString(fmt.Sprintf("Endpoint = %s\n", params.ServerEndpoint))
	}
	sb.WriteString("PersistentKeepalive = 25\n")
	return sb.String()
}

// GenerateQRCodeDataURL generates a QR code as a data URL.
func GenerateQRCodeDataURL(content string, size int) (string, error) {
	if content == "" {
		return "", fmt.Errorf("content cannot be empty")
	}

	png, err := qrcode.Encode(content, qrcode.Medium, size)
	if err != nil {
		return "", err
	}

	// Encode as base64 data URL
	b64 := base64.StdEncoding.EncodeToString(png)
	return "data:image/png;base64," + b64, nil
}
