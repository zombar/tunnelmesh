package mesh

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
)

// CoordinatorClient provides HTTP access to the coordinator's S3 API.
type CoordinatorClient struct {
	baseURL    string // Base URL for coordinator (https://10.42.0.1:443)
	accessKey  string
	secretKey  string
	httpClient *http.Client
}

// NewCoordinatorClient creates a new coordinator S3 API client.
// meshIP should be the coordinator's mesh IP (e.g., "10.42.0.1").
func NewCoordinatorClient(meshIP string, creds *Credentials, insecureSkipVerify bool) *CoordinatorClient {
	return &CoordinatorClient{
		baseURL:   fmt.Sprintf("https://%s:443", meshIP),
		accessKey: creds.AccessKey,
		secretKey: creds.SecretKey,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: insecureSkipVerify,
				},
			},
		},
	}
}

// CreateBucket creates a new S3 bucket on the coordinator.
func (c *CoordinatorClient) CreateBucket(ctx context.Context, bucketName, ownerID string, quotaMB int64) error {
	url := fmt.Sprintf("%s/api/s3/buckets", c.baseURL)

	// Build request payload
	payload := map[string]interface{}{
		"name": bucketName,
	}
	if ownerID != "" {
		payload["owner_id"] = ownerID
	}
	if quotaMB > 0 {
		payload["quota_bytes"] = quotaMB * 1024 * 1024
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	c.setBasicAuth(req)

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP POST %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Check response
	if resp.StatusCode == http.StatusConflict {
		// Bucket already exists - this is fine (idempotent)
		return nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create bucket failed: %d %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// BucketExists checks if a bucket exists on the coordinator.
func (c *CoordinatorClient) BucketExists(ctx context.Context, bucketName string) (bool, error) {
	url := fmt.Sprintf("%s/api/s3/buckets/%s", c.baseURL, bucketName)

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, fmt.Errorf("create request: %w", err)
	}
	c.setBasicAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("HTTP HEAD %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}

	return false, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
}

// PutObject uploads an object to the coordinator S3 API.
func (c *CoordinatorClient) PutObject(ctx context.Context, bucket, key string, body []byte, contentType string, metadata map[string]string) error {
	url := fmt.Sprintf("%s/api/s3/buckets/%s/objects/%s", c.baseURL, bucket, key)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	c.setBasicAuth(req)

	// Add metadata as x-amz-meta- headers
	for k, v := range metadata {
		req.Header.Set("x-amz-meta-"+k, v)
	}

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP PUT %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Check response
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("put object failed: %d %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// setBasicAuth sets HTTP Basic Auth header using S3 credentials.
func (c *CoordinatorClient) setBasicAuth(req *http.Request) {
	auth := c.accessKey + ":" + c.secretKey
	encoded := base64.StdEncoding.EncodeToString([]byte(auth))
	req.Header.Set("Authorization", "Basic "+encoded)
}

// ListBuckets returns all buckets accessible to the authenticated user.
func (c *CoordinatorClient) ListBuckets(ctx context.Context) ([]s3.BucketMeta, error) {
	url := fmt.Sprintf("%s/api/s3/buckets", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.setBasicAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list buckets failed: %d %s", resp.StatusCode, string(bodyBytes))
	}

	var buckets []s3.BucketMeta
	if err := json.NewDecoder(resp.Body).Decode(&buckets); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return buckets, nil
}
