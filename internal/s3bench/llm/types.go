package llm

import (
	"context"
	"time"
)

// Client is an interface for LLM content generation
type Client interface {
	// Generate creates content based on the provided request
	Generate(ctx context.Context, req GenerateRequest) (string, error)

	// IsAvailable checks if the LLM service is reachable
	IsAvailable(ctx context.Context) bool
}

// Config contains configuration for an LLM client
type Config struct {
	// Endpoint is the base URL for the LLM API (e.g., "http://honker:11434")
	Endpoint string

	// Model is the model name to use (e.g., "gpt-oss:120b")
	Model string

	// Timeout is the maximum time to wait for a response
	Timeout time.Duration

	// MaxRetries is the number of times to retry failed requests
	MaxRetries int
}

// GenerateRequest contains parameters for content generation
type GenerateRequest struct {
	// Prompt is the main instruction for the LLM
	Prompt string

	// System is the system message that sets the context
	System string

	// Temperature controls randomness (0.0 = deterministic, 1.0 = creative)
	// Use 0.7 for variety, 0.3 for consistency
	Temperature float64

	// MaxTokens limits the response length (0 = unlimited)
	MaxTokens int
}

// DefaultConfig returns a Config with sensible defaults
func DefaultConfig() Config {
	return Config{
		Endpoint:   "http://localhost:11434",
		Model:      "gpt-oss:120b",
		Timeout:    60 * time.Second,
		MaxRetries: 3,
	}
}
