package mesh

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCoordinatorClient_CreateBucket(t *testing.T) {
	tests := []struct {
		name           string
		bucketName     string
		ownerID        string
		quotaMB        int64
		serverStatus   int
		serverResponse string
		expectError    bool
		errorContains  string
	}{
		{
			name:         "successful creation",
			bucketName:   "test-bucket",
			ownerID:      "user123",
			quotaMB:      1000,
			serverStatus: http.StatusOK,
			expectError:  false,
		},
		{
			name:         "bucket already exists (conflict)",
			bucketName:   "existing-bucket",
			ownerID:      "user123",
			quotaMB:      1000,
			serverStatus: http.StatusConflict,
			expectError:  false, // Should handle gracefully
		},
		{
			name:           "server error",
			bucketName:     "test-bucket",
			ownerID:        "user123",
			quotaMB:        1000,
			serverStatus:   http.StatusInternalServerError,
			serverResponse: "internal error",
			expectError:    true,
			errorContains:  "500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("Expected POST, got %s", r.Method)
				}
				if !strings.Contains(r.URL.Path, "/api/s3/buckets") {
					t.Errorf("Expected /api/s3/buckets in path, got %s", r.URL.Path)
				}

				w.WriteHeader(tt.serverStatus)
				if tt.serverResponse != "" {
					_, _ = w.Write([]byte(tt.serverResponse))
				}
			}))
			defer server.Close()

			client := &CoordinatorClient{
				baseURLs:   []string{server.URL},
				httpClient: server.Client(),
				accessKey:  "test-access",
				secretKey:  "test-secret",
			}

			err := client.CreateBucket(context.Background(), tt.bucketName, tt.ownerID, tt.quotaMB)

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
			}
		})
	}
}

func TestCoordinatorClient_PutObject(t *testing.T) {
	tests := []struct {
		name          string
		bucket        string
		key           string
		body          []byte
		contentType   string
		metadata      map[string]string
		serverStatus  int
		expectError   bool
		errorContains string
	}{
		{
			name:         "successful upload",
			bucket:       "test-bucket",
			key:          "test-file.txt",
			body:         []byte("test content"),
			contentType:  "text/plain",
			metadata:     map[string]string{"author": "test"},
			serverStatus: http.StatusOK,
			expectError:  false,
		},
		{
			name:         "key with special characters",
			bucket:       "test-bucket",
			key:          "folder/sub folder/file name.txt",
			body:         []byte("test content"),
			contentType:  "text/plain",
			serverStatus: http.StatusOK,
			expectError:  false,
		},
		{
			name:          "server error",
			bucket:        "test-bucket",
			key:           "test-file.txt",
			body:          []byte("test content"),
			contentType:   "text/plain",
			serverStatus:  http.StatusInternalServerError,
			expectError:   true,
			errorContains: "500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut {
					t.Errorf("Expected PUT, got %s", r.Method)
				}

				// Verify headers
				if r.Header.Get("Content-Type") != tt.contentType {
					t.Errorf("Expected Content-Type %s, got %s", tt.contentType, r.Header.Get("Content-Type"))
				}

				// Verify metadata headers
				for k, v := range tt.metadata {
					headerKey := "x-amz-meta-" + k
					if r.Header.Get(headerKey) != v {
						t.Errorf("Expected header %s=%s, got %s", headerKey, v, r.Header.Get(headerKey))
					}
				}

				// Verify body
				body, _ := io.ReadAll(r.Body)
				if string(body) != string(tt.body) {
					t.Errorf("Expected body %q, got %q", string(tt.body), string(body))
				}

				w.WriteHeader(tt.serverStatus)
			}))
			defer server.Close()

			client := &CoordinatorClient{
				baseURLs:   []string{server.URL},
				httpClient: server.Client(),
				accessKey:  "test-access",
				secretKey:  "test-secret",
			}

			err := client.PutObject(context.Background(), tt.bucket, tt.key, tt.body, tt.contentType, tt.metadata)

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
			}
		})
	}
}

func TestCoordinatorClient_GetObject(t *testing.T) {
	tests := []struct {
		name          string
		bucket        string
		key           string
		serverStatus  int
		serverBody    string
		expectError   bool
		errorContains string
	}{
		{
			name:         "successful download",
			bucket:       "test-bucket",
			key:          "test-file.txt",
			serverStatus: http.StatusOK,
			serverBody:   "file content here",
			expectError:  false,
		},
		{
			name:          "object not found",
			bucket:        "test-bucket",
			key:           "missing-file.txt",
			serverStatus:  http.StatusNotFound,
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:         "key with special characters",
			bucket:       "test-bucket",
			key:          "folder/file with spaces.txt",
			serverStatus: http.StatusOK,
			serverBody:   "content",
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("Expected GET, got %s", r.Method)
				}

				w.WriteHeader(tt.serverStatus)
				if tt.serverBody != "" {
					_, _ = w.Write([]byte(tt.serverBody))
				}
			}))
			defer server.Close()

			client := &CoordinatorClient{
				baseURLs:   []string{server.URL},
				httpClient: server.Client(),
				accessKey:  "test-access",
				secretKey:  "test-secret",
			}

			data, err := client.GetObject(context.Background(), tt.bucket, tt.key)

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
				if string(data) != tt.serverBody {
					t.Errorf("Expected body %q, got %q", tt.serverBody, string(data))
				}
			}
		})
	}
}

func TestCoordinatorClient_DeleteObject(t *testing.T) {
	tests := []struct {
		name          string
		bucket        string
		key           string
		serverStatus  int
		expectError   bool
		errorContains string
	}{
		{
			name:         "successful deletion",
			bucket:       "test-bucket",
			key:          "test-file.txt",
			serverStatus: http.StatusOK,
			expectError:  false,
		},
		{
			name:         "deletion with 204 No Content",
			bucket:       "test-bucket",
			key:          "test-file.txt",
			serverStatus: http.StatusNoContent,
			expectError:  false,
		},
		{
			name:          "object not found",
			bucket:        "test-bucket",
			key:           "missing-file.txt",
			serverStatus:  http.StatusNotFound,
			expectError:   true,
			errorContains: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete {
					t.Errorf("Expected DELETE, got %s", r.Method)
				}

				w.WriteHeader(tt.serverStatus)
			}))
			defer server.Close()

			client := &CoordinatorClient{
				baseURLs:   []string{server.URL},
				httpClient: server.Client(),
				accessKey:  "test-access",
				secretKey:  "test-secret",
			}

			err := client.DeleteObject(context.Background(), tt.bucket, tt.key)

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
			}
		})
	}
}

func TestCoordinatorClient_BucketExists(t *testing.T) {
	tests := []struct {
		name         string
		bucketName   string
		serverStatus int
		expectExists bool
		expectError  bool
	}{
		{
			name:         "bucket exists",
			bucketName:   "existing-bucket",
			serverStatus: http.StatusOK,
			expectExists: true,
			expectError:  false,
		},
		{
			name:         "bucket does not exist",
			bucketName:   "missing-bucket",
			serverStatus: http.StatusNotFound,
			expectExists: false,
			expectError:  false,
		},
		{
			name:         "server error",
			bucketName:   "test-bucket",
			serverStatus: http.StatusInternalServerError,
			expectExists: false,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodHead {
					t.Errorf("Expected HEAD, got %s", r.Method)
				}

				w.WriteHeader(tt.serverStatus)
			}))
			defer server.Close()

			client := &CoordinatorClient{
				baseURLs:   []string{server.URL},
				httpClient: server.Client(),
				accessKey:  "test-access",
				secretKey:  "test-secret",
			}

			exists, err := client.BucketExists(context.Background(), tt.bucketName)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got %v", err)
				}
				if exists != tt.expectExists {
					t.Errorf("Expected exists=%v, got %v", tt.expectExists, exists)
				}
			}
		})
	}
}

func TestCoordinatorClient_URLEscaping(t *testing.T) {
	// Test that special characters in bucket/key are properly escaped
	var capturedRawPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// RawPath contains the encoded form, Path contains decoded form
		// RawPath is only set if encoding was necessary
		if r.URL.RawPath != "" {
			capturedRawPath = r.URL.RawPath
		} else {
			capturedRawPath = r.URL.Path
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &CoordinatorClient{
		baseURLs:   []string{server.URL},
		httpClient: server.Client(),
		accessKey:  "test",
		secretKey:  "test",
	}

	// Test with spaces and special characters
	bucket := "my-bucket"
	key := "folder/file with spaces.txt"

	_ = client.PutObject(context.Background(), bucket, key, []byte("test"), "text/plain", nil)

	// URL should have slashes and spaces escaped
	expectedPath := fmt.Sprintf("/api/s3/buckets/%s/objects/%s", bucket, "folder%2Ffile%20with%20spaces.txt")
	if capturedRawPath != expectedPath {
		t.Errorf("Expected URL path %q, got %q", expectedPath, capturedRawPath)
	}
}

func TestCoordinatorClient_RoundRobin(t *testing.T) {
	// Track which servers receive requests
	hits := make([]int, 3)

	servers := make([]*httptest.Server, 3)
	for i := range servers {
		idx := i
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits[idx]++
			w.WriteHeader(http.StatusOK)
		}))
		defer servers[i].Close()
	}

	urls := make([]string, 3)
	for i, s := range servers {
		urls[i] = s.URL
	}

	creds := &Credentials{AccessKey: "test", SecretKey: "test"}
	client := NewCoordinatorClientMulti(urls, creds, true)
	// Override httpClient to allow connections to test servers
	client.httpClient = servers[0].Client()

	ctx := context.Background()

	// Send 9 PutObject requests — should distribute 3 to each server
	for i := 0; i < 9; i++ {
		_ = client.PutObject(ctx, "bucket", fmt.Sprintf("key-%d", i), []byte("data"), "text/plain", nil)
	}

	for i, h := range hits {
		if h != 3 {
			t.Errorf("Server %d received %d requests, expected 3", i, h)
		}
	}
}
