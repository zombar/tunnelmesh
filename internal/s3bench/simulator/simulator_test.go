package simulator

import (
	"context"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story/scenarios"
)

func TestNewSimulator(t *testing.T) {
	ai := &scenarios.AlienInvasion{}
	config := DefaultConfig(ai)

	sim, err := NewSimulator(config)
	if err != nil {
		t.Fatalf("NewSimulator() error = %v", err)
	}

	if sim == nil {
		t.Fatal("Simulator should not be nil")
	}

	if sim.config.Story != ai {
		t.Error("Story not set correctly")
	}

	if sim.workloadGen == nil {
		t.Error("Workload generator should be initialized")
	}

	if sim.workflowGen == nil {
		t.Error("Workflow generator should be initialized")
	}

	// Adversary generators should be created for dark characters
	if len(sim.adversaryGen) == 0 {
		t.Error("Expected adversary generators for dark characters")
	}

	t.Logf("Created simulator with %d adversary generators", len(sim.adversaryGen))
}

func TestSimulator_GenerateScenario(t *testing.T) {
	ai := &scenarios.AlienInvasion{}
	config := DefaultConfig(ai)
	config.TimeScale = 100.0 // Speed up for testing

	sim, err := NewSimulator(config)
	if err != nil {
		t.Fatalf("NewSimulator() error = %v", err)
	}

	ctx := context.Background()
	if err := sim.GenerateScenario(ctx); err != nil {
		t.Fatalf("GenerateScenario() error = %v", err)
	}

	// Verify tasks were generated
	if len(sim.tasks) == 0 {
		t.Error("No tasks generated")
	}

	// Verify adversary attempts were generated
	totalAttempts := 0
	for _, attempts := range sim.attempts {
		totalAttempts += len(attempts)
	}
	if totalAttempts == 0 {
		t.Error("No adversary attempts generated")
	}

	// Verify workflows were generated
	if len(sim.workflows) == 0 {
		t.Error("No workflows generated")
	}

	// Verify metrics were updated
	if sim.metrics.TasksGenerated != len(sim.tasks) {
		t.Errorf("TasksGenerated = %d, want %d", sim.metrics.TasksGenerated, len(sim.tasks))
	}

	if sim.metrics.AdversaryAttempts != totalAttempts {
		t.Errorf("AdversaryAttempts = %d, want %d", sim.metrics.AdversaryAttempts, totalAttempts)
	}

	if sim.metrics.WorkflowTestsRun != len(sim.workflows) {
		t.Errorf("WorkflowTestsRun = %d, want %d", sim.metrics.WorkflowTestsRun, len(sim.workflows))
	}

	t.Logf("Generated scenario: %d tasks, %d adversary attempts, %d workflows",
		len(sim.tasks), totalAttempts, len(sim.workflows))
}

func TestSimulator_GetScenarioSummary(t *testing.T) {
	ai := &scenarios.AlienInvasion{}
	config := DefaultConfig(ai)

	sim, err := NewSimulator(config)
	if err != nil {
		t.Fatalf("NewSimulator() error = %v", err)
	}

	ctx := context.Background()
	if err := sim.GenerateScenario(ctx); err != nil {
		t.Fatalf("GenerateScenario() error = %v", err)
	}

	summary := sim.GetScenarioSummary()

	// Verify summary fields
	if summary.StoryName != ai.Name() {
		t.Errorf("StoryName = %v, want %v", summary.StoryName, ai.Name())
	}

	if summary.StoryDuration != ai.Duration() {
		t.Errorf("StoryDuration = %v, want %v", summary.StoryDuration, ai.Duration())
	}

	if summary.TimeScale != config.TimeScale {
		t.Errorf("TimeScale = %v, want %v", summary.TimeScale, config.TimeScale)
	}

	if summary.Characters == 0 {
		t.Error("Characters should be > 0")
	}

	if summary.Departments == 0 {
		t.Error("Departments should be > 0")
	}

	if summary.Tasks == 0 {
		t.Error("Tasks should be > 0")
	}

	// Verify good vs dark character counts
	if summary.GoodCharacters == 0 {
		t.Error("Should have good characters")
	}

	if summary.DarkCharacters == 0 {
		t.Error("Should have dark characters (adversaries)")
	}

	if summary.GoodCharacters+summary.DarkCharacters != summary.Characters {
		t.Error("Good + dark characters should equal total characters")
	}

	t.Logf("Scenario summary:\n%s", summary.String())
}

func TestSimulator_Run(t *testing.T) {
	ai := &scenarios.AlienInvasion{}
	config := DefaultConfig(ai)
	config.TimeScale = 1000.0 // Very fast for testing
	config.EnableMesh = false // Disable mesh for unit test

	sim, err := NewSimulator(config)
	if err != nil {
		t.Fatalf("NewSimulator() error = %v", err)
	}

	ctx := context.Background()
	metrics, err := sim.Run(ctx)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// Verify metrics
	if metrics == nil {
		t.Fatal("Metrics should not be nil")
	}

	if metrics.StartTime.IsZero() {
		t.Error("StartTime should be set")
	}

	if metrics.EndTime.IsZero() {
		t.Error("EndTime should be set")
	}

	if metrics.Duration == 0 {
		t.Error("Duration should be > 0")
	}

	if metrics.TasksGenerated == 0 {
		t.Error("TasksGenerated should be > 0")
	}

	if metrics.TasksCompleted == 0 {
		t.Error("TasksCompleted should be > 0")
	}

	if metrics.AdversaryAttempts == 0 {
		t.Error("AdversaryAttempts should be > 0")
	}

	// All adversary attempts should be denied
	if metrics.AdversaryDenials != metrics.AdversaryAttempts {
		t.Errorf("AdversaryDenials = %d, want %d (all attempts denied)",
			metrics.AdversaryDenials, metrics.AdversaryAttempts)
	}

	// Denial rate should be 100%
	if metrics.AdversaryDenialRate != 1.0 {
		t.Errorf("AdversaryDenialRate = %v, want 1.0", metrics.AdversaryDenialRate)
	}

	t.Logf("Run completed in %v (story time: %v)",
		metrics.Duration, metrics.StoryDuration)
}

func TestSimulator_TimeScaling(t *testing.T) {
	ai := &scenarios.AlienInvasion{}

	testCases := []struct {
		name        string
		timeScale   float64
		maxDuration time.Duration
	}{
		{"10x", 10.0, 8 * time.Hour},
		{"100x", 100.0, 45 * time.Minute},
		{"1000x", 1000.0, 5 * time.Minute},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig(ai)
			config.TimeScale = tc.timeScale
			config.EnableMesh = false

			sim, err := NewSimulator(config)
			if err != nil {
				t.Fatalf("NewSimulator() error = %v", err)
			}

			ctx := context.Background()
			start := time.Now()
			metrics, err := sim.Run(ctx)
			elapsed := time.Since(start)

			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			// Verify scaled duration is reasonable
			expectedDuration := story.ScaledDuration(ai.Duration(), tc.timeScale)

			t.Logf("TimeScale %.0fx: expected %v, actual %v",
				tc.timeScale, expectedDuration, elapsed)

			// Actual duration should be much faster than story duration
			if elapsed > ai.Duration()/time.Duration(tc.timeScale)*2 {
				t.Errorf("Elapsed time %v too long for timeScale %.0fx", elapsed, tc.timeScale)
			}

			if metrics.StoryDuration != ai.Duration() {
				t.Errorf("Story duration = %v, want %v", metrics.StoryDuration, ai.Duration())
			}
		})
	}
}

func TestSimulator_GetProgress(t *testing.T) {
	ai := &scenarios.AlienInvasion{}
	config := DefaultConfig(ai)
	config.TimeScale = 100.0

	sim, err := NewSimulator(config)
	if err != nil {
		t.Fatalf("NewSimulator() error = %v", err)
	}

	ctx := context.Background()
	if err := sim.GenerateScenario(ctx); err != nil {
		t.Fatalf("GenerateScenario() error = %v", err)
	}

	// Simulate starting the scenario
	sim.startTime = time.Now()

	// Wait a bit to have some elapsed time
	time.Sleep(100 * time.Millisecond)

	progress := sim.GetProgress()

	if progress.RealTimeElapsed == 0 {
		t.Error("RealTimeElapsed should be > 0")
	}

	if progress.StoryTimeElapsed == 0 {
		t.Error("StoryTimeElapsed should be > 0")
	}

	if progress.StoryDuration != ai.Duration() {
		t.Errorf("StoryDuration = %v, want %v", progress.StoryDuration, ai.Duration())
	}

	if progress.TasksTotal != len(sim.tasks) {
		t.Errorf("TasksTotal = %d, want %d", progress.TasksTotal, len(sim.tasks))
	}

	// Story time should be scaled
	expectedStoryTime := time.Duration(float64(progress.RealTimeElapsed) * config.TimeScale)
	if progress.StoryTimeElapsed < expectedStoryTime/2 || progress.StoryTimeElapsed > expectedStoryTime*2 {
		t.Errorf("StoryTimeElapsed = %v, expected ~%v (scale %.0fx)",
			progress.StoryTimeElapsed, expectedStoryTime, config.TimeScale)
	}

	t.Logf("Progress: %.1f%% complete (%v story time in %v real time)",
		progress.PercentComplete, progress.StoryTimeElapsed, progress.RealTimeElapsed)
}

func TestSimulator_ConfigValidation(t *testing.T) {
	// Test with nil story
	config := SimulatorConfig{
		Story: nil,
	}

	_, err := NewSimulator(config)
	if err == nil {
		t.Error("Expected error with nil story")
	}
}

func TestSimulator_DisabledFeatures(t *testing.T) {
	ai := &scenarios.AlienInvasion{}
	config := DefaultConfig(ai)
	config.EnableAdversary = false
	config.EnableWorkflows = false
	config.EnableMesh = false

	sim, err := NewSimulator(config)
	if err != nil {
		t.Fatalf("NewSimulator() error = %v", err)
	}

	if len(sim.adversaryGen) != 0 {
		t.Error("Should not have adversary generators when disabled")
	}

	if sim.meshOrch != nil {
		t.Error("Should not have mesh orchestrator when disabled")
	}

	ctx := context.Background()
	if err := sim.GenerateScenario(ctx); err != nil {
		t.Fatalf("GenerateScenario() error = %v", err)
	}

	// Should still have tasks
	if len(sim.tasks) == 0 {
		t.Error("Should have tasks even with features disabled")
	}

	// Should not have adversary attempts
	totalAttempts := 0
	for _, attempts := range sim.attempts {
		totalAttempts += len(attempts)
	}
	if totalAttempts != 0 {
		t.Errorf("Should have 0 adversary attempts when disabled, got %d", totalAttempts)
	}

	// Should not have workflows
	if len(sim.workflows) != 0 {
		t.Errorf("Should have 0 workflows when disabled, got %d", len(sim.workflows))
	}
}

func TestSimulator_WorkflowFiltering(t *testing.T) {
	ai := &scenarios.AlienInvasion{}
	config := DefaultConfig(ai)
	config.WorkflowTestsEnabled = map[WorkflowType]bool{
		WorkflowDeletion:   true,
		WorkflowExpiration: false, // Disable expiration tests
		WorkflowQuota:      true,
		// Others not specified = default to false
	}

	sim, err := NewSimulator(config)
	if err != nil {
		t.Fatalf("NewSimulator() error = %v", err)
	}

	ctx := context.Background()
	if err := sim.GenerateScenario(ctx); err != nil {
		t.Fatalf("GenerateScenario() error = %v", err)
	}

	// Check workflow types
	hasExpiration := false
	for _, wf := range sim.workflows {
		if wf.Type == WorkflowExpiration {
			hasExpiration = true
			break
		}
	}

	if hasExpiration {
		t.Error("Should not have expiration workflows when disabled")
	}

	t.Logf("Generated %d workflows with filtering enabled", len(sim.workflows))
}

func TestDefaultConfig(t *testing.T) {
	ai := &scenarios.AlienInvasion{}
	config := DefaultConfig(ai)

	if config.Story != ai {
		t.Error("Story not set")
	}

	if config.TimeScale != 1.0 {
		t.Errorf("TimeScale = %v, want 1.0", config.TimeScale)
	}

	if config.EnableMesh {
		t.Error("EnableMesh should be false by default")
	}

	if !config.EnableAdversary {
		t.Error("EnableAdversary should be true by default")
	}

	if !config.EnableWorkflows {
		t.Error("EnableWorkflows should be true by default")
	}

	if config.MaxConcurrentUploads == 0 {
		t.Error("MaxConcurrentUploads should be set")
	}

	// All workflow types should be enabled by default
	for _, wfType := range []WorkflowType{
		WorkflowDeletion, WorkflowExpiration, WorkflowPermissions,
		WorkflowQuota, WorkflowRetention,
	} {
		if !config.WorkflowTestsEnabled[wfType] {
			t.Errorf("Workflow type %v should be enabled by default", wfType)
		}
	}
}

func TestScenarioSummary_String(t *testing.T) {
	ai := &scenarios.AlienInvasion{}
	config := DefaultConfig(ai)

	sim, err := NewSimulator(config)
	if err != nil {
		t.Fatalf("NewSimulator() error = %v", err)
	}

	ctx := context.Background()
	if err := sim.GenerateScenario(ctx); err != nil {
		t.Fatalf("GenerateScenario() error = %v", err)
	}

	summary := sim.GetScenarioSummary()
	summaryStr := summary.String()

	if summaryStr == "" {
		t.Error("Summary string should not be empty")
	}

	// Should contain key information
	if !contains(summaryStr, ai.Name()) {
		t.Error("Summary should contain story name")
	}

	if !contains(summaryStr, "Characters:") {
		t.Error("Summary should contain character count")
	}

	t.Logf("Summary:\n%s", summaryStr)
}

func TestGenerateScenario_Idempotent(t *testing.T) {
	ai := &scenarios.AlienInvasion{}
	config := DefaultConfig(ai)
	config.TimeScale = 100.0
	config.EnableWorkflows = true

	sim, err := NewSimulator(config)
	if err != nil {
		t.Fatalf("NewSimulator() error = %v", err)
	}

	ctx := context.Background()

	// First call generates scenario
	if err := sim.GenerateScenario(ctx); err != nil {
		t.Fatalf("GenerateScenario() first call error = %v", err)
	}

	tasksAfterFirst := len(sim.tasks)
	workflowsAfterFirst := len(sim.workflows)
	adversaryAfterFirst := 0
	for _, attempts := range sim.attempts {
		adversaryAfterFirst += len(attempts)
	}

	if tasksAfterFirst == 0 {
		t.Fatal("expected tasks to be generated")
	}

	// Second call should be a no-op
	if err := sim.GenerateScenario(ctx); err != nil {
		t.Fatalf("GenerateScenario() second call error = %v", err)
	}

	if len(sim.tasks) != tasksAfterFirst {
		t.Errorf("tasks doubled: got %d after second call, want %d", len(sim.tasks), tasksAfterFirst)
	}
	if len(sim.workflows) != workflowsAfterFirst {
		t.Errorf("workflows doubled: got %d after second call, want %d", len(sim.workflows), workflowsAfterFirst)
	}
	adversaryAfterSecond := 0
	for _, attempts := range sim.attempts {
		adversaryAfterSecond += len(attempts)
	}
	if adversaryAfterSecond != adversaryAfterFirst {
		t.Errorf("adversary attempts doubled: got %d after second call, want %d", adversaryAfterSecond, adversaryAfterFirst)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
