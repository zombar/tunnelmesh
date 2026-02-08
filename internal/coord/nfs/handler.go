package nfs

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"sync"

	"github.com/go-git/go-billy/v5"
	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
	nfs "github.com/willscott/go-nfs"
	"github.com/willscott/go-nfs/helpers"
	"golang.org/x/crypto/bcrypt"
)

// Handler implements nfs.Handler with S3 backend and authentication.
type Handler struct {
	store        *s3.Store
	shares       *s3.FileShareManager
	authorizer   *auth.Authorizer
	passwords    *PasswordStore
	cachingLimit int

	// Internal handler wrapping
	inner nfs.Handler

	// Connection -> user mapping
	connUsers map[net.Conn]string
	connMu    sync.RWMutex
}

// PasswordStore manages user passwords for NFS authentication.
type PasswordStore struct {
	hashes map[string][]byte // userID -> bcrypt hash
	mu     sync.RWMutex
}

// NewPasswordStore creates a new password store.
func NewPasswordStore() *PasswordStore {
	return &PasswordStore{
		hashes: make(map[string][]byte),
	}
}

// SetPassword sets a user's password.
func (ps *PasswordStore) SetPassword(userID, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.hashes[userID] = hash
	return nil
}

// VerifyPassword checks if password matches for user.
func (ps *PasswordStore) VerifyPassword(userID, password string) bool {
	ps.mu.RLock()
	hash, ok := ps.hashes[userID]
	ps.mu.RUnlock()
	if !ok {
		return false
	}
	return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
}

// HasPassword checks if user has a password set.
func (ps *PasswordStore) HasPassword(userID string) bool {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	_, ok := ps.hashes[userID]
	return ok
}

// NewHandler creates a new NFS handler.
func NewHandler(store *s3.Store, shares *s3.FileShareManager, authorizer *auth.Authorizer, passwords *PasswordStore) *Handler {
	h := &Handler{
		store:        store,
		shares:       shares,
		authorizer:   authorizer,
		passwords:    passwords,
		cachingLimit: 1024, // Handle cache limit
		connUsers:    make(map[net.Conn]string),
	}
	return h
}

// Mount handles NFS mount requests.
// It authenticates the user and returns the appropriate filesystem.
func (h *Handler) Mount(ctx context.Context, conn net.Conn, req nfs.MountRequest) (nfs.MountStatus, billy.Filesystem, []nfs.AuthFlavor) {
	// Extract share name from mount path
	path := string(req.Dirpath)
	path = strings.TrimPrefix(path, "/")
	parts := strings.SplitN(path, "/", 2)
	shareName := parts[0]
	prefix := ""
	if len(parts) > 1 {
		prefix = parts[1]
	}

	log.Debug().
		Str("path", path).
		Str("share", shareName).
		Str("prefix", prefix).
		Msg("NFS mount request")

	// Authenticate user
	userID, err := h.authenticateConnection(conn)
	if err != nil {
		log.Warn().Err(err).Msg("NFS authentication failed")
		return nfs.MountStatusErrAcces, nil, nil
	}

	// Store user for this connection
	h.connMu.Lock()
	h.connUsers[conn] = userID
	h.connMu.Unlock()

	// Lookup share
	share := h.shares.Get(shareName)
	if share == nil {
		log.Warn().Str("share", shareName).Msg("NFS share not found")
		return nfs.MountStatusErrNoEnt, nil, nil
	}

	// Check authorization
	bucket := h.shares.BucketName(shareName)
	fullPrefix := prefix
	if !h.authorizer.Authorize(userID, "get", "objects", bucket, fullPrefix) {
		log.Warn().
			Str("user", userID).
			Str("bucket", bucket).
			Str("prefix", fullPrefix).
			Msg("NFS access denied")
		return nfs.MountStatusErrAcces, nil, nil
	}

	// Check if user has write access
	readOnly := !h.authorizer.Authorize(userID, "put", "objects", bucket, fullPrefix)

	log.Info().
		Str("user", userID).
		Str("share", shareName).
		Str("bucket", bucket).
		Bool("readonly", readOnly).
		Msg("NFS mount successful")

	fs := NewS3Filesystem(h.store, bucket, prefix, readOnly)
	return nfs.MountStatusOk, fs, []nfs.AuthFlavor{nfs.AuthFlavorNull}
}

// authenticateConnection extracts user identity from connection.
// Tries TLS client cert first, then falls back to AUTH_SYS (unix uid).
func (h *Handler) authenticateConnection(conn net.Conn) (string, error) {
	// Try TLS client certificate
	if tlsConn, ok := conn.(*tls.Conn); ok {
		state := tlsConn.ConnectionState()
		if len(state.PeerCertificates) > 0 {
			cert := state.PeerCertificates[0]
			// Extract user ID from CN (format: "username.tunnelmesh")
			cn := cert.Subject.CommonName
			if strings.HasSuffix(cn, ".tunnelmesh") {
				userID := strings.TrimSuffix(cn, ".tunnelmesh")
				log.Debug().Str("user", userID).Msg("NFS TLS auth successful")
				return userID, nil
			}
			// Use CN as user ID directly
			log.Debug().Str("user", cn).Msg("NFS TLS auth successful (raw CN)")
			return cn, nil
		}
	}

	// No TLS auth - require password auth configured for this user
	// For NFS, we'd typically use AUTH_SYS (uid/gid) or AUTH_GSS (Kerberos)
	// Since AUTH_SYS doesn't provide passwords, we'll need a mapping
	// For now, we'll allow anonymous access with a default user
	// In production, you'd want to require TLS client certs

	return "", errors.New("authentication required: TLS client certificate or password")
}

// Change returns a billy.Change for the filesystem.
func (h *Handler) Change(fs billy.Filesystem) billy.Change {
	return nil // Read-only changes not supported via S3
}

// FSStat fills in filesystem statistics.
func (h *Handler) FSStat(ctx context.Context, fs billy.Filesystem, stat *nfs.FSStat) error {
	// Return some reasonable defaults
	stat.TotalSize = 1 << 40      // 1 TB
	stat.FreeSize = 1 << 40       // 1 TB free
	stat.AvailableSize = 1 << 40  // 1 TB available
	stat.TotalFiles = 1 << 20     // 1M files
	stat.FreeFiles = 1 << 20      // 1M free
	stat.AvailableFiles = 1 << 20 // 1M available
	stat.CacheHint = 0            // No caching hint
	return nil
}

// ToHandle converts a file path to a handle.
func (h *Handler) ToHandle(fs billy.Filesystem, path []string) []byte {
	return h.getInner(fs).ToHandle(fs, path)
}

// FromHandle converts a handle back to a filesystem and path.
func (h *Handler) FromHandle(fh []byte) (billy.Filesystem, []string, error) {
	return h.inner.FromHandle(fh)
}

// InvalidateHandle invalidates a handle.
func (h *Handler) InvalidateHandle(fs billy.Filesystem, fh []byte) error {
	return h.getInner(fs).InvalidateHandle(fs, fh)
}

// HandleLimit returns the maximum number of handles.
func (h *Handler) HandleLimit() int {
	return h.cachingLimit
}

// getInner returns the inner caching handler, initializing if needed.
func (h *Handler) getInner(fs billy.Filesystem) nfs.Handler {
	if h.inner == nil {
		h.inner = helpers.NewCachingHandler(helpers.NewNullAuthHandler(fs), h.cachingLimit)
	}
	return h.inner
}
