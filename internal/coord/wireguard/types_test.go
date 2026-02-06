package wireguard

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWGClientJSON(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	client := &Client{
		ID:        "test-id-123",
		Name:      "iPhone",
		PublicKey: "abc123pubkey==",
		MeshIP:    "172.30.100.1",
		DNSName:   "iphone",
		Enabled:   true,
		CreatedAt: now,
		LastSeen:  now,
	}

	// Test marshal
	data, err := json.Marshal(client)
	if err != nil {
		t.Fatalf("failed to marshal client: %v", err)
	}

	// Test unmarshal
	var decoded Client
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal client: %v", err)
	}

	// Verify fields
	if decoded.ID != client.ID {
		t.Errorf("ID mismatch: got %q, want %q", decoded.ID, client.ID)
	}
	if decoded.Name != client.Name {
		t.Errorf("Name mismatch: got %q, want %q", decoded.Name, client.Name)
	}
	if decoded.PublicKey != client.PublicKey {
		t.Errorf("PublicKey mismatch: got %q, want %q", decoded.PublicKey, client.PublicKey)
	}
	if decoded.MeshIP != client.MeshIP {
		t.Errorf("MeshIP mismatch: got %q, want %q", decoded.MeshIP, client.MeshIP)
	}
	if decoded.DNSName != client.DNSName {
		t.Errorf("DNSName mismatch: got %q, want %q", decoded.DNSName, client.DNSName)
	}
	if decoded.Enabled != client.Enabled {
		t.Errorf("Enabled mismatch: got %v, want %v", decoded.Enabled, client.Enabled)
	}
}

func TestCreateClientRequestValidation(t *testing.T) {
	tests := []struct {
		name    string
		req     CreateClientRequest
		wantErr bool
	}{
		{
			name:    "valid request",
			req:     CreateClientRequest{Name: "iPhone"},
			wantErr: false,
		},
		{
			name:    "empty name",
			req:     CreateClientRequest{Name: ""},
			wantErr: true,
		},
		{
			name:    "name with spaces only",
			req:     CreateClientRequest{Name: "   "},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCreateClientResponseJSON(t *testing.T) {
	resp := CreateClientResponse{
		Client: Client{
			ID:      "test-id",
			Name:    "iPhone",
			MeshIP:  "172.30.100.1",
			DNSName: "iphone",
			Enabled: true,
		},
		PrivateKey: "privatekey123==",
		Config:     "[Interface]\nPrivateKey=...",
		QRCode:     "data:image/png;base64,iVBORw...",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal response: %v", err)
	}

	var decoded CreateClientResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if decoded.PrivateKey != resp.PrivateKey {
		t.Errorf("PrivateKey mismatch: got %q, want %q", decoded.PrivateKey, resp.PrivateKey)
	}
	if decoded.Config != resp.Config {
		t.Errorf("Config mismatch: got %q, want %q", decoded.Config, resp.Config)
	}
	if decoded.QRCode != resp.QRCode {
		t.Errorf("QRCode mismatch: got %q, want %q", decoded.QRCode, resp.QRCode)
	}
}

func TestUpdateClientRequestValidation(t *testing.T) {
	tests := []struct {
		name    string
		req     UpdateClientRequest
		wantErr bool
	}{
		{
			name:    "valid enable",
			req:     UpdateClientRequest{Enabled: boolPtr(true)},
			wantErr: false,
		},
		{
			name:    "valid disable",
			req:     UpdateClientRequest{Enabled: boolPtr(false)},
			wantErr: false,
		},
		{
			name:    "empty request",
			req:     UpdateClientRequest{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClientListResponseJSON(t *testing.T) {
	resp := ClientListResponse{
		Clients: []Client{
			{ID: "1", Name: "iPhone", MeshIP: "172.30.100.1"},
			{ID: "2", Name: "Android", MeshIP: "172.30.100.2"},
		},
		ConcentratorPublicKey: "concentratorkey==",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal response: %v", err)
	}

	var decoded ClientListResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if len(decoded.Clients) != 2 {
		t.Errorf("expected 2 clients, got %d", len(decoded.Clients))
	}
	if decoded.ConcentratorPublicKey != resp.ConcentratorPublicKey {
		t.Errorf("ConcentratorPublicKey mismatch")
	}
}

func boolPtr(b bool) *bool {
	return &b
}
