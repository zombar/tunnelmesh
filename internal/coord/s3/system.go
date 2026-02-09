// Package s3 provides S3-compatible storage for the coordinator.
package s3

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
)

// SystemBucket is an alias for auth.SystemBucket for convenience.
const SystemBucket = auth.SystemBucket

// SystemStore provides high-level access to the _tunnelmesh system bucket.
// This is used by the coordinator to store internal state like users, roles,
// bindings, stats history, and wireguard configs.
type SystemStore struct {
	store *Store
	owner string // Service user ID that owns the system bucket
}

// NewSystemStore creates a new system store.
// It creates the _tunnelmesh bucket if it doesn't exist.
func NewSystemStore(store *Store, serviceUserID string) (*SystemStore, error) {
	ss := &SystemStore{
		store: store,
		owner: serviceUserID,
	}

	// Create system bucket if it doesn't exist
	if _, err := store.HeadBucket(SystemBucket); errors.Is(err, ErrBucketNotFound) {
		if err := store.CreateBucket(SystemBucket, serviceUserID); err != nil {
			return nil, fmt.Errorf("create system bucket: %w", err)
		}
	}

	return ss, nil
}

// Auth paths
const (
	UsersPath         = "auth/users.json"
	RolesPath         = "auth/roles.json"
	BindingsPath      = "auth/bindings.json"
	GroupsPath        = "auth/groups.json"
	GroupBindingsPath = "auth/group_bindings.json"
	FileSharesPath    = "auth/file_shares.json"
	PanelsPath        = "auth/panels.json"
)

// Stats paths
const (
	StatsHistoryPath = "stats/history.json"
)

// WireGuard paths
const (
	WireGuardClientsPath = "wireguard/clients.json"
	WGConcentratorPath   = "wireguard/concentrator.json"
)

// DNS paths
const (
	DNSCachePath = "dns/cache.json"
	DNSAliasPath = "dns/aliases.json"
)

// --- Users ---

// SaveUsers saves the user list to S3 with checksum validation.
func (ss *SystemStore) SaveUsers(users []*auth.User) error {
	return ss.saveJSONWithChecksum(UsersPath, users)
}

// LoadUsers loads the user list from S3 with automatic rollback on corruption.
func (ss *SystemStore) LoadUsers() ([]*auth.User, error) {
	var users []*auth.User
	if err := ss.loadJSONWithChecksum(UsersPath, &users, 3); err != nil {
		return nil, err
	}
	return users, nil
}

// --- Roles ---

// SaveRoles saves custom roles to S3 with checksum validation.
func (ss *SystemStore) SaveRoles(roles []auth.Role) error {
	return ss.saveJSONWithChecksum(RolesPath, roles)
}

// LoadRoles loads custom roles from S3 with automatic rollback on corruption.
func (ss *SystemStore) LoadRoles() ([]auth.Role, error) {
	var roles []auth.Role
	if err := ss.loadJSONWithChecksum(RolesPath, &roles, 3); err != nil {
		return nil, err
	}
	return roles, nil
}

// --- Role Bindings ---

// SaveBindings saves role bindings to S3 with checksum validation.
func (ss *SystemStore) SaveBindings(bindings []*auth.RoleBinding) error {
	return ss.saveJSONWithChecksum(BindingsPath, bindings)
}

// LoadBindings loads role bindings from S3 with automatic rollback on corruption.
func (ss *SystemStore) LoadBindings() ([]*auth.RoleBinding, error) {
	var bindings []*auth.RoleBinding
	if err := ss.loadJSONWithChecksum(BindingsPath, &bindings, 3); err != nil {
		return nil, err
	}
	return bindings, nil
}

// --- Groups ---

// SaveGroups saves groups to S3 with checksum validation.
func (ss *SystemStore) SaveGroups(groups []*auth.Group) error {
	return ss.saveJSONWithChecksum(GroupsPath, groups)
}

// LoadGroups loads groups from S3 with automatic rollback on corruption.
func (ss *SystemStore) LoadGroups() ([]*auth.Group, error) {
	var groups []*auth.Group
	if err := ss.loadJSONWithChecksum(GroupsPath, &groups, 3); err != nil {
		return nil, err
	}
	return groups, nil
}

// --- Group Bindings ---

// SaveGroupBindings saves group bindings to S3 with checksum validation.
func (ss *SystemStore) SaveGroupBindings(bindings []*auth.GroupBinding) error {
	return ss.saveJSONWithChecksum(GroupBindingsPath, bindings)
}

// LoadGroupBindings loads group bindings from S3 with automatic rollback on corruption.
func (ss *SystemStore) LoadGroupBindings() ([]*auth.GroupBinding, error) {
	var bindings []*auth.GroupBinding
	if err := ss.loadJSONWithChecksum(GroupBindingsPath, &bindings, 3); err != nil {
		return nil, err
	}
	return bindings, nil
}

// --- External Panels ---

// SavePanels saves external panel definitions to S3 with checksum validation.
// Only external (non-builtin) panels are persisted.
func (ss *SystemStore) SavePanels(panels []*auth.PanelDefinition) error {
	return ss.saveJSONWithChecksum(PanelsPath, panels)
}

// LoadPanels loads external panel definitions from S3 with automatic rollback on corruption.
func (ss *SystemStore) LoadPanels() ([]*auth.PanelDefinition, error) {
	var panels []*auth.PanelDefinition
	if err := ss.loadJSONWithChecksum(PanelsPath, &panels, 3); err != nil {
		return nil, err
	}
	return panels, nil
}

// --- File Shares ---

// FileShare represents a file sharing configuration backed by an S3 bucket.
type FileShare struct {
	Name        string    `json:"name"`                  // Share name (bucket will be "fs+{name}")
	Description string    `json:"description"`           // Human-readable description
	Owner       string    `json:"owner"`                 // UserID of creator
	CreatedAt   time.Time `json:"created_at"`            //
	ExpiresAt   time.Time `json:"expires_at,omitempty"`  // When the share expires (0 = never)
	QuotaBytes  int64     `json:"quota_bytes,omitempty"` // Per-share quota in bytes (0 = unlimited within global quota)
	GuestRead   bool      `json:"guest_read"`            // Allow all mesh users to read (default: true)
}

// IsExpired returns true if the share has expired.
func (s *FileShare) IsExpired() bool {
	if s.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(s.ExpiresAt)
}

// FileShareBucketPrefix is the prefix for file share bucket names.
const FileShareBucketPrefix = "fs+"

// SaveFileShares saves file shares to S3 with checksum validation.
func (ss *SystemStore) SaveFileShares(shares []*FileShare) error {
	return ss.saveJSONWithChecksum(FileSharesPath, shares)
}

// LoadFileShares loads file shares from S3 with automatic rollback on corruption.
func (ss *SystemStore) LoadFileShares() ([]*FileShare, error) {
	var shares []*FileShare
	if err := ss.loadJSONWithChecksum(FileSharesPath, &shares, 3); err != nil {
		return nil, err
	}
	return shares, nil
}

// --- Stats History ---

// SaveStatsHistory saves stats history to S3 with checksum validation.
func (ss *SystemStore) SaveStatsHistory(data interface{}) error {
	return ss.saveJSONWithChecksum(StatsHistoryPath, data)
}

// LoadStatsHistory loads stats history from S3 with automatic rollback on corruption.
func (ss *SystemStore) LoadStatsHistory(target interface{}) error {
	return ss.loadJSONWithChecksum(StatsHistoryPath, target, 3)
}

// --- WireGuard Clients ---

// SaveWireGuardClients saves WireGuard client configs to S3 with checksum validation.
func (ss *SystemStore) SaveWireGuardClients(data interface{}) error {
	return ss.saveJSONWithChecksum(WireGuardClientsPath, data)
}

// LoadWireGuardClients loads WireGuard client configs from S3 with automatic rollback on corruption.
func (ss *SystemStore) LoadWireGuardClients(target interface{}) error {
	return ss.loadJSONWithChecksum(WireGuardClientsPath, target, 3)
}

// --- WireGuard Concentrator ---

// WGConcentratorConfig stores the WireGuard concentrator assignment.
type WGConcentratorConfig struct {
	PeerName string    `json:"peer_name"`
	SetAt    time.Time `json:"set_at"`
}

// SaveWGConcentrator saves the WireGuard concentrator assignment to S3.
func (ss *SystemStore) SaveWGConcentrator(peerName string) error {
	config := WGConcentratorConfig{
		PeerName: peerName,
		SetAt:    time.Now(),
	}
	return ss.saveJSONWithChecksum(WGConcentratorPath, config)
}

// LoadWGConcentrator loads the WireGuard concentrator assignment from S3.
func (ss *SystemStore) LoadWGConcentrator() (string, error) {
	var config WGConcentratorConfig
	if err := ss.loadJSONWithChecksum(WGConcentratorPath, &config, 3); err != nil {
		return "", err
	}
	return config.PeerName, nil
}

// ClearWGConcentrator removes the WireGuard concentrator assignment from S3.
func (ss *SystemStore) ClearWGConcentrator() error {
	return ss.Delete(WGConcentratorPath)
}

// --- DNS Cache ---

// DNSCacheEntry represents a DNS cache entry with timestamp.
type DNSCacheEntry struct {
	Hostname  string    `json:"hostname"`
	MeshIP    string    `json:"mesh_ip"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SaveDNSCache saves the DNS cache to S3.
func (ss *SystemStore) SaveDNSCache(cache map[string]string) error {
	entries := make([]DNSCacheEntry, 0, len(cache))
	now := time.Now()
	for hostname, meshIP := range cache {
		entries = append(entries, DNSCacheEntry{
			Hostname:  hostname,
			MeshIP:    meshIP,
			UpdatedAt: now,
		})
	}
	return ss.saveJSONWithChecksum(DNSCachePath, entries)
}

// LoadDNSCache loads the DNS cache from S3 with TTL validation.
// Entries older than 7 days are filtered out.
func (ss *SystemStore) LoadDNSCache() (map[string]string, error) {
	var entries []DNSCacheEntry
	if err := ss.loadJSONWithChecksum(DNSCachePath, &entries, 3); err != nil {
		return nil, err
	}

	cache := make(map[string]string, len(entries))
	cutoff := time.Now().Add(-7 * 24 * time.Hour) // 7-day TTL

	for _, entry := range entries {
		// Skip stale entries
		if entry.UpdatedAt.Before(cutoff) {
			log.Debug().Str("hostname", entry.Hostname).
				Time("updated_at", entry.UpdatedAt).
				Msg("skipping stale DNS cache entry")
			continue
		}
		cache[entry.Hostname] = entry.MeshIP
	}

	return cache, nil
}

// --- DNS Aliases ---

// DNSAliasEntry represents a peer's DNS aliases.
type DNSAliasEntry struct {
	PeerName string   `json:"peer_name"`
	Aliases  []string `json:"aliases"`
}

// SaveDNSAliases saves peer DNS aliases to S3.
// aliasOwner maps alias -> peer name
// peerAliases maps peer name -> aliases list
func (ss *SystemStore) SaveDNSAliases(aliasOwner map[string]string, peerAliases map[string][]string) error {
	// Store as peer-centric data (peer -> aliases list)
	entries := make([]DNSAliasEntry, 0, len(peerAliases))
	for peerName, aliases := range peerAliases {
		if len(aliases) > 0 {
			entries = append(entries, DNSAliasEntry{
				PeerName: peerName,
				Aliases:  aliases,
			})
		}
	}
	return ss.saveJSONWithChecksum(DNSAliasPath, entries)
}

// LoadDNSAliases loads peer DNS aliases from S3.
// Returns both the peer->aliases map and the alias->peer reverse map.
func (ss *SystemStore) LoadDNSAliases() (peerAliases map[string][]string, aliasOwner map[string]string, err error) {
	var entries []DNSAliasEntry
	if err := ss.loadJSONWithChecksum(DNSAliasPath, &entries, 3); err != nil {
		return nil, nil, err
	}

	peerAliases = make(map[string][]string)
	aliasOwner = make(map[string]string)

	for _, entry := range entries {
		peerAliases[entry.PeerName] = entry.Aliases
		for _, alias := range entry.Aliases {
			aliasOwner[alias] = entry.PeerName
		}
	}

	return peerAliases, aliasOwner, nil
}

// --- Generic JSON helpers ---

// saveJSON saves a value as JSON to the system bucket.
func (ss *SystemStore) saveJSON(key string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", key, err)
	}

	_, err = ss.store.PutObject(SystemBucket, key, bytes.NewReader(data), int64(len(data)), "application/json", nil)
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}

	return nil
}

// loadJSON loads a JSON value from the system bucket.
// Returns nil if the object doesn't exist.
func (ss *SystemStore) loadJSON(key string, target interface{}) error {
	reader, _, err := ss.store.GetObject(SystemBucket, key)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return nil // Not found is OK - return nil with empty target
		}
		return fmt.Errorf("get %s: %w", key, err)
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read %s: %w", key, err)
	}

	if len(data) == 0 {
		return nil // Empty file
	}

	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("unmarshal %s: %w", key, err)
	}

	return nil
}

// --- Checksum-validated JSON helpers ---

// MetadataWrapper adds checksum validation to system metadata.
// This protects against corruption and enables automatic rollback to previous versions.
type MetadataWrapper struct {
	Version  int    `json:"version"`  // Schema version (for future migrations)
	Checksum string `json:"checksum"` // SHA-256 of decoded Data field
	Data     string `json:"data"`     // Base64-encoded payload
}

// saveJSONWithChecksum saves JSON with a checksum wrapper for corruption detection.
func (ss *SystemStore) saveJSONWithChecksum(key string, v interface{}) error {
	// Marshal payload
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", key, err)
	}

	// Compute checksum
	hash := sha256.Sum256(data)
	checksum := hex.EncodeToString(hash[:])

	// Wrap with metadata (base64-encode data to avoid JSON formatting issues)
	wrapper := MetadataWrapper{
		Version:  1,
		Checksum: checksum,
		Data:     base64.StdEncoding.EncodeToString(data),
	}

	// Save with indentation for readability
	wrappedData, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal wrapper: %w", err)
	}

	_, err = ss.store.PutObject(SystemBucket, key, bytes.NewReader(wrappedData),
		int64(len(wrappedData)), "application/json", nil)
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}

	return nil
}

// loadJSONWithChecksum loads and validates JSON with automatic rollback on corruption.
// If the current version is corrupted, it tries up to maxRetries previous versions.
// Returns nil if the object doesn't exist (same as loadJSON for backward compatibility).
func (ss *SystemStore) loadJSONWithChecksum(key string, target interface{}, maxRetries int) error {
	// Try loading current version first
	if err := ss.tryLoadVersion(key, "", target); err == nil {
		return nil // Success
	} else if errors.Is(err, ErrObjectNotFound) {
		return nil // Not found is OK - return nil with empty target
	} else {
		log.Warn().Err(err).Str("key", key).Msg("failed to load current version")
	}

	// Current version failed - try previous versions
	versions, err := ss.store.ListVersions(SystemBucket, key)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) || errors.Is(err, ErrBucketNotFound) {
			return nil // No versions exist - return nil
		}
		return fmt.Errorf("list versions for %s: %w", key, err)
	}

	// Try up to maxRetries previous versions
	attempts := 0
	for _, ver := range versions {
		if attempts >= maxRetries {
			break
		}

		log.Info().Str("key", key).Str("version", ver.VersionID).Msg("attempting rollback")
		if err := ss.tryLoadVersion(key, ver.VersionID, target); err == nil {
			// Success - restore this version as current
			log.Warn().Str("key", key).Str("version", ver.VersionID).
				Msg("recovered from corrupted data using previous version")
			if _, err := ss.store.RestoreVersion(SystemBucket, key, ver.VersionID); err != nil {
				log.Error().Err(err).Msg("failed to restore recovered version")
			}
			return nil
		}

		attempts++
	}

	return fmt.Errorf("exhausted %d versions, all corrupted", attempts)
}

// tryLoadVersion attempts to load and validate a specific version.
// It supports both checksum-wrapped format (new) and direct JSON (legacy).
func (ss *SystemStore) tryLoadVersion(key, versionID string, target interface{}) error {
	// Get object (specific version if provided)
	var reader io.ReadCloser
	var err error

	if versionID == "" {
		reader, _, err = ss.store.GetObject(SystemBucket, key)
	} else {
		reader, _, err = ss.store.GetObjectVersion(SystemBucket, key, versionID)
	}

	if err != nil {
		return fmt.Errorf("get object: %w", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}

	if len(data) == 0 {
		return fmt.Errorf("empty data")
	}

	// Try unwrapping checksum wrapper
	var wrapper MetadataWrapper
	if err := json.Unmarshal(data, &wrapper); err == nil && wrapper.Checksum != "" {
		// Has checksum - decode and validate
		decodedData, err := base64.StdEncoding.DecodeString(wrapper.Data)
		if err != nil {
			return fmt.Errorf("decode base64 data: %w", err)
		}

		hash := sha256.Sum256(decodedData)
		expectedChecksum := hex.EncodeToString(hash[:])

		if wrapper.Checksum != expectedChecksum {
			return fmt.Errorf("checksum mismatch: expected %s, got %s",
				expectedChecksum, wrapper.Checksum)
		}

		// Checksum valid - unmarshal payload
		if err := json.Unmarshal(decodedData, target); err != nil {
			return fmt.Errorf("unmarshal payload: %w", err)
		}

		return nil
	}

	// No wrapper (legacy format or first write) - try direct unmarshal
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("unmarshal direct: %w", err)
	}

	return nil
}

// --- Migration helpers ---

// MigrateFromFile migrates data from a local file to S3.
// If the S3 key already exists, no migration is performed.
// If the local file exists, it's read and saved to the S3 path.
// Returns true if migration was performed.
func (ss *SystemStore) MigrateFromFile(localPath, s3Key string) (bool, error) {
	// Check if already exists in S3
	_, _, err := ss.store.GetObject(SystemBucket, s3Key)
	if err == nil {
		return false, nil // Already migrated
	}
	if !errors.Is(err, ErrObjectNotFound) {
		return false, err
	}

	// Read from local file
	data, err := os.ReadFile(localPath)
	if os.IsNotExist(err) {
		return false, nil // No local file to migrate
	}
	if err != nil {
		return false, fmt.Errorf("read local file: %w", err)
	}

	// Save to S3
	if _, err := ss.store.PutObject(SystemBucket, s3Key, bytes.NewReader(data), int64(len(data)), "application/json", nil); err != nil {
		return false, fmt.Errorf("save to S3: %w", err)
	}

	return true, nil
}

// Exists checks if an object exists in the system bucket.
func (ss *SystemStore) Exists(key string) bool {
	_, err := ss.store.HeadObject(SystemBucket, key)
	return err == nil
}

// Delete permanently removes an object from the system bucket.
// Unlike user data, system configuration is not tombstoned.
func (ss *SystemStore) Delete(key string) error {
	return ss.store.PurgeObject(SystemBucket, key)
}

// Raw returns the underlying store for advanced operations.
func (ss *SystemStore) Raw() *Store {
	return ss.store
}
