package mesh

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

func TestRegisterWithCoordinator(t *testing.T) {
	tests := []struct {
		name           string
		serverStatus   int
		serverResponse proto.RegisterResponse
		serverError    *proto.ErrorResponse
		authToken      string
		expectError    bool
		errorContains  string
	}{
		{
			name:         "successful registration",
			serverStatus: http.StatusOK,
			serverResponse: proto.RegisterResponse{
				MeshIP:       "10.42.0.100",
				CoordMeshIPs: []string{"10.42.0.1"},
				PeerID:       "test-peer-id",
				PeerName:     "s3bench",
				IsAdmin:      false,
			},
			expectError: false,
		},
		{
			name:         "successful registration with admin",
			serverStatus: http.StatusOK,
			serverResponse: proto.RegisterResponse{
				MeshIP:       "10.42.0.100",
				CoordMeshIPs: []string{"10.42.0.1"},
				PeerID:       "admin-peer-id",
				PeerName:     "s3bench",
				IsAdmin:      true,
			},
			expectError: false,
		},
		{
			name:         "registration with auth token",
			serverStatus: http.StatusOK,
			serverResponse: proto.RegisterResponse{
				MeshIP:       "10.42.0.100",
				CoordMeshIPs: []string{"10.42.0.1"},
				PeerID:       "test-peer-id",
				PeerName:     "s3bench",
				IsAdmin:      false,
			},
			authToken:   "test-token-12345",
			expectError: false,
		},
		{
			name:         "unauthorized",
			serverStatus: http.StatusUnauthorized,
			serverError: &proto.ErrorResponse{
				Error: "Unauthorized",
			},
			expectError:   true,
			errorContains: "Unauthorized",
		},
		{
			name:         "forbidden",
			serverStatus: http.StatusForbidden,
			serverError: &proto.ErrorResponse{
				Error: "Access denied",
			},
			expectError:   true,
			errorContains: "Access denied",
		},
		{
			name:          "server error",
			serverStatus:  http.StatusInternalServerError,
			expectError:   true,
			errorContains: "500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedAuthToken string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify method and path
				if r.Method != http.MethodPost {
					t.Errorf("Expected POST, got %s", r.Method)
				}
				if !strings.Contains(r.URL.Path, "/api/v1/register") {
					t.Errorf("Expected /api/v1/register in path, got %s", r.URL.Path)
				}

				// Capture auth token
				authHeader := r.Header.Get("Authorization")
				if authHeader != "" {
					receivedAuthToken = strings.TrimPrefix(authHeader, "Bearer ")
				}

				// Verify request body
				var regReq proto.RegisterRequest
				if err := json.NewDecoder(r.Body).Decode(&regReq); err != nil {
					t.Errorf("Failed to decode request: %v", err)
				}

				// Verify required fields
				if regReq.Name != "s3bench" {
					t.Errorf("Expected name=s3bench, got %s", regReq.Name)
				}
				if regReq.PublicKey == "" {
					t.Error("Expected non-empty public key")
				}

				// Send response
				w.WriteHeader(tt.serverStatus)
				if tt.serverStatus == http.StatusOK {
					_ = json.NewEncoder(w).Encode(tt.serverResponse)
				} else if tt.serverError != nil {
					_ = json.NewEncoder(w).Encode(tt.serverError)
				}
			}))
			defer server.Close()

			// Create test credentials
			creds := &Credentials{
				PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINF... test-key",
			}

			meshInfo, err := RegisterWithCoordinator(context.Background(), server.URL, creds, true, tt.authToken)

			// Verify auth token was sent
			if tt.authToken != "" && receivedAuthToken != tt.authToken {
				t.Errorf("Expected auth token %q, got %q", tt.authToken, receivedAuthToken)
			}

			// Verify results
			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error, got nil")
				} else if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("Expected error to contain %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got %v", err)
				}
				if meshInfo == nil {
					t.Fatal("Expected meshInfo, got nil")
				}

				// Verify mesh info
				if meshInfo.MeshIP != tt.serverResponse.MeshIP {
					t.Errorf("Expected MeshIP=%s, got %s", tt.serverResponse.MeshIP, meshInfo.MeshIP)
				}
				expectedCoordIP := ""
				if len(tt.serverResponse.CoordMeshIPs) > 0 {
					expectedCoordIP = tt.serverResponse.CoordMeshIPs[0]
				}
				if meshInfo.CoordMeshIP != expectedCoordIP {
					t.Errorf("Expected CoordMeshIP=%s, got %s", expectedCoordIP, meshInfo.CoordMeshIP)
				}
				if meshInfo.PeerID != tt.serverResponse.PeerID {
					t.Errorf("Expected PeerID=%s, got %s", tt.serverResponse.PeerID, meshInfo.PeerID)
				}
				if meshInfo.PeerName != tt.serverResponse.PeerName {
					t.Errorf("Expected PeerName=%s, got %s", tt.serverResponse.PeerName, meshInfo.PeerName)
				}
				if meshInfo.IsAdmin != tt.serverResponse.IsAdmin {
					t.Errorf("Expected IsAdmin=%v, got %v", tt.serverResponse.IsAdmin, meshInfo.IsAdmin)
				}
			}
		})
	}
}

func TestRegisterWithCoordinator_TLSVerification(t *testing.T) {
	// Start HTTPS test server
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := proto.RegisterResponse{
			MeshIP:       "10.42.0.100",
			CoordMeshIPs: []string{"10.42.0.1"},
			PeerID:       "test-peer",
			PeerName:     "s3bench",
			IsAdmin:      false,
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	creds := &Credentials{
		PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINF... test-key",
	}

	// Test 1: With insecure TLS (should succeed with self-signed cert)
	_, err := RegisterWithCoordinator(context.Background(), server.URL, creds, true, "")
	if err != nil {
		t.Errorf("Expected success with insecureSkipVerify=true, got error: %v", err)
	}

	// Test 2: With secure TLS (should fail with self-signed cert)
	_, err = RegisterWithCoordinator(context.Background(), server.URL, creds, false, "")
	if err == nil {
		t.Error("Expected error with insecureSkipVerify=false and self-signed cert, got nil")
	}
}

func TestRegisterWithCoordinator_NetworkError(t *testing.T) {
	creds := &Credentials{
		PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINF... test-key",
	}

	// Test with unreachable coordinator
	_, err := RegisterWithCoordinator(context.Background(), "http://localhost:1", creds, true, "")
	if err == nil {
		t.Error("Expected error when coordinator is unreachable, got nil")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("Expected 'unreachable' in error, got: %v", err)
	}
}

func TestRegisterWithCoordinator_ContextCancellation(t *testing.T) {
	// Create a server that delays response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Server doesn't respond quickly enough
		select {}
	}))
	defer server.Close()

	creds := &Credentials{
		PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINF... test-key",
	}

	// Create context with immediate cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := RegisterWithCoordinator(ctx, server.URL, creds, true, "")
	if err == nil {
		t.Error("Expected error when context is cancelled, got nil")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Logf("Error was: %v", err) // Log for debugging
	}
}
