package simulator

import (
	"context"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story/scenarios"
)

func TestUserManager_Setup(t *testing.T) {
	// Create test components
	store := createTestStore(t)
	credentials := s3.NewCredentialStore()
	authorizer := auth.NewAuthorizerWithGroups()
	systemStore, err := s3.NewSystemStore(store, "test")
	if err != nil {
		t.Fatalf("NewSystemStore() error = %v", err)
	}
	shareManager := s3.NewFileShareManager(store, systemStore, authorizer)

	// Use alien invasion scenario
	ai := &scenarios.AlienInvasion{}
	userMgr := NewUserManager(store, credentials, authorizer, shareManager, ai)

	ctx := context.Background()
	endpoint := "http://localhost:8080"

	// Setup users and file shares
	if err := userMgr.Setup(ctx, endpoint); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	// Verify user sessions created for all characters
	characters := ai.Characters()
	if len(userMgr.sessions) != len(characters) {
		t.Errorf("Expected %d sessions, got %d", len(characters), len(userMgr.sessions))
	}

	// Verify each character has valid credentials
	for _, char := range characters {
		session, err := userMgr.GetSession(char.ID)
		if err != nil {
			t.Errorf("GetSession(%s) error = %v", char.ID, err)
			continue
		}

		if session.AccessKey == "" {
			t.Errorf("Character %s has empty access key", char.ID)
		}

		if session.SecretKey == "" {
			t.Errorf("Character %s has empty secret key", char.ID)
		}

		if session.Endpoint != endpoint {
			t.Errorf("Character %s endpoint = %v, want %v", char.ID, session.Endpoint, endpoint)
		}

		// Verify credentials registered
		userID, ok := credentials.LookupUser(session.AccessKey)
		if !ok {
			t.Errorf("Access key %s not found in credential store", session.AccessKey)
		}
		if userID != char.ID {
			t.Errorf("Access key maps to %s, want %s", userID, char.ID)
		}

		secret, ok := credentials.GetSecret(char.ID)
		if !ok {
			t.Errorf("Secret key for %s not found", char.ID)
		}
		if secret != session.SecretKey {
			t.Errorf("Secret key mismatch for %s", char.ID)
		}
	}

	// Verify file shares created for all departments
	departments := ai.Departments()
	for _, dept := range departments {
		share := shareManager.Get(dept.FileShare)
		if share == nil {
			t.Errorf("File share %s not created", dept.FileShare)
			continue
		}

		if share.Name != dept.FileShare {
			t.Errorf("File share name = %v, want %v", share.Name, dept.FileShare)
		}

		// Verify quota
		expectedQuota := userMgr.defaultQuota
		if dept.QuotaMB > 0 {
			expectedQuota = dept.QuotaMB * 1024 * 1024
		}
		if share.QuotaBytes != expectedQuota {
			t.Errorf("File share %s quota = %d, want %d", dept.FileShare, share.QuotaBytes, expectedQuota)
		}

		// Verify guest read setting
		if share.GuestRead != dept.GuestRead {
			t.Errorf("File share %s guest read = %v, want %v", dept.FileShare, share.GuestRead, dept.GuestRead)
		}
	}

	// Verify department members have access
	for _, dept := range departments {
		bucketName := s3.FileShareBucketPrefix + dept.FileShare
		for _, memberID := range dept.Members {
			// Check for role binding
			hasBinding := false
			for _, binding := range authorizer.Bindings.List() {
				if binding.UserID == memberID && binding.BucketScope == bucketName {
					hasBinding = true
					break
				}
			}
			if !hasBinding {
				t.Errorf("Member %s has no binding for share %s", memberID, dept.FileShare)
			}
		}
	}

	t.Logf("Setup successful: %d users, %d file shares", len(userMgr.sessions), len(departments))
}

func TestUserManager_QuotaOverride(t *testing.T) {
	store := createTestStore(t)
	credentials := s3.NewCredentialStore()
	authorizer := auth.NewAuthorizerWithGroups()
	systemStore, err := s3.NewSystemStore(store, "test")
	if err != nil {
		t.Fatalf("NewSystemStore() error = %v", err)
	}
	shareManager := s3.NewFileShareManager(store, systemStore, authorizer)

	ai := &scenarios.AlienInvasion{}
	userMgr := NewUserManager(store, credentials, authorizer, shareManager, ai)

	// Set quota override
	overrideMB := int64(100)
	userMgr.SetQuotaOverride(overrideMB)

	ctx := context.Background()
	if err := userMgr.Setup(ctx, "http://localhost:8080"); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	// Verify all file shares use override quota
	expectedQuota := overrideMB * 1024 * 1024
	for _, dept := range ai.Departments() {
		share := shareManager.Get(dept.FileShare)
		if share == nil {
			t.Errorf("File share %s not created", dept.FileShare)
			continue
		}

		if share.QuotaBytes != expectedQuota {
			t.Errorf("File share %s quota = %d, want %d (override)", dept.FileShare, share.QuotaBytes, expectedQuota)
		}
	}
}

func TestUserManager_GetSession(t *testing.T) {
	store := createTestStore(t)
	credentials := s3.NewCredentialStore()
	authorizer := auth.NewAuthorizerWithGroups()
	systemStore, err := s3.NewSystemStore(store, "test")
	if err != nil {
		t.Fatalf("NewSystemStore() error = %v", err)
	}
	shareManager := s3.NewFileShareManager(store, systemStore, authorizer)

	ai := &scenarios.AlienInvasion{}
	userMgr := NewUserManager(store, credentials, authorizer, shareManager, ai)

	ctx := context.Background()
	if err := userMgr.Setup(ctx, "http://localhost:8080"); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	// Test getting existing session
	characters := ai.Characters()
	if len(characters) == 0 {
		t.Fatal("No characters in scenario")
	}

	session, err := userMgr.GetSession(characters[0].ID)
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}

	if session.Character.ID != characters[0].ID {
		t.Errorf("Session character ID = %v, want %v", session.Character.ID, characters[0].ID)
	}

	// Test getting non-existent session
	_, err = userMgr.GetSession("nonexistent")
	if err == nil {
		t.Error("GetSession() expected error for non-existent character, got nil")
	}
}

func TestUserManager_GetAllSessions(t *testing.T) {
	store := createTestStore(t)
	credentials := s3.NewCredentialStore()
	authorizer := auth.NewAuthorizerWithGroups()
	systemStore, err := s3.NewSystemStore(store, "test")
	if err != nil {
		t.Fatalf("NewSystemStore() error = %v", err)
	}
	shareManager := s3.NewFileShareManager(store, systemStore, authorizer)

	ai := &scenarios.AlienInvasion{}
	userMgr := NewUserManager(store, credentials, authorizer, shareManager, ai)

	ctx := context.Background()
	if err := userMgr.Setup(ctx, "http://localhost:8080"); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	sessions := userMgr.GetAllSessions()

	characters := ai.Characters()
	if len(sessions) != len(characters) {
		t.Errorf("GetAllSessions() returned %d sessions, want %d", len(sessions), len(characters))
	}

	// Verify all sessions are present
	sessionMap := make(map[string]*UserSession)
	for _, session := range sessions {
		sessionMap[session.Character.ID] = session
	}

	for _, char := range characters {
		if _, ok := sessionMap[char.ID]; !ok {
			t.Errorf("Session for character %s not found in GetAllSessions()", char.ID)
		}
	}
}

func TestUserManager_GrantPermission(t *testing.T) {
	store := createTestStore(t)
	credentials := s3.NewCredentialStore()
	authorizer := auth.NewAuthorizerWithGroups()
	systemStore, err := s3.NewSystemStore(store, "test")
	if err != nil {
		t.Fatalf("NewSystemStore() error = %v", err)
	}
	shareManager := s3.NewFileShareManager(store, systemStore, authorizer)

	// Create simple test story
	testStory := &testMockStory{}
	testStory.chars = []story.Character{
		{
			ID:         "alice",
			Name:       "Alice",
			Role:       "User",
			Department: "test",
			Clearance:  3,
		},
	}
	testStory.depts = []story.Department{
		{
			ID:        "test",
			Name:      "Test Department",
			FileShare: "testshare",
			Members:   []string{"alice"},
			QuotaMB:   100,
			GuestRead: false,
		},
	}

	userMgr := NewUserManager(store, credentials, authorizer, shareManager, testStory)

	ctx := context.Background()
	if err := userMgr.Setup(ctx, "http://localhost:8080"); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	// Grant alice bucket-admin permission to testshare
	if err := userMgr.GrantPermission("alice", "testshare", auth.RoleBucketAdmin); err != nil {
		t.Fatalf("GrantPermission() error = %v", err)
	}

	// Verify binding was created
	bucketName := s3.FileShareBucketPrefix + "testshare"
	found := false
	for _, binding := range authorizer.Bindings.List() {
		if binding.UserID == "alice" && binding.RoleName == auth.RoleBucketAdmin && binding.BucketScope == bucketName {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected bucket-admin binding for alice not found")
	}

	// Test granting to non-existent character
	err = userMgr.GrantPermission("nonexistent", "testshare", auth.RoleBucketRead)
	if err == nil {
		t.Error("GrantPermission() expected error for non-existent character, got nil")
	}

	// Test granting to non-existent file share
	err = userMgr.GrantPermission("alice", "nonexistent", auth.RoleBucketRead)
	if err == nil {
		t.Error("GrantPermission() expected error for non-existent share, got nil")
	}
}

func TestUserManager_RevokePermission(t *testing.T) {
	store := createTestStore(t)
	credentials := s3.NewCredentialStore()
	authorizer := auth.NewAuthorizerWithGroups()
	systemStore, err := s3.NewSystemStore(store, "test")
	if err != nil {
		t.Fatalf("NewSystemStore() error = %v", err)
	}
	shareManager := s3.NewFileShareManager(store, systemStore, authorizer)

	testStory := &testMockStory{}
	testStory.chars = []story.Character{
		{
			ID:         "alice",
			Name:       "Alice",
			Role:       "User",
			Department: "test",
			Clearance:  3,
		},
	}
	testStory.depts = []story.Department{
		{
			ID:        "test",
			Name:      "Test Department",
			FileShare: "testshare",
			Members:   []string{"alice"},
			QuotaMB:   100,
			GuestRead: false,
		},
	}

	userMgr := NewUserManager(store, credentials, authorizer, shareManager, testStory)

	ctx := context.Background()
	if err := userMgr.Setup(ctx, "http://localhost:8080"); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	// Revoke alice's permission
	if err := userMgr.RevokePermission("alice", "testshare"); err != nil {
		t.Fatalf("RevokePermission() error = %v", err)
	}

	// Verify binding was removed
	bucketName := s3.FileShareBucketPrefix + "testshare"
	for _, binding := range authorizer.Bindings.List() {
		if binding.UserID == "alice" && binding.BucketScope == bucketName {
			t.Error("Expected binding to be removed, but it still exists")
		}
	}

	// Test revoking non-existent permission
	err = userMgr.RevokePermission("alice", "testshare")
	if err == nil {
		t.Error("RevokePermission() expected error for non-existent permission, got nil")
	}
}

func TestUserManager_Cleanup(t *testing.T) {
	store := createTestStore(t)
	credentials := s3.NewCredentialStore()
	authorizer := auth.NewAuthorizerWithGroups()
	systemStore, err := s3.NewSystemStore(store, "test")
	if err != nil {
		t.Fatalf("NewSystemStore() error = %v", err)
	}
	shareManager := s3.NewFileShareManager(store, systemStore, authorizer)

	ai := &scenarios.AlienInvasion{}
	userMgr := NewUserManager(store, credentials, authorizer, shareManager, ai)

	ctx := context.Background()
	if err := userMgr.Setup(ctx, "http://localhost:8080"); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	// Verify setup worked
	if len(userMgr.sessions) == 0 {
		t.Fatal("No sessions created")
	}
	if len(shareManager.List()) == 0 {
		t.Fatal("No file shares created")
	}
	if len(authorizer.Bindings.List()) == 0 {
		t.Fatal("No bindings created")
	}

	// Cleanup
	if err := userMgr.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	// Verify cleanup
	if len(userMgr.sessions) != 0 {
		t.Errorf("Sessions not cleared: %d remaining", len(userMgr.sessions))
	}

	if len(shareManager.List()) != 0 {
		t.Errorf("File shares not deleted: %d remaining", len(shareManager.List()))
	}

	if len(authorizer.Bindings.List()) != 0 {
		t.Errorf("Bindings not removed: %d remaining", len(authorizer.Bindings.List()))
	}

	if len(authorizer.GroupBindings.List()) != 0 {
		t.Errorf("Group bindings not removed: %d remaining", len(authorizer.GroupBindings.List()))
	}
}

func TestUserManager_ClearanceBasedPermissions(t *testing.T) {
	store := createTestStore(t)
	credentials := s3.NewCredentialStore()
	authorizer := auth.NewAuthorizerWithGroups()
	systemStore, err := s3.NewSystemStore(store, "test")
	if err != nil {
		t.Fatalf("NewSystemStore() error = %v", err)
	}
	shareManager := s3.NewFileShareManager(store, systemStore, authorizer)

	// Create test story with characters of different clearance levels
	testStory := &testMockStory{}
	testStory.chars = []story.Character{
		{ID: "admin", Name: "Admin", Clearance: 5, Department: "test"},
		{ID: "manager", Name: "Manager", Clearance: 4, Department: "test"},
		{ID: "user", Name: "User", Clearance: 3, Department: "test"},
		{ID: "guest", Name: "Guest", Clearance: 1, Department: "test"},
	}
	testStory.depts = []story.Department{
		{
			ID:        "test",
			Name:      "Test Department",
			FileShare: "testshare",
			Members:   []string{"admin", "manager", "user", "guest"},
			QuotaMB:   100,
			GuestRead: false,
		},
	}

	userMgr := NewUserManager(store, credentials, authorizer, shareManager, testStory)

	ctx := context.Background()
	if err := userMgr.Setup(ctx, "http://localhost:8080"); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	bucketName := s3.FileShareBucketPrefix + "testshare"

	// Verify clearance-based roles
	testCases := []struct {
		userID       string
		clearance    int
		expectedRole string
	}{
		{"admin", 5, auth.RoleBucketAdmin},
		{"manager", 4, auth.RoleBucketAdmin},
		{"user", 3, auth.RoleBucketWrite},
		{"guest", 1, auth.RoleBucketRead},
	}

	for _, tc := range testCases {
		found := false
		for _, binding := range authorizer.Bindings.List() {
			if binding.UserID == tc.userID && binding.BucketScope == bucketName {
				if binding.RoleName != tc.expectedRole {
					t.Errorf("User %s (clearance %d) has role %s, want %s",
						tc.userID, tc.clearance, binding.RoleName, tc.expectedRole)
				}
				found = true
				break
			}
		}
		if !found {
			t.Errorf("No binding found for user %s (clearance %d)", tc.userID, tc.clearance)
		}
	}
}

func TestGenerateDeterministicPublicKey(t *testing.T) {
	// Test that the same ID always produces the same key
	key1 := generateDeterministicPublicKey("alice")
	key2 := generateDeterministicPublicKey("alice")

	if key1 != key2 {
		t.Error("Deterministic public key not consistent for same ID")
	}

	// Test that different IDs produce different keys
	key3 := generateDeterministicPublicKey("bob")
	if key1 == key3 {
		t.Error("Different IDs produced the same public key")
	}

	// Test key format (should be hex string)
	if len(key1) != 64 {
		t.Errorf("Public key length = %d, want 64 (32 bytes hex)", len(key1))
	}
}

// testMockStory is a test mock story that allows setting characters and departments
type testMockStory struct {
	chars []story.Character
	depts []story.Department
}

func (m *testMockStory) Name() string {
	return "test_story"
}

func (m *testMockStory) Description() string {
	return "Test story"
}

func (m *testMockStory) Duration() time.Duration {
	return 10 * time.Hour
}

func (m *testMockStory) Timeline() []story.TimelineEvent {
	return []story.TimelineEvent{
		{Time: 0, Type: "start", Description: "Start", Severity: 1},
	}
}

func (m *testMockStory) Characters() []story.Character {
	return m.chars
}

func (m *testMockStory) Departments() []story.Department {
	return m.depts
}

func (m *testMockStory) DocumentRules() []story.DocumentRule {
	return []story.DocumentRule{}
}

// createTestStore creates a test S3 store with CAS enabled
func createTestStore(t *testing.T) *s3.Store {
	t.Helper()
	tempDir := t.TempDir()
	quotaMgr := s3.NewQuotaManager(10 * 1024 * 1024 * 1024) // 10GB quota for tests
	masterKey := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	store, err := s3.NewStoreWithCAS(tempDir, quotaMgr, masterKey)
	if err != nil {
		t.Fatalf("NewStoreWithCAS() error = %v", err)
	}
	return store
}
