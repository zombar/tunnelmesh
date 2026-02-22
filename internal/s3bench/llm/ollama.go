package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// OllamaClient implements the Client interface for Ollama API
type OllamaClient struct {
	config Config
	client *http.Client
}

// NewOllamaClient creates a new Ollama client with the given configuration
func NewOllamaClient(config Config) (*OllamaClient, error) {
	if config.Endpoint == "" {
		return nil, fmt.Errorf("endpoint is required")
	}
	if config.Model == "" {
		return nil, fmt.Errorf("model is required")
	}
	if config.Timeout == 0 {
		config.Timeout = 60 * time.Second
	}
	if config.MaxRetries == 0 {
		config.MaxRetries = 3
	}

	// Ensure endpoint doesn't have trailing slash
	config.Endpoint = strings.TrimRight(config.Endpoint, "/")

	return &OllamaClient{
		config: config,
		client: &http.Client{
			Timeout: config.Timeout,
		},
	}, nil
}

// ollamaGenerateRequest matches Ollama's /api/generate endpoint schema
type ollamaGenerateRequest struct {
	Model       string                 `json:"model"`
	Prompt      string                 `json:"prompt"`
	System      string                 `json:"system,omitempty"`
	Temperature float64                `json:"temperature,omitempty"`
	Stream      bool                   `json:"stream"`
	Options     map[string]interface{} `json:"options,omitempty"`
}

// ollamaGenerateResponse matches Ollama's /api/generate endpoint response
type ollamaGenerateResponse struct {
	Model     string `json:"model"`
	Response  string `json:"response"`
	Done      bool   `json:"done"`
	Error     string `json:"error,omitempty"`
	CreatedAt string `json:"created_at"`
}

// Generate creates content using Ollama's generate API with automatic retries
func (c *OllamaClient) Generate(ctx context.Context, req GenerateRequest) (string, error) {
	var lastErr error

	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1s, 2s, 4s
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			log.Debug().
				Int("attempt", attempt).
				Dur("backoff", backoff).
				Msg("Retrying Ollama request after backoff")

			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(backoff):
			}
		}

		response, err := c.generateOnce(ctx, req)
		if err == nil {
			return response, nil
		}

		lastErr = err
		log.Warn().
			Err(err).
			Int("attempt", attempt+1).
			Int("max_retries", c.config.MaxRetries).
			Msg("Ollama request failed")
	}

	return "", fmt.Errorf("failed after %d retries: %w", c.config.MaxRetries, lastErr)
}

// generateOnce performs a single generation request
func (c *OllamaClient) generateOnce(ctx context.Context, req GenerateRequest) (string, error) {
	ollamaReq := ollamaGenerateRequest{
		Model:       c.config.Model,
		Prompt:      req.Prompt,
		System:      req.System,
		Temperature: req.Temperature,
		Stream:      false,
	}

	if req.MaxTokens > 0 {
		ollamaReq.Options = map[string]interface{}{
			"num_predict": req.MaxTokens,
		}
	}

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.config.Endpoint + "/api/generate"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var ollamaResp ollamaGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if ollamaResp.Error != "" {
		return "", fmt.Errorf("ollama error: %s", ollamaResp.Error)
	}

	if !ollamaResp.Done {
		return "", fmt.Errorf("incomplete response (done=false)")
	}

	return ollamaResp.Response, nil
}

// IsAvailable checks if the Ollama service is reachable and the model is available
func (c *OllamaClient) IsAvailable(ctx context.Context) bool {
	url := c.config.Endpoint + "/api/tags"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		log.Debug().Err(err).Msg("Failed to create health check request")
		return false
	}

	resp, err := c.client.Do(req)
	if err != nil {
		log.Debug().Err(err).Msg("Failed to connect to Ollama")
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		log.Debug().Int("status", resp.StatusCode).Msg("Ollama health check failed")
		return false
	}

	// Parse response to check if model is available
	var tagsResp struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tagsResp); err != nil {
		log.Debug().Err(err).Msg("Failed to decode tags response")
		return false
	}

	// Check if our model is in the list
	for _, model := range tagsResp.Models {
		if model.Name == c.config.Model {
			return true
		}
	}

	log.Debug().
		Str("model", c.config.Model).
		Int("available_models", len(tagsResp.Models)).
		Msg("Model not found in Ollama")
	return false
}
