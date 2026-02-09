package simulator

import (
	"context"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story/scenarios"
)

func TestWorkflowGenerator_GenerateDeletionWorkflow(t *testing.T) {
	mock := &mockStory{}
	gen := NewWorkflowGenerator(mock, 1.0)

	alice := story.Character{
		ID:         "alice",
		Name:       "Alice",
		Role:       "Commander",
		Department: "command",
		Clearance:  5,
		Alignment:  "good",
		DockerPeer: "alice",
	}

	test := gen.GenerateDeletionWorkflow(10*time.Hour, alice)

	// Verify test structure
	if test.TestID == "" {
		t.Error("Test should have ID")
	}

	if test.Type != WorkflowDeletion {
		t.Errorf("Test type = %v, want %v", test.Type, WorkflowDeletion)
	}

	if test.StoryTime != 10*time.Hour {
		t.Errorf("Story time = %v, want 10h", test.StoryTime)
	}

	if test.Actor.ID != alice.ID {
		t.Errorf("Actor = %v, want %v", test.Actor.ID, alice.ID)
	}

	// Verify parameters
	if test.Parameters["document_name"] == "" {
		t.Error("Should have document_name parameter")
	}

	if test.Parameters["content_size"] == 0 {
		t.Error("Should have content_size parameter")
	}

	// Verify expectations
	if !test.Expected.ShouldSucceed {
		t.Error("Deletion workflow should succeed")
	}

	if len(test.Expected.ValidationChecks) == 0 {
		t.Error("Should have validation checks")
	}

	t.Logf("Deletion workflow test: %+v", test)
}

func TestWorkflowGenerator_GenerateExpirationWorkflow(t *testing.T) {
	mock := &mockStory{}
	gen := NewWorkflowGenerator(mock, 1.0)

	bob := story.Character{
		ID:         "bob",
		Name:       "Bob",
		Role:       "Analyst",
		Department: "intel",
		Clearance:  3,
		Alignment:  "good",
		DockerPeer: "bob",
	}

	test := gen.GenerateExpirationWorkflow(20*time.Hour, bob)

	if test.Type != WorkflowExpiration {
		t.Errorf("Test type = %v, want %v", test.Type, WorkflowExpiration)
	}

	// Verify expiration-specific parameters
	if test.Parameters["expiration_time"] == nil {
		t.Error("Should have expiration_time parameter")
	}

	if test.Parameters["check_interval"] == nil {
		t.Error("Should have check_interval parameter")
	}

	// Verify expected state
	if test.Expected.ExpectedState != "expired_and_cleaned" {
		t.Errorf("Expected state = %v, want expired_and_cleaned", test.Expected.ExpectedState)
	}

	t.Logf("Expiration workflow test: %+v", test)
}

func TestWorkflowGenerator_GeneratePermissionWorkflow(t *testing.T) {
	mock := &mockStory{}
	gen := NewWorkflowGenerator(mock, 1.0)

	requester := story.Character{
		ID:         "bob",
		Name:       "Bob",
		Role:       "Analyst",
		Department: "intel",
		Clearance:  3,
		Alignment:  "good",
		DockerPeer: "bob",
	}

	owner := story.Character{
		ID:         "alice",
		Name:       "Alice",
		Role:       "Commander",
		Department: "command",
		Clearance:  5,
		Alignment:  "good",
		DockerPeer: "alice",
	}

	testCases := []struct {
		name          string
		shouldApprove bool
		expectedState string
	}{
		{"approval", true, "access_granted"},
		{"denial", false, "access_denied"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			test := gen.GeneratePermissionWorkflow(15*time.Hour, requester, owner, tc.shouldApprove)

			if test.Type != WorkflowPermissions {
				t.Errorf("Test type = %v, want %v", test.Type, WorkflowPermissions)
			}

			if test.Actor.ID != requester.ID {
				t.Errorf("Actor = %v, want %v", test.Actor.ID, requester.ID)
			}

			if test.Target.ID != owner.ID {
				t.Errorf("Target = %v, want %v", test.Target.ID, owner.ID)
			}

			if test.Parameters["should_approve"] != tc.shouldApprove {
				t.Errorf("should_approve = %v, want %v", test.Parameters["should_approve"], tc.shouldApprove)
			}

			if test.Expected.ExpectedState != tc.expectedState {
				t.Errorf("Expected state = %v, want %v", test.Expected.ExpectedState, tc.expectedState)
			}
		})
	}
}

func TestWorkflowGenerator_GenerateQuotaWorkflow(t *testing.T) {
	mock := &mockStory{}
	gen := NewWorkflowGenerator(mock, 1.0)

	scientist := story.Character{
		ID:         "scientist",
		Name:       "Dr. Wright",
		Role:       "Scientist",
		Department: "science",
		Clearance:  4,
		Alignment:  "good",
		DockerPeer: "bob",
	}

	quotaMB := int64(500)
	test := gen.GenerateQuotaWorkflow(8*time.Hour, scientist, quotaMB)

	if test.Type != WorkflowQuota {
		t.Errorf("Test type = %v, want %v", test.Type, WorkflowQuota)
	}

	if test.Parameters["quota_mb"] != quotaMB {
		t.Errorf("quota_mb = %v, want %v", test.Parameters["quota_mb"], quotaMB)
	}

	// Verify documents_to_fill exceeds quota
	docsToFill := test.Parameters["documents_to_fill"].(int)
	if docsToFill <= int(quotaMB) {
		t.Errorf("documents_to_fill = %v, should exceed quota %v", docsToFill, quotaMB)
	}

	// Verify expected state
	if test.Expected.ExpectedState != "quota_enforced_and_freed" {
		t.Errorf("Expected state = %v, want quota_enforced_and_freed", test.Expected.ExpectedState)
	}

	t.Logf("Quota workflow test: %+v", test)
}

func TestWorkflowGenerator_GenerateRetentionWorkflow(t *testing.T) {
	mock := &mockStory{}
	gen := NewWorkflowGenerator(mock, 1.0)

	commander := story.Character{
		ID:         "commander",
		Name:       "General Chen",
		Role:       "Commander",
		Department: "command",
		Clearance:  5,
		Alignment:  "good",
		DockerPeer: "alice",
	}

	versionCount := 20
	test := gen.GenerateRetentionWorkflow(36*time.Hour, commander, versionCount)

	if test.Type != WorkflowRetention {
		t.Errorf("Test type = %v, want %v", test.Type, WorkflowRetention)
	}

	if test.Parameters["version_count"] != versionCount {
		t.Errorf("version_count = %v, want %v", test.Parameters["version_count"], versionCount)
	}

	// Verify retention policy
	if test.Parameters["retention_policy"] == "" {
		t.Error("Should have retention_policy parameter")
	}

	// Verify expected state
	if test.Expected.ExpectedState != "versions_pruned" {
		t.Errorf("Expected state = %v, want versions_pruned", test.Expected.ExpectedState)
	}

	t.Logf("Retention workflow test: %+v", test)
}

func TestWorkflowGenerator_GenerateAllWorkflows(t *testing.T) {
	// Use alien invasion scenario for realistic test
	ai := &scenarios.AlienInvasion{}
	gen := NewWorkflowGenerator(ai, 1.0)

	tests, err := gen.GenerateAllWorkflows(context.Background())
	if err != nil {
		t.Fatalf("GenerateAllWorkflows() error = %v", err)
	}

	if len(tests) == 0 {
		t.Fatal("No workflow tests generated")
	}

	t.Logf("Generated %d workflow tests", len(tests))

	// Verify we have different workflow types
	typeCount := make(map[WorkflowType]int)
	for _, test := range tests {
		typeCount[test.Type]++
	}

	t.Logf("Workflow types: %+v", typeCount)

	// Verify we have at least some of each type
	requiredTypes := []WorkflowType{
		WorkflowDeletion,
		WorkflowExpiration,
		WorkflowPermissions,
		WorkflowQuota,
		WorkflowRetention,
	}

	for _, reqType := range requiredTypes {
		if typeCount[reqType] == 0 {
			t.Errorf("Missing workflow type: %v", reqType)
		}
	}

	// Verify tests are at appropriate times
	for _, test := range tests {
		if test.StoryTime < 0 || test.StoryTime >= ai.Duration() {
			t.Errorf("Test %s has invalid story time %v", test.TestID, test.StoryTime)
		}

		if test.RealTime != test.StoryTime {
			t.Errorf("Test %s real time %v != story time %v (timeScale=1.0)",
				test.TestID, test.RealTime, test.StoryTime)
		}
	}

	// Verify each test has required fields
	for _, test := range tests {
		if test.TestID == "" {
			t.Error("Test missing TestID")
		}
		if test.Type == "" {
			t.Error("Test missing Type")
		}
		if test.Actor.ID == "" {
			t.Error("Test missing Actor")
		}
		if len(test.Parameters) == 0 {
			t.Errorf("Test %s missing Parameters", test.TestID)
		}
		if len(test.Expected.ValidationChecks) == 0 {
			t.Errorf("Test %s missing ValidationChecks", test.TestID)
		}
	}
}

func TestWorkflowGenerator_TimeScaling(t *testing.T) {
	ai := &scenarios.AlienInvasion{}

	testCases := []struct {
		name      string
		timeScale float64
	}{
		{"realtime", 1.0},
		{"10x", 10.0},
		{"100x", 100.0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gen := NewWorkflowGenerator(ai, tc.timeScale)
			tests, err := gen.GenerateAllWorkflows(context.Background())
			if err != nil {
				t.Fatalf("GenerateAllWorkflows() error = %v", err)
			}

			// Verify time scaling is applied to all tests
			for _, test := range tests {
				expectedRealTime := story.ScaledDuration(test.StoryTime, tc.timeScale)
				if test.RealTime != expectedRealTime {
					t.Errorf("Test %s: RealTime=%v, want %v (StoryTime=%v, scale=%v)",
						test.TestID, test.RealTime, expectedRealTime, test.StoryTime, tc.timeScale)
				}

				// Verify parameters that are durations are also scaled
				if expirationTime, ok := test.Parameters["expiration_time"].(time.Duration); ok {
					// Expiration time should be scaled
					if expirationTime > time.Hour {
						t.Errorf("Test %s: expiration_time %v not scaled (timeScale=%v)",
							test.TestID, expirationTime, tc.timeScale)
					}
				}

				// Verify expected duration is scaled
				if test.Expected.ExpectedDuration > ai.Duration() {
					t.Errorf("Test %s: ExpectedDuration %v exceeds story duration %v",
						test.TestID, test.Expected.ExpectedDuration, ai.Duration())
				}
			}
		})
	}
}

func TestWorkflowGenerator_UniqueTestIDs(t *testing.T) {
	mock := &mockStory{}
	gen := NewWorkflowGenerator(mock, 1.0)

	alice := story.Character{
		ID:         "alice",
		Name:       "Alice",
		Role:       "Commander",
		Department: "command",
		Clearance:  5,
		Alignment:  "good",
		DockerPeer: "alice",
	}

	// Generate multiple tests
	test1 := gen.GenerateDeletionWorkflow(10*time.Hour, alice)
	test2 := gen.GenerateDeletionWorkflow(20*time.Hour, alice)
	test3 := gen.GenerateExpirationWorkflow(30*time.Hour, alice)

	// Verify IDs are unique
	ids := map[string]bool{
		test1.TestID: true,
		test2.TestID: true,
		test3.TestID: true,
	}

	if len(ids) != 3 {
		t.Errorf("Test IDs not unique: %v, %v, %v", test1.TestID, test2.TestID, test3.TestID)
	}
}

func TestWorkflowGenerator_ValidationChecks(t *testing.T) {
	mock := &mockStory{}
	gen := NewWorkflowGenerator(mock, 1.0)

	alice := story.Character{
		ID:         "alice",
		Name:       "Alice",
		Role:       "Commander",
		Department: "command",
		Clearance:  5,
		Alignment:  "good",
		DockerPeer: "alice",
	}

	// Test each workflow type has appropriate validation checks
	testCases := []struct {
		name           string
		test           WorkflowTest
		requiredChecks []string
	}{
		{
			name: "deletion",
			test: gen.GenerateDeletionWorkflow(10*time.Hour, alice),
			requiredChecks: []string{
				"document_uploaded",
				"document_deleted",
				"tombstone_created",
			},
		},
		{
			name: "expiration",
			test: gen.GenerateExpirationWorkflow(10*time.Hour, alice),
			requiredChecks: []string{
				"document_uploaded_with_ttl",
				"document_inaccessible_after_expiry",
			},
		},
		{
			name: "quota",
			test: gen.GenerateQuotaWorkflow(10*time.Hour, alice, 500),
			requiredChecks: []string{
				"upload_failed_when_quota_exceeded",
				"upload_succeeded_after_freeing_space",
			},
		},
		{
			name: "retention",
			test: gen.GenerateRetentionWorkflow(10*time.Hour, alice, 20),
			requiredChecks: []string{
				"all_versions_created",
				"correct_versions_kept",
				"correct_versions_deleted",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			for _, requiredCheck := range tc.requiredChecks {
				found := false
				for _, check := range tc.test.Expected.ValidationChecks {
					if check == requiredCheck {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Test %s missing required validation check: %s", tc.test.TestID, requiredCheck)
				}
			}
		})
	}
}
