package audit

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestNewLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	auditLogger := NewLogger(logger)

	if auditLogger == nil {
		t.Fatal("NewLogger returned nil")
	}
}

func TestLogAuth(t *testing.T) {
	tests := []struct {
		name      string
		userID    string
		method    string
		result    string
		details   string
		sourceIP  string
		wantLevel string
	}{
		{
			name:      "successful auth",
			userID:    "alice@example.com",
			method:    "aws_sigv4",
			result:    "allowed",
			details:   "valid signature",
			sourceIP:  "172.30.0.5",
			wantLevel: "info",
		},
		{
			name:      "failed auth",
			userID:    "",
			method:    "basic",
			result:    "denied",
			details:   "invalid credentials",
			sourceIP:  "172.30.0.6",
			wantLevel: "warn",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := zerolog.New(&buf)
			auditLogger := NewLogger(logger)

			auditLogger.LogAuth(tt.userID, tt.method, tt.result, tt.details, tt.sourceIP)

			var logEntry map[string]interface{}
			if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
				t.Fatalf("failed to unmarshal log entry: %v", err)
			}

			// Check standard fields
			if got := logEntry["level"]; got != tt.wantLevel {
				t.Errorf("level = %v, want %v", got, tt.wantLevel)
			}
			if got := logEntry["event_type"]; got != "auth" {
				t.Errorf("event_type = %v, want auth", got)
			}
			if got := logEntry["method"]; got != tt.method {
				t.Errorf("method = %v, want %v", got, tt.method)
			}
			if got := logEntry["result"]; got != tt.result {
				t.Errorf("result = %v, want %v", got, tt.result)
			}
			if got := logEntry["source_ip"]; got != tt.sourceIP {
				t.Errorf("source_ip = %v, want %v", got, tt.sourceIP)
			}

			// user_id may be empty for failed auth
			if tt.userID != "" {
				if got := logEntry["user_id"]; got != tt.userID {
					t.Errorf("user_id = %v, want %v", got, tt.userID)
				}
			}
		})
	}
}

func TestLogAuthz(t *testing.T) {
	tests := []struct {
		name      string
		userID    string
		verb      string
		resource  string
		bucket    string
		objectKey string
		result    string
		reason    string
		wantLevel string
	}{
		{
			name:      "allowed access",
			userID:    "bob@example.com",
			verb:      "get",
			resource:  "objects",
			bucket:    "shared-files",
			objectKey: "data.txt",
			result:    "allowed",
			reason:    "",
			wantLevel: "info",
		},
		{
			name:      "denied access",
			userID:    "charlie@example.com",
			verb:      "put",
			resource:  "objects",
			bucket:    "private-files",
			objectKey: "secret.txt",
			result:    "denied",
			reason:    "no matching role binding",
			wantLevel: "warn",
		},
		{
			name:      "bucket operation",
			userID:    "dave@example.com",
			verb:      "list",
			resource:  "objects",
			bucket:    "public-bucket",
			objectKey: "",
			result:    "allowed",
			reason:    "",
			wantLevel: "info",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := zerolog.New(&buf)
			auditLogger := NewLogger(logger)

			auditLogger.LogAuthz(tt.userID, tt.verb, tt.resource, tt.bucket, tt.objectKey, tt.result, tt.reason)

			var logEntry map[string]interface{}
			if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
				t.Fatalf("failed to unmarshal log entry: %v", err)
			}

			// Check standard fields
			if got := logEntry["level"]; got != tt.wantLevel {
				t.Errorf("level = %v, want %v", got, tt.wantLevel)
			}
			if got := logEntry["event_type"]; got != "authz" {
				t.Errorf("event_type = %v, want authz", got)
			}
			if got := logEntry["user_id"]; got != tt.userID {
				t.Errorf("user_id = %v, want %v", got, tt.userID)
			}
			if got := logEntry["verb"]; got != tt.verb {
				t.Errorf("verb = %v, want %v", got, tt.verb)
			}
			if got := logEntry["resource"]; got != tt.resource {
				t.Errorf("resource = %v, want %v", got, tt.resource)
			}
			if got := logEntry["bucket"]; got != tt.bucket {
				t.Errorf("bucket = %v, want %v", got, tt.bucket)
			}
			if got := logEntry["result"]; got != tt.result {
				t.Errorf("result = %v, want %v", got, tt.result)
			}

			// object_key and reason are optional
			if tt.objectKey != "" {
				if got := logEntry["object_key"]; got != tt.objectKey {
					t.Errorf("object_key = %v, want %v", got, tt.objectKey)
				}
			}
			if tt.reason != "" {
				if got := logEntry["reason"]; got != tt.reason {
					t.Errorf("reason = %v, want %v", got, tt.reason)
				}
			}
		})
	}
}

// nolint:dupl // Audit test cases follow similar structure for different scenarios
func TestLogS3Op(t *testing.T) {
	tests := []struct {
		name      string
		userID    string
		operation string
		bucket    string
		objectKey string
		result    string
		details   string
		sourceIP  string
		wantLevel string
	}{
		{
			name:      "successful get",
			userID:    "eve@example.com",
			operation: "GetObject",
			bucket:    "my-bucket",
			objectKey: "file.pdf",
			result:    "allowed",
			details:   "",
			sourceIP:  "172.30.0.7",
			wantLevel: "info",
		},
		{
			name:      "denied put",
			userID:    "frank@example.com",
			operation: "PutObject",
			bucket:    "restricted",
			objectKey: "upload.dat",
			result:    "denied",
			details:   "quota exceeded",
			sourceIP:  "172.30.0.8",
			wantLevel: "warn",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := zerolog.New(&buf)
			auditLogger := NewLogger(logger)

			auditLogger.LogS3Op(tt.userID, tt.operation, tt.bucket, tt.objectKey, tt.result, tt.details, tt.sourceIP)

			var logEntry map[string]interface{}
			if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
				t.Fatalf("failed to unmarshal log entry: %v", err)
			}

			// Check standard fields
			if got := logEntry["level"]; got != tt.wantLevel {
				t.Errorf("level = %v, want %v", got, tt.wantLevel)
			}
			if got := logEntry["event_type"]; got != "s3_operation" {
				t.Errorf("event_type = %v, want s3_operation", got)
			}
			if got := logEntry["component"]; got != "s3" {
				t.Errorf("component = %v, want s3", got)
			}
			if got := logEntry["user_id"]; got != tt.userID {
				t.Errorf("user_id = %v, want %v", got, tt.userID)
			}
			if got := logEntry["operation"]; got != tt.operation {
				t.Errorf("operation = %v, want %v", got, tt.operation)
			}
			if got := logEntry["bucket"]; got != tt.bucket {
				t.Errorf("bucket = %v, want %v", got, tt.bucket)
			}
			if got := logEntry["result"]; got != tt.result {
				t.Errorf("result = %v, want %v", got, tt.result)
			}
			if got := logEntry["source_ip"]; got != tt.sourceIP {
				t.Errorf("source_ip = %v, want %v", got, tt.sourceIP)
			}

			// object_key and details are optional
			if tt.objectKey != "" {
				if got := logEntry["object_key"]; got != tt.objectKey {
					t.Errorf("object_key = %v, want %v", got, tt.objectKey)
				}
			}
			if tt.details != "" {
				if got := logEntry["details"]; got != tt.details {
					t.Errorf("details = %v, want %v", got, tt.details)
				}
			}
		})
	}
}

// nolint:dupl // Companion test case
func TestLogNFSOp(t *testing.T) {
	tests := []struct {
		name      string
		userID    string
		operation string
		share     string
		path      string
		result    string
		details   string
		sourceIP  string
		wantLevel string
	}{
		{
			name:      "successful mount",
			userID:    "grace@example.com",
			operation: "Mount",
			share:     "project-files",
			path:      "",
			result:    "allowed",
			details:   "readonly=false",
			sourceIP:  "172.30.0.9",
			wantLevel: "info",
		},
		{
			name:      "denied read",
			userID:    "henry@example.com",
			operation: "Read",
			share:     "secure-data",
			path:      "/sensitive.txt",
			result:    "denied",
			details:   "access denied",
			sourceIP:  "172.30.0.10",
			wantLevel: "warn",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := zerolog.New(&buf)
			auditLogger := NewLogger(logger)

			auditLogger.LogNFSOp(tt.userID, tt.operation, tt.share, tt.path, tt.result, tt.details, tt.sourceIP)

			var logEntry map[string]interface{}
			if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
				t.Fatalf("failed to unmarshal log entry: %v", err)
			}

			// Check standard fields
			if got := logEntry["level"]; got != tt.wantLevel {
				t.Errorf("level = %v, want %v", got, tt.wantLevel)
			}
			if got := logEntry["event_type"]; got != "nfs_operation" {
				t.Errorf("event_type = %v, want nfs_operation", got)
			}
			if got := logEntry["component"]; got != "nfs" {
				t.Errorf("component = %v, want nfs", got)
			}
			if got := logEntry["user_id"]; got != tt.userID {
				t.Errorf("user_id = %v, want %v", got, tt.userID)
			}
			if got := logEntry["operation"]; got != tt.operation {
				t.Errorf("operation = %v, want %v", got, tt.operation)
			}
			if got := logEntry["share"]; got != tt.share {
				t.Errorf("share = %v, want %v", got, tt.share)
			}
			if got := logEntry["result"]; got != tt.result {
				t.Errorf("result = %v, want %v", got, tt.result)
			}
			if got := logEntry["source_ip"]; got != tt.sourceIP {
				t.Errorf("source_ip = %v, want %v", got, tt.sourceIP)
			}

			// path and details are optional
			if tt.path != "" {
				if got := logEntry["path"]; got != tt.path {
					t.Errorf("path = %v, want %v", got, tt.path)
				}
			}
			if tt.details != "" {
				if got := logEntry["details"]; got != tt.details {
					t.Errorf("details = %v, want %v", got, tt.details)
				}
			}
		})
	}
}

func TestLogUserMgmt(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	auditLogger := NewLogger(logger)

	auditLogger.LogUserMgmt("admin@example.com", "create_user", "newuser@example.com", "created via admin API")

	var logEntry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("failed to unmarshal log entry: %v", err)
	}

	if got := logEntry["level"]; got != "info" {
		t.Errorf("level = %v, want info", got)
	}
	if got := logEntry["event_type"]; got != "user_management" {
		t.Errorf("event_type = %v, want user_management", got)
	}
	if got := logEntry["admin_id"]; got != "admin@example.com" {
		t.Errorf("admin_id = %v, want admin@example.com", got)
	}
	if got := logEntry["action"]; got != "create_user" {
		t.Errorf("action = %v, want create_user", got)
	}
	if got := logEntry["target_user_id"]; got != "newuser@example.com" {
		t.Errorf("target_user_id = %v, want newuser@example.com", got)
	}
}

func TestLogRoleBinding(t *testing.T) {
	tests := []struct {
		name    string
		adminID string
		action  string
		userID  string
		role    string
		bucket  string
		details string
	}{
		{
			name:    "add global binding",
			adminID: "admin@example.com",
			action:  "add_binding",
			userID:  "user@example.com",
			role:    "admin",
			bucket:  "",
			details: "granted admin access",
		},
		{
			name:    "add bucket binding",
			adminID: "admin@example.com",
			action:  "add_binding",
			userID:  "user@example.com",
			role:    "reader",
			bucket:  "project-data",
			details: "granted read access to bucket",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := zerolog.New(&buf)
			auditLogger := NewLogger(logger)

			auditLogger.LogRoleBinding(tt.adminID, tt.action, tt.userID, tt.role, tt.bucket, tt.details)

			var logEntry map[string]interface{}
			if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
				t.Fatalf("failed to unmarshal log entry: %v", err)
			}

			if got := logEntry["level"]; got != "info" {
				t.Errorf("level = %v, want info", got)
			}
			if got := logEntry["event_type"]; got != "role_binding" {
				t.Errorf("event_type = %v, want role_binding", got)
			}
			if got := logEntry["admin_id"]; got != tt.adminID {
				t.Errorf("admin_id = %v, want %v", got, tt.adminID)
			}
			if got := logEntry["action"]; got != tt.action {
				t.Errorf("action = %v, want %v", got, tt.action)
			}
			if got := logEntry["user_id"]; got != tt.userID {
				t.Errorf("user_id = %v, want %v", got, tt.userID)
			}
			if got := logEntry["role"]; got != tt.role {
				t.Errorf("role = %v, want %v", got, tt.role)
			}

			// bucket and details are optional
			if tt.bucket != "" {
				if got := logEntry["bucket"]; got != tt.bucket {
					t.Errorf("bucket = %v, want %v", got, tt.bucket)
				}
			}
			if tt.details != "" {
				if got := logEntry["details"]; got != tt.details {
					t.Errorf("details = %v, want %v", got, tt.details)
				}
			}
		})
	}
}

	// nolint:revive // t required by test helper signature but not used
func TestNilLogger(t *testing.T) {
	// Test that calling methods on a logger with noop logger doesn't panic
	logger := zerolog.Nop()
	auditLogger := NewLogger(logger)

	// These should all complete without panic
	auditLogger.LogAuth("user", "method", "allowed", "details", "127.0.0.1")
	auditLogger.LogAuthz("user", "get", "objects", "bucket", "key", "allowed", "")
	auditLogger.LogS3Op("user", "GetObject", "bucket", "key", "allowed", "", "127.0.0.1")
	auditLogger.LogNFSOp("user", "Read", "share", "/path", "allowed", "", "127.0.0.1")
	auditLogger.LogUserMgmt("admin", "create_user", "newuser", "created")
	auditLogger.LogRoleBinding("admin", "add_binding", "user", "admin", "", "granted")
}

func TestMessageContent(t *testing.T) {
	// Verify that message field contains expected strings
	tests := []struct {
		name        string
		logFunc     func(*Logger)
		wantMessage string
	}{
		{
			name: "auth message",
			logFunc: func(l *Logger) {
				l.LogAuth("user", "basic", "allowed", "", "127.0.0.1")
			},
			wantMessage: "Authentication event",
		},
		{
			name: "authz message",
			logFunc: func(l *Logger) {
				l.LogAuthz("user", "get", "objects", "bucket", "", "allowed", "")
			},
			wantMessage: "Authorization event",
		},
		{
			name: "s3 message",
			logFunc: func(l *Logger) {
				l.LogS3Op("user", "GetObject", "bucket", "key", "allowed", "", "127.0.0.1")
			},
			wantMessage: "S3 operation",
		},
		{
			name: "nfs message",
			logFunc: func(l *Logger) {
				l.LogNFSOp("user", "Read", "share", "/path", "allowed", "", "127.0.0.1")
			},
			wantMessage: "NFS operation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := zerolog.New(&buf)
			auditLogger := NewLogger(logger)

			tt.logFunc(auditLogger)

			var logEntry map[string]interface{}
			if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
				t.Fatalf("failed to unmarshal log entry: %v", err)
			}

			message, ok := logEntry["message"].(string)
			if !ok {
				t.Fatal("message field not found or not a string")
			}

			if !strings.Contains(message, tt.wantMessage) {
				t.Errorf("message = %q, want to contain %q", message, tt.wantMessage)
			}
		})
	}
}
