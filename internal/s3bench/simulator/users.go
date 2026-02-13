package simulator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
)

// UserSession represents an S3 user session with credentials.
type UserSession struct {
	Character story.Character // The character this session represents
	UserID    string          // User ID in the system
	AccessKey string          // S3 access key
	SecretKey string          // S3 secret key
	Endpoint  string          // S3 endpoint URL
}

// UserManager manages user sessions and file share setup for a story.
type UserManager struct {
	store         *s3.Store
	credentials   *s3.CredentialStore
	authorizer    *auth.Authorizer
	shareManager  *s3.FileShareManager
	story         story.Story
	sessions      map[string]*UserSession // Map character ID to session
	defaultQuota  int64                   // Default file share quota in bytes
	quotaOverride int64                   // User-specified quota override (0 = use defaults)
}

// NewUserManager creates a new user manager.
func NewUserManager(store *s3.Store, credentials *s3.CredentialStore, authorizer *auth.Authorizer, shareManager *s3.FileShareManager, st story.Story) *UserManager {
	return &UserManager{
		store:        store,
		credentials:  credentials,
		authorizer:   authorizer,
		shareManager: shareManager,
		story:        st,
		sessions:     make(map[string]*UserSession),
		defaultQuota: 500 * 1024 * 1024, // 500MB default
	}
}

// SetQuotaOverride sets a user-specified quota override for all file shares.
// If quotaMB is 0, story defaults will be used.
func (um *UserManager) SetQuotaOverride(quotaMB int64) {
	if quotaMB > 0 {
		um.quotaOverride = quotaMB * 1024 * 1024 // Convert MB to bytes
	}
}

// Setup creates users, credentials, and file shares based on the story definition.
func (um *UserManager) Setup(ctx context.Context, endpoint string) error {
	// Create user sessions for all characters
	for _, char := range um.story.Characters() {
		if err := um.createUserSession(char, endpoint); err != nil {
			return fmt.Errorf("create session for %s: %w", char.ID, err)
		}
	}

	// Create file shares for all departments
	for _, dept := range um.story.Departments() {
		if err := um.createFileShare(ctx, dept); err != nil {
			return fmt.Errorf("create file share %s: %w", dept.ID, err)
		}
	}

	// Grant department members access to their file shares
	for _, dept := range um.story.Departments() {
		if err := um.grantDepartmentAccess(dept); err != nil {
			return fmt.Errorf("grant access to %s: %w", dept.ID, err)
		}
	}

	return nil
}

// createUserSession creates a user session with S3 credentials.
func (um *UserManager) createUserSession(char story.Character, endpoint string) error {
	// Generate a deterministic public key from character ID for credentials
	// In a real scenario, this would use actual public keys
	publicKey := generateDeterministicPublicKey(char.ID)

	// Register user with credential store
	accessKey, secretKey, err := um.credentials.RegisterUser(char.ID, publicKey)
	if err != nil {
		return fmt.Errorf("register credentials: %w", err)
	}

	// Create session
	session := &UserSession{
		Character: char,
		UserID:    char.ID,
		AccessKey: accessKey,
		SecretKey: secretKey,
		Endpoint:  endpoint,
	}
	um.sessions[char.ID] = session

	// Add user to 'everyone' group for basic access (if groups are enabled)
	if um.authorizer.Groups != nil {
		_ = um.authorizer.Groups.AddMember(auth.GroupEveryone, char.ID)
	}

	return nil
}

// createFileShare creates a file share for a department.
func (um *UserManager) createFileShare(ctx context.Context, dept story.Department) error {
	// Find the department owner (first member, or create admin if no members)
	ownerID := "admin"
	if len(dept.Members) > 0 {
		ownerID = dept.Members[0]
	}

	// Determine quota
	quotaBytes := um.defaultQuota
	if um.quotaOverride > 0 {
		quotaBytes = um.quotaOverride
	} else if dept.QuotaMB > 0 {
		quotaBytes = dept.QuotaMB * 1024 * 1024
	}

	// Create file share options
	opts := &s3.FileShareOptions{
		GuestRead:    dept.GuestRead,
		GuestReadSet: true,
	}

	// Create the file share
	_, err := um.shareManager.Create(context.Background(), dept.FileShare, dept.Name, ownerID, quotaBytes, opts)
	if err != nil {
		return fmt.Errorf("create file share: %w", err)
	}

	return nil
}

// grantDepartmentAccess grants all department members access to the department file share.
func (um *UserManager) grantDepartmentAccess(dept story.Department) error {
	bucketName := um.shareManager.BucketName(dept.FileShare)

	for _, memberID := range dept.Members {
		// Find character to determine clearance level
		var char *story.Character
		for _, c := range um.story.Characters() {
			if c.ID == memberID {
				char = &c
				break
			}
		}

		if char == nil {
			continue
		}

		// Determine role based on clearance
		// Higher clearance = more permissions
		var roleName string
		switch {
		case char.Clearance >= 4:
			// High clearance: admin access to department shares
			roleName = auth.RoleBucketAdmin
		case char.Clearance >= 2:
			// Medium clearance: write access
			roleName = auth.RoleBucketWrite
		default:
			// Low clearance: read-only
			roleName = auth.RoleBucketRead
		}

		// Create role binding
		binding := auth.NewRoleBinding(memberID, roleName, bucketName)
		um.authorizer.Bindings.Add(binding)
	}

	return nil
}

// GetSession returns the user session for a character.
func (um *UserManager) GetSession(characterID string) (*UserSession, error) {
	session, ok := um.sessions[characterID]
	if !ok {
		return nil, fmt.Errorf("no session for character %s", characterID)
	}
	return session, nil
}

// GetAllSessions returns all user sessions.
func (um *UserManager) GetAllSessions() []*UserSession {
	sessions := make([]*UserSession, 0, len(um.sessions))
	for _, session := range um.sessions {
		sessions = append(sessions, session)
	}
	return sessions
}

// GrantPermission grants a character access to a file share.
// This simulates a permission request approval.
func (um *UserManager) GrantPermission(characterID, fileShareName, roleName string) error {
	// Check character exists
	if _, ok := um.sessions[characterID]; !ok {
		return fmt.Errorf("character %s not found", characterID)
	}

	// Check file share exists
	share := um.shareManager.Get(fileShareName)
	if share == nil {
		return fmt.Errorf("file share %s not found", fileShareName)
	}

	bucketName := um.shareManager.BucketName(fileShareName)

	// Create role binding
	binding := auth.NewRoleBinding(characterID, roleName, bucketName)
	um.authorizer.Bindings.Add(binding)

	return nil
}

// RevokePermission revokes a character's access to a file share.
// Removes all role bindings for the character on the specified file share.
func (um *UserManager) RevokePermission(characterID, fileShareName string) error {
	bucketName := um.shareManager.BucketName(fileShareName)

	// Find and remove all bindings for this user on this bucket
	removed := false
	for _, binding := range um.authorizer.Bindings.List() {
		if binding.PeerID == characterID && binding.BucketScope == bucketName {
			um.authorizer.Bindings.Remove(binding.Name)
			removed = true
		}
	}

	if !removed {
		return fmt.Errorf("no permission found for character %s on share %s", characterID, fileShareName)
	}

	return nil
}

// Cleanup removes all users and file shares created during setup.
func (um *UserManager) Cleanup() error {
	// Delete all file shares
	for _, dept := range um.story.Departments() {
		_ = um.shareManager.Delete(context.Background(), dept.FileShare)
	}

	// Remove all role bindings
	for _, binding := range um.authorizer.Bindings.List() {
		um.authorizer.Bindings.Remove(binding.Name)
	}

	// Remove all group bindings
	for _, binding := range um.authorizer.GroupBindings.List() {
		um.authorizer.GroupBindings.Remove(binding.Name)
	}

	// Clear sessions
	um.sessions = make(map[string]*UserSession)

	return nil
}

// generateDeterministicPublicKey generates a deterministic public key from a character ID.
// This is for testing purposes only - real systems would use actual public keys.
func generateDeterministicPublicKey(characterID string) string {
	hash := sha256.Sum256([]byte("tunnelmesh-s3bench-" + characterID))
	return hex.EncodeToString(hash[:])
}
