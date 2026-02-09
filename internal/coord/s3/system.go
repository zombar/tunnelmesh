// Package s3 provides S3-compatible storage for the coordinator.
package s3

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

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
	if _, err := store.HeadBucket(SystemBucket); err == ErrBucketNotFound {
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
)

// --- Users ---

// SaveUsers saves the user list to S3.
func (ss *SystemStore) SaveUsers(users []*auth.User) error {
	return ss.saveJSON(UsersPath, users)
}

// LoadUsers loads the user list from S3.
func (ss *SystemStore) LoadUsers() ([]*auth.User, error) {
	var users []*auth.User
	if err := ss.loadJSON(UsersPath, &users); err != nil {
		return nil, err
	}
	return users, nil
}

// --- Roles ---

// SaveRoles saves custom roles to S3.
func (ss *SystemStore) SaveRoles(roles []auth.Role) error {
	return ss.saveJSON(RolesPath, roles)
}

// LoadRoles loads custom roles from S3.
func (ss *SystemStore) LoadRoles() ([]auth.Role, error) {
	var roles []auth.Role
	if err := ss.loadJSON(RolesPath, &roles); err != nil {
		return nil, err
	}
	return roles, nil
}

// --- Role Bindings ---

// SaveBindings saves role bindings to S3.
func (ss *SystemStore) SaveBindings(bindings []*auth.RoleBinding) error {
	return ss.saveJSON(BindingsPath, bindings)
}

// LoadBindings loads role bindings from S3.
func (ss *SystemStore) LoadBindings() ([]*auth.RoleBinding, error) {
	var bindings []*auth.RoleBinding
	if err := ss.loadJSON(BindingsPath, &bindings); err != nil {
		return nil, err
	}
	return bindings, nil
}

// --- Groups ---

// SaveGroups saves groups to S3.
func (ss *SystemStore) SaveGroups(groups []*auth.Group) error {
	return ss.saveJSON(GroupsPath, groups)
}

// LoadGroups loads groups from S3.
func (ss *SystemStore) LoadGroups() ([]*auth.Group, error) {
	var groups []*auth.Group
	if err := ss.loadJSON(GroupsPath, &groups); err != nil {
		return nil, err
	}
	return groups, nil
}

// --- Group Bindings ---

// SaveGroupBindings saves group bindings to S3.
func (ss *SystemStore) SaveGroupBindings(bindings []*auth.GroupBinding) error {
	return ss.saveJSON(GroupBindingsPath, bindings)
}

// LoadGroupBindings loads group bindings from S3.
func (ss *SystemStore) LoadGroupBindings() ([]*auth.GroupBinding, error) {
	var bindings []*auth.GroupBinding
	if err := ss.loadJSON(GroupBindingsPath, &bindings); err != nil {
		return nil, err
	}
	return bindings, nil
}

// --- External Panels ---

// SavePanels saves external panel definitions to S3.
// Only external (non-builtin) panels are persisted.
func (ss *SystemStore) SavePanels(panels []*auth.PanelDefinition) error {
	return ss.saveJSON(PanelsPath, panels)
}

// LoadPanels loads external panel definitions from S3.
func (ss *SystemStore) LoadPanels() ([]*auth.PanelDefinition, error) {
	var panels []*auth.PanelDefinition
	if err := ss.loadJSON(PanelsPath, &panels); err != nil {
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

// SaveFileShares saves file shares to S3.
func (ss *SystemStore) SaveFileShares(shares []*FileShare) error {
	return ss.saveJSON(FileSharesPath, shares)
}

// LoadFileShares loads file shares from S3.
func (ss *SystemStore) LoadFileShares() ([]*FileShare, error) {
	var shares []*FileShare
	if err := ss.loadJSON(FileSharesPath, &shares); err != nil {
		return nil, err
	}
	return shares, nil
}

// --- Stats History ---

// SaveStatsHistory saves stats history to S3.
func (ss *SystemStore) SaveStatsHistory(data interface{}) error {
	return ss.saveJSON(StatsHistoryPath, data)
}

// LoadStatsHistory loads stats history from S3.
func (ss *SystemStore) LoadStatsHistory(target interface{}) error {
	return ss.loadJSON(StatsHistoryPath, target)
}

// --- WireGuard Clients ---

// SaveWireGuardClients saves WireGuard client configs to S3.
func (ss *SystemStore) SaveWireGuardClients(data interface{}) error {
	return ss.saveJSON(WireGuardClientsPath, data)
}

// LoadWireGuardClients loads WireGuard client configs from S3.
func (ss *SystemStore) LoadWireGuardClients(target interface{}) error {
	return ss.loadJSON(WireGuardClientsPath, target)
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
		if err == ErrObjectNotFound {
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
	if err != ErrObjectNotFound {
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
