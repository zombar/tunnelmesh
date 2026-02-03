package wireguard

import (
	"encoding/json"
	"testing"
)

func TestAPIHandlerSetOnClientsChanged(t *testing.T) {
	store, err := NewClientStore("10.99.0.0/16", t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	handler := NewAPIHandler(store, "pubkey", "endpoint:51820", "10.99.0.0/16", "mesh.local")

	var callbackClients []Client
	var callbackCalled bool

	handler.SetOnClientsChanged(func(clients []Client) {
		callbackCalled = true
		callbackClients = clients
	})

	// Create a client via API
	reqBody, _ := json.Marshal(CreateClientRequest{Name: "TestClient"})
	response := handler.HandleRequest("POST /clients", reqBody)

	var resp APIResponse
	if err := json.Unmarshal(response, &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.StatusCode != 201 {
		t.Errorf("expected status 201, got %d", resp.StatusCode)
	}

	// Verify callback was called
	if !callbackCalled {
		t.Error("callback should have been called after creating client")
	}

	if len(callbackClients) != 1 {
		t.Errorf("callback should have received 1 client, got %d", len(callbackClients))
	}
}

func TestAPIHandlerCallbackOnUpdate(t *testing.T) {
	store, err := NewClientStore("10.99.0.0/16", t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	handler := NewAPIHandler(store, "pubkey", "endpoint:51820", "10.99.0.0/16", "mesh.local")

	// Create a client first
	reqBody, _ := json.Marshal(CreateClientRequest{Name: "TestClient"})
	response := handler.HandleRequest("POST /clients", reqBody)

	var createResp APIResponse
	if err := json.Unmarshal(response, &createResp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	var createResult CreateClientResponse
	if err := json.Unmarshal(createResp.Body, &createResult); err != nil {
		t.Fatalf("failed to unmarshal create result: %v", err)
	}
	clientID := createResult.Client.ID

	// Now set callback and update
	var callbackCount int
	handler.SetOnClientsChanged(func(clients []Client) {
		callbackCount++
	})

	// Update client
	enabled := false
	updateBody, _ := json.Marshal(UpdateClientRequest{Enabled: &enabled})
	handler.HandleRequest("PATCH /clients/"+clientID, updateBody)

	if callbackCount != 1 {
		t.Errorf("callback should have been called once, got %d", callbackCount)
	}
}

func TestAPIHandlerCallbackOnDelete(t *testing.T) {
	store, err := NewClientStore("10.99.0.0/16", t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	handler := NewAPIHandler(store, "pubkey", "endpoint:51820", "10.99.0.0/16", "mesh.local")

	// Create a client first
	reqBody, _ := json.Marshal(CreateClientRequest{Name: "TestClient"})
	response := handler.HandleRequest("POST /clients", reqBody)

	var createResp APIResponse
	if err := json.Unmarshal(response, &createResp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	var createResult CreateClientResponse
	if err := json.Unmarshal(createResp.Body, &createResult); err != nil {
		t.Fatalf("failed to unmarshal create result: %v", err)
	}
	clientID := createResult.Client.ID

	// Now set callback and delete
	var callbackClients []Client
	handler.SetOnClientsChanged(func(clients []Client) {
		callbackClients = clients
	})

	// Delete client
	handler.HandleRequest("DELETE /clients/"+clientID, nil)

	// Callback should have been called with empty list
	if len(callbackClients) != 0 {
		t.Errorf("callback should have received 0 clients after delete, got %d", len(callbackClients))
	}
}

func TestAPIHandlerNoCallbackOnReadOperations(t *testing.T) {
	store, err := NewClientStore("10.99.0.0/16", t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	handler := NewAPIHandler(store, "pubkey", "endpoint:51820", "10.99.0.0/16", "mesh.local")

	// Create a client first (without callback)
	reqBody, _ := json.Marshal(CreateClientRequest{Name: "TestClient"})
	response := handler.HandleRequest("POST /clients", reqBody)

	var createResp APIResponse
	if err := json.Unmarshal(response, &createResp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	var createResult CreateClientResponse
	if err := json.Unmarshal(createResp.Body, &createResult); err != nil {
		t.Fatalf("failed to unmarshal create result: %v", err)
	}
	clientID := createResult.Client.ID

	// Now set callback
	var callbackCount int
	handler.SetOnClientsChanged(func(clients []Client) {
		callbackCount++
	})

	// List clients - should NOT trigger callback
	handler.HandleRequest("GET /clients", nil)

	// Get single client - should NOT trigger callback
	handler.HandleRequest("GET /clients/"+clientID, nil)

	if callbackCount != 0 {
		t.Errorf("callback should not have been called for read operations, got %d calls", callbackCount)
	}
}

func TestAPIHandlerNilCallback(t *testing.T) {
	store, err := NewClientStore("10.99.0.0/16", t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	handler := NewAPIHandler(store, "pubkey", "endpoint:51820", "10.99.0.0/16", "mesh.local")
	// Don't set callback - should not panic

	// Create a client
	reqBody, _ := json.Marshal(CreateClientRequest{Name: "TestClient"})
	response := handler.HandleRequest("POST /clients", reqBody)

	var resp APIResponse
	if err := json.Unmarshal(response, &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.StatusCode != 201 {
		t.Errorf("expected status 201, got %d", resp.StatusCode)
	}
}

func TestGenerateClientConfig(t *testing.T) {
	params := ClientConfigParams{
		ClientPrivateKey: "privatekey123",
		ClientMeshIP:     "10.99.100.1",
		ServerPublicKey:  "serverpubkey456",
		ServerEndpoint:   "vpn.example.com:51820",
		MeshCIDR:         "10.99.0.0/16",
	}

	config := GenerateClientConfig(params)

	// Check that config contains expected values
	if !containsString(config, "PrivateKey = privatekey123") {
		t.Error("config should contain private key")
	}
	if !containsString(config, "Address = 10.99.100.1/32") {
		t.Error("config should contain address")
	}
	if !containsString(config, "PublicKey = serverpubkey456") {
		t.Error("config should contain server public key")
	}
	if !containsString(config, "Endpoint = vpn.example.com:51820") {
		t.Error("config should contain endpoint")
	}
	if !containsString(config, "AllowedIPs = 10.99.0.0/16") {
		t.Error("config should contain allowed IPs")
	}
}

func TestGenerateClientConfigNoDNS(t *testing.T) {
	params := ClientConfigParams{
		ClientPrivateKey: "privatekey123",
		ClientMeshIP:     "10.99.100.1",
		ServerPublicKey:  "serverpubkey456",
		ServerEndpoint:   "vpn.example.com:51820",
		DNSServer:        "", // No DNS
		MeshCIDR:         "10.99.0.0/16",
	}

	config := GenerateClientConfig(params)

	// Should not contain DNS line when empty
	if containsString(config, "DNS =") {
		t.Error("config should not contain DNS when not specified")
	}
}

func TestGenerateClientConfigWithDNS(t *testing.T) {
	params := ClientConfigParams{
		ClientPrivateKey: "privatekey123",
		ClientMeshIP:     "10.99.100.1",
		ServerPublicKey:  "serverpubkey456",
		ServerEndpoint:   "vpn.example.com:51820",
		DNSServer:        "10.99.0.1",
		MeshCIDR:         "10.99.0.0/16",
	}

	config := GenerateClientConfig(params)

	if !containsString(config, "DNS = 10.99.0.1") {
		t.Error("config should contain DNS when specified")
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
