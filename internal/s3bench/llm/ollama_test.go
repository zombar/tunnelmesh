package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewOllamaClient(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: Config{
				Endpoint:   "http://localhost:11434",
				Model:      "gpt-oss:120b",
				Timeout:    30 * time.Second,
				MaxRetries: 3,
			},
			wantErr: false,
		},
		{
			name: "missing endpoint",
			config: Config{
				Model:      "gpt-oss:120b",
				Timeout:    30 * time.Second,
				MaxRetries: 3,
			},
			wantErr: true,
		},
		{
			name: "missing model",
			config: Config{
				Endpoint:   "http://localhost:11434",
				Timeout:    30 * time.Second,
				MaxRetries: 3,
			},
			wantErr: true,
		},
		{
			name: "default timeout and retries",
			config: Config{
				Endpoint: "http://localhost:11434",
				Model:    "gpt-oss:120b",
			},
			wantErr: false,
		},
		{
			name: "trailing slash removed",
			config: Config{
				Endpoint: "http://localhost:11434/",
				Model:    "gpt-oss:120b",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewOllamaClient(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewOllamaClient() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if client == nil {
					t.Error("NewOllamaClient() returned nil client")
				} else if strings.HasSuffix(client.config.Endpoint, "/") { //nolint:staticcheck // intentionally checking nil first
					t.Error("Endpoint should not have trailing slash")
				}
			}
		})
	}
}

func TestOllamaClient_Generate(t *testing.T) {
	tests := []struct {
		name           string
		request        GenerateRequest
		serverResponse ollamaGenerateResponse
		serverStatus   int
		wantErr        bool
		wantContains   string
	}{
		{
			name: "successful generation",
			request: GenerateRequest{
				Prompt:      "Generate a battle report",
				System:      "You are a military officer",
				Temperature: 0.7,
			},
			serverResponse: ollamaGenerateResponse{
				Model:    "gpt-oss:120b",
				Response: "Enemy forces engaged at grid reference...",
				Done:     true,
			},
			serverStatus: http.StatusOK,
			wantErr:      false,
			wantContains: "Enemy forces",
		},
		{
			name: "server error",
			request: GenerateRequest{
				Prompt: "Generate content",
			},
			serverResponse: ollamaGenerateResponse{},
			serverStatus:   http.StatusInternalServerError,
			wantErr:        true,
		},
		{
			name: "ollama error response",
			request: GenerateRequest{
				Prompt: "Generate content",
			},
			serverResponse: ollamaGenerateResponse{
				Error: "model not found",
				Done:  false,
			},
			serverStatus: http.StatusOK,
			wantErr:      true,
			wantContains: "model not found",
		},
		{
			name: "incomplete response",
			request: GenerateRequest{
				Prompt: "Generate content",
			},
			serverResponse: ollamaGenerateResponse{
				Response: "Partial response",
				Done:     false,
			},
			serverStatus: http.StatusOK,
			wantErr:      true,
			wantContains: "incomplete response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/generate" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Method != "POST" {
					t.Errorf("unexpected method: %s", r.Method)
				}

				// Verify request body
				var req ollamaGenerateRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("failed to decode request: %v", err)
				}

				w.WriteHeader(tt.serverStatus)
				_ = json.NewEncoder(w).Encode(tt.serverResponse)
			}))
			defer server.Close()

			client, err := NewOllamaClient(Config{
				Endpoint:   server.URL,
				Model:      "gpt-oss:120b",
				Timeout:    5 * time.Second,
				MaxRetries: 0, // No retries for faster tests
			})
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			ctx := context.Background()
			response, err := client.Generate(ctx, tt.request)

			if (err != nil) != tt.wantErr {
				t.Errorf("Generate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && tt.wantContains != "" {
				if !strings.Contains(response, tt.wantContains) {
					t.Errorf("Generate() response = %v, want to contain %v", response, tt.wantContains)
				}
			}

			if tt.wantErr && tt.wantContains != "" {
				if err != nil && !strings.Contains(err.Error(), tt.wantContains) {
					t.Errorf("Generate() error = %v, want to contain %v", err, tt.wantContains)
				}
			}
		})
	}
}

func TestOllamaClient_GenerateWithRetries(t *testing.T) {
	attemptCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount++
		if attemptCount < 3 {
			// Fail first 2 attempts
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// Succeed on 3rd attempt
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ollamaGenerateResponse{
			Model:    "gpt-oss:120b",
			Response: "Success after retries",
			Done:     true,
		})
	}))
	defer server.Close()

	client, err := NewOllamaClient(Config{
		Endpoint:   server.URL,
		Model:      "gpt-oss:120b",
		Timeout:    5 * time.Second,
		MaxRetries: 3,
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()
	response, err := client.Generate(ctx, GenerateRequest{
		Prompt: "test",
	})

	if err != nil {
		t.Errorf("Generate() should succeed after retries, got error: %v", err)
	}

	if !strings.Contains(response, "Success after retries") {
		t.Errorf("unexpected response: %s", response)
	}

	if attemptCount != 3 {
		t.Errorf("expected 3 attempts, got %d", attemptCount)
	}
}

func TestOllamaClient_GenerateContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ollamaGenerateResponse{
			Response: "Should not see this",
			Done:     true,
		})
	}))
	defer server.Close()

	client, err := NewOllamaClient(Config{
		Endpoint:   server.URL,
		Model:      "gpt-oss:120b",
		Timeout:    5 * time.Second,
		MaxRetries: 0,
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err = client.Generate(ctx, GenerateRequest{
		Prompt: "test",
	})

	if err == nil {
		t.Error("Generate() should fail with context timeout")
	}
}

func TestOllamaClient_IsAvailable(t *testing.T) {
	tests := []struct {
		name          string
		modelName     string
		serverModels  []string
		serverStatus  int
		wantAvailable bool
	}{
		{
			name:      "model available",
			modelName: "gpt-oss:120b",
			serverModels: []string{
				"llama2",
				"gpt-oss:120b",
				"mistral",
			},
			serverStatus:  http.StatusOK,
			wantAvailable: true,
		},
		{
			name:      "model not found",
			modelName: "gpt-oss:120b",
			serverModels: []string{
				"llama2",
				"mistral",
			},
			serverStatus:  http.StatusOK,
			wantAvailable: false,
		},
		{
			name:          "server error",
			modelName:     "gpt-oss:120b",
			serverModels:  []string{},
			serverStatus:  http.StatusInternalServerError,
			wantAvailable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/tags" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				w.WriteHeader(tt.serverStatus)
				if tt.serverStatus == http.StatusOK {
					models := make([]map[string]string, len(tt.serverModels))
					for i, name := range tt.serverModels {
						models[i] = map[string]string{"name": name}
					}
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"models": models,
					})
				}
			}))
			defer server.Close()

			client, err := NewOllamaClient(Config{
				Endpoint:   server.URL,
				Model:      tt.modelName,
				Timeout:    5 * time.Second,
				MaxRetries: 0,
			})
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			ctx := context.Background()
			available := client.IsAvailable(ctx)

			if available != tt.wantAvailable {
				t.Errorf("IsAvailable() = %v, want %v", available, tt.wantAvailable)
			}
		})
	}
}
