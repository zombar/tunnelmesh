package simulator

import (
	"context"
	"fmt"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
)

// WorkflowType represents different workflow scenarios to test.
type WorkflowType string

const (
	WorkflowDeletion    WorkflowType = "deletion"    // Test document deletion and tombstones
	WorkflowExpiration  WorkflowType = "expiration"  // Test document expiration enforcement
	WorkflowPermissions WorkflowType = "permissions" // Test file share permission requests
	WorkflowQuota       WorkflowType = "quota"       // Test file share quota enforcement
	WorkflowRetention   WorkflowType = "retention"   // Test version retention policies
)

// WorkflowTest represents a single workflow test scenario.
type WorkflowTest struct {
	// Identification
	TestID    string        // Unique test identifier
	Type      WorkflowType  // Type of workflow being tested
	StoryTime time.Duration // When this happens in story time
	RealTime  time.Duration // When this happens in real time (scaled)

	// Participants
	Actor  story.Character // Primary character performing the workflow
	Target story.Character // Optional: target character (for permission requests)

	// Test parameters
	Parameters map[string]interface{} // Workflow-specific parameters

	// Expected outcomes
	Expected WorkflowExpectation // What should happen
}

// WorkflowExpectation defines the expected outcome of a workflow test.
type WorkflowExpectation struct {
	ShouldSucceed    bool                   // Whether the workflow should succeed
	ExpectedDuration time.Duration          // Expected time to complete
	ExpectedState    string                 // Expected final state
	ValidationChecks []string               // List of things to validate
	Metrics          map[string]interface{} // Metrics to collect
}

// WorkflowGenerator generates workflow test scenarios based on story events.
type WorkflowGenerator struct {
	story     story.Story
	timeScale float64
	testCount int // Counter for generating test IDs
}

// NewWorkflowGenerator creates a new workflow generator.
func NewWorkflowGenerator(s story.Story, timeScale float64) *WorkflowGenerator {
	return &WorkflowGenerator{
		story:     s,
		timeScale: timeScale,
	}
}

// GenerateDeletionWorkflow creates a deletion workflow test.
// Scenario: Upload document → Delete → Verify tombstone → Attempt access (should fail) → Restore → Access (should work)
func (w *WorkflowGenerator) GenerateDeletionWorkflow(storyTime time.Duration, actor story.Character) WorkflowTest {
	w.testCount++

	return WorkflowTest{
		TestID:    fmt.Sprintf("deletion_%03d", w.testCount),
		Type:      WorkflowDeletion,
		StoryTime: storyTime,
		RealTime:  story.ScaledDuration(storyTime, w.timeScale),
		Actor:     actor,
		Parameters: map[string]interface{}{
			"document_name": fmt.Sprintf("deletion_test_%d.txt", w.testCount),
			"content_size":  5000, // 5KB test document
			"restore_delay": story.ScaledDuration(30*time.Second, w.timeScale),
		},
		Expected: WorkflowExpectation{
			ShouldSucceed:    true,
			ExpectedDuration: story.ScaledDuration(2*time.Minute, w.timeScale),
			ExpectedState:    "restored",
			ValidationChecks: []string{
				"document_uploaded",
				"document_deleted",
				"tombstone_created",
				"access_denied_after_delete",
				"document_restored",
				"access_granted_after_restore",
			},
			Metrics: map[string]interface{}{
				"deletion_latency_ms":    0, // To be measured
				"restoration_latency_ms": 0, // To be measured
			},
		},
	}
}

// GenerateExpirationWorkflow creates an expiration workflow test.
// Scenario: Upload with short TTL → Wait for expiration → Verify inaccessible → GC cleanup
func (w *WorkflowGenerator) GenerateExpirationWorkflow(storyTime time.Duration, actor story.Character) WorkflowTest {
	w.testCount++

	// Short expiration for testing (scaled by timeScale)
	expirationTime := story.ScaledDuration(2*time.Minute, w.timeScale)

	return WorkflowTest{
		TestID:    fmt.Sprintf("expiration_%03d", w.testCount),
		Type:      WorkflowExpiration,
		StoryTime: storyTime,
		RealTime:  story.ScaledDuration(storyTime, w.timeScale),
		Actor:     actor,
		Parameters: map[string]interface{}{
			"document_name":   fmt.Sprintf("expiring_doc_%d.txt", w.testCount),
			"content_size":    3000, // 3KB test document
			"expiration_time": expirationTime,
			"check_interval":  story.ScaledDuration(30*time.Second, w.timeScale),
		},
		Expected: WorkflowExpectation{
			ShouldSucceed:    true,
			ExpectedDuration: expirationTime + story.ScaledDuration(1*time.Minute, w.timeScale),
			ExpectedState:    "expired_and_cleaned",
			ValidationChecks: []string{
				"document_uploaded_with_ttl",
				"document_accessible_before_expiry",
				"document_inaccessible_after_expiry",
				"gc_cleaned_expired_document",
			},
			Metrics: map[string]interface{}{
				"expiration_enforcement_accuracy_ms": 0, // To be measured
				"gc_cleanup_latency_ms":              0, // To be measured
			},
		},
	}
}

// GeneratePermissionWorkflow creates a permission request workflow test.
// Scenario: Request access to file share → Owner approves/denies → Test access
func (w *WorkflowGenerator) GeneratePermissionWorkflow(storyTime time.Duration, requester, owner story.Character, shouldApprove bool) WorkflowTest {
	w.testCount++

	expectedState := "access_granted"
	if !shouldApprove {
		expectedState = "access_denied"
	}

	return WorkflowTest{
		TestID:    fmt.Sprintf("permission_%03d", w.testCount),
		Type:      WorkflowPermissions,
		StoryTime: storyTime,
		RealTime:  story.ScaledDuration(storyTime, w.timeScale),
		Actor:     requester,
		Target:    owner,
		Parameters: map[string]interface{}{
			"fileshare_name": fmt.Sprintf("%s-share", owner.Department),
			"should_approve": shouldApprove,
			"response_delay": story.ScaledDuration(15*time.Second, w.timeScale),
		},
		Expected: WorkflowExpectation{
			ShouldSucceed:    true, // Workflow succeeds regardless of approval
			ExpectedDuration: story.ScaledDuration(1*time.Minute, w.timeScale),
			ExpectedState:    expectedState,
			ValidationChecks: []string{
				"permission_request_created",
				"request_appears_in_pending",
				"owner_responded",
				"access_matches_approval",
			},
			Metrics: map[string]interface{}{
				"request_creation_latency_ms": 0, // To be measured
				"response_latency_ms":         0, // To be measured
				"access_check_latency_ms":     0, // To be measured
			},
		},
	}
}

// GenerateQuotaWorkflow creates a quota enforcement workflow test.
// Scenario: Upload until quota exceeded → Delete to free space → Upload again (should succeed)
func (w *WorkflowGenerator) GenerateQuotaWorkflow(storyTime time.Duration, actor story.Character, quotaMB int64) WorkflowTest {
	w.testCount++

	return WorkflowTest{
		TestID:    fmt.Sprintf("quota_%03d", w.testCount),
		Type:      WorkflowQuota,
		StoryTime: storyTime,
		RealTime:  story.ScaledDuration(storyTime, w.timeScale),
		Actor:     actor,
		Parameters: map[string]interface{}{
			"fileshare_name":      fmt.Sprintf("%s-share", actor.Department),
			"quota_mb":            quotaMB,
			"document_size_mb":    1,                // 1MB documents
			"documents_to_fill":   int(quotaMB) + 2, // Upload more than quota
			"documents_to_delete": 3,                // Delete 3 to free space
		},
		Expected: WorkflowExpectation{
			ShouldSucceed:    true,
			ExpectedDuration: story.ScaledDuration(3*time.Minute, w.timeScale),
			ExpectedState:    "quota_enforced_and_freed",
			ValidationChecks: []string{
				"uploads_succeeded_until_quota",
				"upload_failed_when_quota_exceeded",
				"deletions_freed_space",
				"upload_succeeded_after_freeing_space",
				"quota_calculation_accurate",
			},
			Metrics: map[string]interface{}{
				"quota_enforcement_accuracy":   0, // % accuracy
				"quota_check_latency_ms":       0, // To be measured
				"space_reclamation_latency_ms": 0, // To be measured
			},
		},
	}
}

// GenerateRetentionWorkflow creates a version retention workflow test.
// Scenario: Create many versions → Apply retention policy → Verify correct versions kept/deleted
func (w *WorkflowGenerator) GenerateRetentionWorkflow(storyTime time.Duration, actor story.Character, versionCount int) WorkflowTest {
	w.testCount++

	return WorkflowTest{
		TestID:    fmt.Sprintf("retention_%03d", w.testCount),
		Type:      WorkflowRetention,
		StoryTime: storyTime,
		RealTime:  story.ScaledDuration(storyTime, w.timeScale),
		Actor:     actor,
		Parameters: map[string]interface{}{
			"document_name":    fmt.Sprintf("versioned_doc_%d.txt", w.testCount),
			"version_count":    versionCount,
			"version_interval": story.ScaledDuration(5*time.Minute, w.timeScale),
			"retention_policy": "keep_recent_5_weekly_monthly", // Keep 5 recent, then weekly, then monthly
			"versions_to_keep": []int{1, 2, 3, 4, 5},           // Specific versions to retain
		},
		Expected: WorkflowExpectation{
			ShouldSucceed:    true,
			ExpectedDuration: story.ScaledDuration(time.Duration(versionCount)*5*time.Minute+2*time.Minute, w.timeScale),
			ExpectedState:    "versions_pruned",
			ValidationChecks: []string{
				"all_versions_created",
				"retention_policy_applied",
				"correct_versions_kept",
				"correct_versions_deleted",
				"storage_reduced",
			},
			Metrics: map[string]interface{}{
				"pruning_latency_ms":      0, // To be measured
				"storage_savings_percent": 0, // % reduction
				"versions_kept":           0, // Count
				"versions_deleted":        0, // Count
			},
		},
	}
}

// GenerateAllWorkflows generates all workflow tests for the scenario.
func (w *WorkflowGenerator) GenerateAllWorkflows(ctx context.Context) ([]WorkflowTest, error) {
	var tests []WorkflowTest

	duration := w.story.Duration()
	characters := w.story.Characters()
	departments := w.story.Departments()

	// Find characters for different workflows
	var commander, scientist, adversary *story.Character
	for i := range characters {
		switch characters[i].Role {
		case "Commander", "Military Commander":
			commander = &characters[i]
		case "Lead Xenobiologist", "Xenobiologist":
			scientist = &characters[i]
		}
		if characters[i].IsAdversary() {
			adversary = &characters[i]
		}
	}

	// Generate deletion workflow test (at 16h in story - leaked report scenario)
	if commander != nil {
		deletionTime := 16 * time.Hour
		if deletionTime < duration {
			tests = append(tests, w.GenerateDeletionWorkflow(deletionTime, *commander))
		}
	}

	// Generate expiration workflow test (at 24h - temporary evacuation orders)
	if commander != nil {
		expirationTime := 24 * time.Hour
		if expirationTime < duration {
			tests = append(tests, w.GenerateExpirationWorkflow(expirationTime, *commander))
		}
	}

	// Generate permission workflow test (at 20h - scientist requests access, denied)
	if scientist != nil && commander != nil {
		permissionTime := 20 * time.Hour
		if permissionTime < duration {
			tests = append(tests, w.GeneratePermissionWorkflow(permissionTime, *scientist, *commander, false))
		}
	}

	// Generate quota workflow test (at 8h - science share hits quota)
	if scientist != nil {
		quotaTime := 8 * time.Hour
		if quotaTime < duration {
			// Find science department quota
			quotaMB := int64(500) // Default
			for _, dept := range departments {
				if dept.ID == scientist.Department {
					if dept.QuotaMB > 0 {
						quotaMB = dept.QuotaMB
					}
					break
				}
			}
			tests = append(tests, w.GenerateQuotaWorkflow(quotaTime, *scientist, quotaMB))
		}
	}

	// Generate retention workflow test (at 36h - old meeting minutes pruned)
	if commander != nil {
		retentionTime := 36 * time.Hour
		if retentionTime < duration {
			tests = append(tests, w.GenerateRetentionWorkflow(retentionTime, *commander, 20))
		}
	}

	// Additional permission test with adversary (should always be denied)
	if adversary != nil && commander != nil {
		adversaryTime := adversary.JoinTime + 2*time.Hour
		if adversaryTime < duration {
			tests = append(tests, w.GeneratePermissionWorkflow(adversaryTime, *adversary, *commander, false))
		}
	}

	return tests, nil
}

// WorkflowResult captures the outcome of a workflow test execution.
type WorkflowResult struct {
	Test           WorkflowTest           // The test that was executed
	Success        bool                   // Whether the test passed
	ActualDuration time.Duration          // How long it actually took
	ActualState    string                 // Actual final state
	CheckResults   map[string]bool        // Results of each validation check
	Metrics        map[string]interface{} // Measured metrics
	Error          error                  // Any error that occurred
}

// WorkflowExecutor is an interface for executing workflow tests.
// The main simulator will implement this to perform actual S3 operations.
type WorkflowExecutor interface {
	// ExecuteDeletionWorkflow performs a deletion workflow test.
	ExecuteDeletionWorkflow(ctx context.Context, test WorkflowTest) (WorkflowResult, error)

	// ExecuteExpirationWorkflow performs an expiration workflow test.
	ExecuteExpirationWorkflow(ctx context.Context, test WorkflowTest) (WorkflowResult, error)

	// ExecutePermissionWorkflow performs a permission request workflow test.
	ExecutePermissionWorkflow(ctx context.Context, test WorkflowTest) (WorkflowResult, error)

	// ExecuteQuotaWorkflow performs a quota enforcement workflow test.
	ExecuteQuotaWorkflow(ctx context.Context, test WorkflowTest) (WorkflowResult, error)

	// ExecuteRetentionWorkflow performs a version retention workflow test.
	ExecuteRetentionWorkflow(ctx context.Context, test WorkflowTest) (WorkflowResult, error)
}
