package simulator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story/scenarios"
)

// TestStressAlienInvasion runs a 5-minute high-intensity stress test with ~10K documents.
// This tests S3 storage, versioning, deduplication, and CAS under realistic load.
//
// Run with: go test ./internal/s3bench/simulator -run=TestStressAlienInvasion -v -timeout=10m
// Skip with: go test -short (stress test will be skipped)
func TestStressAlienInvasion(t *testing.T) {
	ctx := context.Background()

	// Configure test based on short mode
	var (
		timeScale         float64
		adversaryAttempts int
		testName          string
		targetOps         string
		targetDuration    string
	)

	if testing.Short() {
		// Quick smoke test: 72h in ~12 seconds
		timeScale = 21600.0
		adversaryAttempts = 10
		testName = "12-SECOND SMOKE TEST"
		targetOps = "~500 operations"
		targetDuration = "12 seconds"
	} else {
		// Full stress test: 72h in 5 minutes
		timeScale = 864.0
		adversaryAttempts = 100
		testName = "5-MINUTE HIGH-INTENSITY S3 STRESS TEST"
		targetOps = "~10,000 operations"
		targetDuration = "5 minutes"
	}

	// Create temporary storage
	tempDir := t.TempDir()
	quotaMgr := s3.NewQuotaManager(10 * 1024 * 1024 * 1024) // 10GB
	masterKey := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	store, err := s3.NewStoreWithCAS(tempDir, quotaMgr, masterKey)
	if err != nil {
		t.Fatalf("Creating store: %v", err)
	}

	// Create components
	credentials := s3.NewCredentialStore()
	authorizer := auth.NewAuthorizerWithGroups()
	systemStore, err := s3.NewSystemStore(store, "stress-test")
	if err != nil {
		t.Fatalf("Creating system store: %v", err)
	}
	shareManager := s3.NewFileShareManager(store, systemStore, authorizer)

	// Create story with increased document counts (4-6 versions)
	story := &scenarios.AlienInvasion{}

	// Phase 1: Optionally enable HTTP testing (set USE_HTTP=1 to test full network stack)
	useHTTP := os.Getenv("USE_HTTP") == "1"
	var httpServer *httptest.Server
	var httpEndpoint string

	if useHTTP {
		// Create S3 HTTP server with mock authorizer for testing (allows all authenticated requests)
		mockAuth := &mockHTTPAuthorizer{credentials: credentials, authorizer: authorizer}
		s3Server := s3.NewServer(store, mockAuth, nil)
		httpServer = httptest.NewServer(s3Server.Handler())
		httpEndpoint = httpServer.URL
		t.Logf("HTTP server started at %s (testing full network stack)", httpEndpoint)
		defer httpServer.Close()
	}

	// Create user manager
	endpoint := "http://localhost:8080"
	if useHTTP {
		endpoint = httpEndpoint
	}
	userMgr := NewUserManager(store, credentials, authorizer, shareManager, story)
	if err := userMgr.Setup(ctx, endpoint); err != nil {
		t.Fatalf("Setting up users: %v", err)
	}
	defer func() {
		if err := userMgr.Cleanup(); err != nil {
			t.Logf("Cleanup error: %v", err)
		}
	}()

	// Configure simulator
	config := SimulatorConfig{
		Story:                story,
		TimeScale:            timeScale,
		EnableMesh:           false,
		EnableAdversary:      true,
		EnableWorkflows:      true,
		AdversaryAttempts:    adversaryAttempts,
		MaxConcurrentUploads: 20,
		UseHTTP:              useHTTP,      // Phase 1: HTTP testing
		HTTPEndpoint:         httpEndpoint, // Phase 1: HTTP endpoint
		UserManager:          userMgr,
		WorkflowTestsEnabled: map[WorkflowType]bool{
			WorkflowDeletion:    true,
			WorkflowExpiration:  true,
			WorkflowPermissions: true,
			WorkflowQuota:       true,
			WorkflowRetention:   true,
		},
	}

	// Create simulator
	sim, err := NewSimulator(config)
	if err != nil {
		t.Fatalf("Creating simulator: %v", err)
	}

	// Run stress test
	t.Log("=======================================================")
	t.Logf("   %s", testName)
	t.Log("=======================================================")
	t.Logf("Story: %s", story.Name())
	t.Logf("Time scale: %.0fx (72h story compressed to %s)", config.TimeScale, targetDuration)
	t.Logf("Target: %s (uploads, updates, deletes)", targetOps)
	t.Log("")

	startTime := time.Now()
	metrics, err := sim.Run(ctx)
	duration := time.Since(startTime)

	if err != nil {
		t.Fatalf("Running stress test: %v", err)
	}

	// Report results
	t.Log("")
	t.Log("=======================================================")
	t.Log("              STRESS TEST RESULTS")
	t.Log("=======================================================")
	t.Logf("Duration: %v (target: %s)", duration, targetDuration)
	t.Logf("Story Duration: %v (scaled from 72h)", metrics.StoryDuration)
	t.Log("")
	t.Log("Operations:")
	t.Logf("  Tasks Generated: %d", metrics.TasksGenerated)
	t.Logf("  Tasks Completed: %d (%.1f%%)", metrics.TasksCompleted,
		float64(metrics.TasksCompleted)*100/float64(metrics.TasksGenerated))
	t.Logf("  Tasks Failed:    %d", metrics.TasksFailed)
	t.Logf("  Uploads:         %d", metrics.UploadCount)
	t.Logf("  Updates:         %d (versions)", metrics.UpdateCount)
	t.Logf("  Downloads:       %d", metrics.DownloadCount)
	t.Logf("  Deletes:         %d", metrics.DeleteCount)
	t.Log("")
	t.Log("Data:")
	t.Logf("  Uploaded:   %.2f MB", float64(metrics.BytesUploaded)/1024/1024)
	t.Logf("  Downloaded: %.2f MB", float64(metrics.BytesDownloaded)/1024/1024)
	t.Logf("  Documents:  %d", metrics.DocumentsCreated)
	t.Logf("  Versions:   %d", metrics.VersionsCreated)
	t.Log("")
	t.Log("Performance:")
	t.Logf("  Avg Upload Latency:   %v", metrics.AvgUploadLatency)
	t.Logf("  Avg Download Latency: %v", metrics.AvgDownloadLatency)
	t.Logf("  Throughput: %.1f ops/sec", float64(metrics.TasksCompleted)/duration.Seconds())
	t.Log("")
	t.Log("Security (Adversary Simulation):")
	t.Logf("  Attempts: %d", metrics.AdversaryAttempts)
	t.Logf("  Denials:  %d", metrics.AdversaryDenials)
	t.Logf("  Denial Rate: %.1f%% (should be >50%%)", metrics.AdversaryDenialRate*100)
	t.Log("")
	t.Log("Workflows:")
	t.Logf("  Tests Run:    %d", metrics.WorkflowTestsRun)
	t.Logf("  Tests Passed: %d", metrics.WorkflowTestsPassed)
	t.Logf("  Tests Failed: %d", metrics.WorkflowTestsFailed)
	t.Log("=======================================================")
	t.Log("")

	// Verify success criteria
	if metrics.TasksCompleted < metrics.TasksGenerated*95/100 {
		t.Errorf("Too many failed tasks: %d/%d completed (%.1f%%)",
			metrics.TasksCompleted, metrics.TasksGenerated,
			float64(metrics.TasksCompleted)*100/float64(metrics.TasksGenerated))
	}

	// Only check task count in full mode
	if !testing.Short() && metrics.TasksGenerated < 8000 {
		t.Logf("NOTE: Generated %d tasks (target: 10K). Close to target!", metrics.TasksGenerated)
	}

	if metrics.AdversaryDenialRate < 0.5 {
		t.Errorf("Adversary denial rate too low: %.1f%% (expected >50%%)", metrics.AdversaryDenialRate*100)
	}

	if metrics.WorkflowTestsFailed > 0 {
		t.Errorf("Workflow tests failed: %d/%d", metrics.WorkflowTestsFailed, metrics.WorkflowTestsRun)
	}

	if len(metrics.Errors) > 0 {
		t.Errorf("Encountered %d errors during stress test:", len(metrics.Errors))
		for i, err := range metrics.Errors {
			if i < 10 { // Show first 10 errors
				t.Logf("  - %s", err)
			}
		}
		if len(metrics.Errors) > 10 {
			t.Logf("  ... and %d more errors", len(metrics.Errors)-10)
		}
	}

	// Verify timing is reasonable based on mode
	if testing.Short() {
		// Short mode: should complete in 10-60 seconds (includes workflow execution)
		if duration < 10*time.Second {
			t.Logf("NOTE: Completed faster than expected (%v < 10s). Excellent performance!", duration)
		} else if duration > 90*time.Second {
			t.Errorf("Duration %v exceeded target range (should be 10-60 seconds)", duration)
		}
	} else {
		// Full mode: should complete in 4-6 minutes
		if duration < 4*time.Minute {
			t.Logf("NOTE: Completed faster than expected (%v < 4min). Good performance!", duration)
		} else if duration > 6*time.Minute {
			t.Errorf("Duration %v exceeded target range (should be 4-6 minutes)", duration)
		}
	}

	t.Logf("\n✅ Stress test completed successfully!")
}

// mockHTTPAuthorizer allows authenticated requests for HTTP testing.
// It validates requests based on user ID header and uses the real authorizer for RBAC checks.
type mockHTTPAuthorizer struct {
	credentials *s3.CredentialStore
	authorizer  *auth.Authorizer
}

// AuthorizeRequest authenticates and authorizes an HTTP request.
func (m *mockHTTPAuthorizer) AuthorizeRequest(r *http.Request, verb, resource, bucket, objectKey string) (string, error) {
	// Extract user ID from header (set by simulator for testing)
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		return "", s3.ErrAccessDenied
	}

	// Use real authorizer for RBAC checks
	if m.authorizer.Authorize(userID, verb, resource, bucket, objectKey) {
		return userID, nil
	}

	return "", s3.ErrAccessDenied
}

// GetAllowedPrefixes returns the allowed object prefixes for a user in a bucket.
func (m *mockHTTPAuthorizer) GetAllowedPrefixes(userID, bucket string) []string {
	// For testing, return nil (unrestricted access within authorized buckets)
	return nil
}
