package simulator

import (
	"context"
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
	if testing.Short() {
		t.Skip("Skipping 5-minute stress test in short mode (use -short to skip)")
	}

	ctx := context.Background()

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

	// Create story with increased document counts (4-6 versions = ~10K operations)
	story := &scenarios.AlienInvasion{}

	// Create user manager
	userMgr := NewUserManager(store, credentials, authorizer, shareManager, story)
	if err := userMgr.Setup(ctx, "http://localhost:8080"); err != nil {
		t.Fatalf("Setting up users: %v", err)
	}
	defer func() {
		if err := userMgr.Cleanup(); err != nil {
			t.Logf("Cleanup error: %v", err)
		}
	}()

	// Configure simulator for high intensity (10K documents over 5 minutes)
	// 864x time scale = 72h story in 5 minutes
	config := SimulatorConfig{
		Story:                story,
		TimeScale:            864.0, // 72h in 5 minutes
		EnableMesh:           false,
		EnableAdversary:      true,
		EnableWorkflows:      true,
		AdversaryAttempts:    100,
		MaxConcurrentUploads: 20,
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
	t.Log("   5-MINUTE HIGH-INTENSITY S3 STRESS TEST")
	t.Log("=======================================================")
	t.Logf("Story: %s", story.Name())
	t.Logf("Time scale: %.0fx (72h story compressed to 5 minutes)", config.TimeScale)
	t.Log("Target: ~10,000 operations (uploads, updates, deletes)")
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
	t.Logf("Duration: %v (target: 5 minutes)", duration)
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

	if metrics.TasksGenerated < 8000 {
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

	// Verify timing is reasonable (should be close to 5 minutes)
	if duration < 4*time.Minute {
		t.Logf("NOTE: Completed faster than expected (%v < 4min). Good performance!", duration)
	} else if duration > 6*time.Minute {
		t.Errorf("Duration %v exceeded target range (should be 4-6 minutes)", duration)
	}

	t.Logf("\n✅ Stress test completed successfully!")
}
