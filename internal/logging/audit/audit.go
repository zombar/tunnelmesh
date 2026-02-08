package audit

import (
	"github.com/rs/zerolog"
)

// Logger provides structured audit logging for security-relevant events.
// All audit events are logged with structured fields for easy filtering and analysis.
type Logger struct {
	logger zerolog.Logger
}

// NewLogger creates a new audit logger from a zerolog.Logger.
// If logger is nil, a noop logger is returned that silently discards all log entries.
func NewLogger(logger zerolog.Logger) *Logger {
	return &Logger{logger: logger}
}

// LogAuth logs an authentication event.
// userID: the user attempting authentication (may be empty for failed attempts)
// method: authentication method (e.g., "aws_sigv4", "aws_sigv2", "basic", "bearer", "tls_cert", "password")
// result: "allowed" or "denied"
// details: additional context (e.g., error message)
// sourceIP: source IP address of the request
func (l *Logger) LogAuth(userID, method, result, details, sourceIP string) {
	level := zerolog.InfoLevel
	if result == "denied" {
		level = zerolog.WarnLevel
	}

	l.logger.WithLevel(level).
		Str("event_type", "auth").
		Str("user_id", userID).
		Str("method", method).
		Str("result", result).
		Str("details", details).
		Str("source_ip", sourceIP).
		Msg("Authentication event")
}

// LogAuthz logs an authorization event (RBAC permission check).
// userID: the user performing the operation
// verb: operation verb (e.g., "get", "put", "delete", "list")
// resource: resource type (e.g., "buckets", "objects")
// bucket: bucket name
// objectKey: object key (may be empty for bucket operations)
// result: "allowed" or "denied"
// reason: why access was denied (empty for allowed)
func (l *Logger) LogAuthz(userID, verb, resource, bucket, objectKey, result, reason string) {
	level := zerolog.InfoLevel
	if result == "denied" {
		level = zerolog.WarnLevel
	}

	event := l.logger.WithLevel(level).
		Str("event_type", "authz").
		Str("user_id", userID).
		Str("verb", verb).
		Str("resource", resource).
		Str("bucket", bucket).
		Str("result", result)

	if objectKey != "" {
		event = event.Str("object_key", objectKey)
	}
	if reason != "" {
		event = event.Str("reason", reason)
	}

	event.Msg("Authorization event")
}

// LogS3Op logs an S3 operation event.
// userID: the user performing the operation
// operation: S3 operation (e.g., "GetObject", "PutObject", "DeleteBucket")
// bucket: bucket name
// objectKey: object key (may be empty for bucket operations)
// result: "allowed" or "denied"
// details: additional context (e.g., error message)
// sourceIP: source IP address of the request
func (l *Logger) LogS3Op(userID, operation, bucket, objectKey, result, details, sourceIP string) {
	level := zerolog.InfoLevel
	if result == "denied" {
		level = zerolog.WarnLevel
	}

	event := l.logger.WithLevel(level).
		Str("event_type", "s3_operation").
		Str("component", "s3").
		Str("user_id", userID).
		Str("operation", operation).
		Str("bucket", bucket).
		Str("result", result).
		Str("source_ip", sourceIP)

	if objectKey != "" {
		event = event.Str("object_key", objectKey)
	}
	if details != "" {
		event = event.Str("details", details)
	}

	event.Msg("S3 operation")
}

// LogNFSOp logs an NFS operation event.
// userID: the user performing the operation
// operation: NFS operation (e.g., "Mount", "Read", "Write", "Open")
// share: share name
// path: file path within the share (may be empty for mount operations)
// result: "allowed" or "denied"
// details: additional context (e.g., error message)
// sourceIP: source IP address of the connection
func (l *Logger) LogNFSOp(userID, operation, share, path, result, details, sourceIP string) {
	level := zerolog.InfoLevel
	if result == "denied" {
		level = zerolog.WarnLevel
	}

	event := l.logger.WithLevel(level).
		Str("event_type", "nfs_operation").
		Str("component", "nfs").
		Str("user_id", userID).
		Str("operation", operation).
		Str("share", share).
		Str("result", result).
		Str("source_ip", sourceIP)

	if path != "" {
		event = event.Str("path", path)
	}
	if details != "" {
		event = event.Str("details", details)
	}

	event.Msg("NFS operation")
}

// LogUserMgmt logs a user management event.
// adminID: the admin performing the action
// action: action performed (e.g., "create_user", "delete_user", "update_user")
// targetUserID: the user being managed
// details: additional context
func (l *Logger) LogUserMgmt(adminID, action, targetUserID, details string) {
	l.logger.Info().
		Str("event_type", "user_management").
		Str("admin_id", adminID).
		Str("action", action).
		Str("target_user_id", targetUserID).
		Str("details", details).
		Msg("User management event")
}

// LogRoleBinding logs a role binding event.
// adminID: the admin performing the action
// action: action performed (e.g., "add_binding", "remove_binding")
// userID: the user whose bindings are being modified
// role: role name
// bucket: bucket name (may be empty for global roles)
// details: additional context
func (l *Logger) LogRoleBinding(adminID, action, userID, role, bucket, details string) {
	event := l.logger.Info().
		Str("event_type", "role_binding").
		Str("admin_id", adminID).
		Str("action", action).
		Str("user_id", userID).
		Str("role", role)

	if bucket != "" {
		event = event.Str("bucket", bucket)
	}
	if details != "" {
		event = event.Str("details", details)
	}

	event.Msg("Role binding event")
}
