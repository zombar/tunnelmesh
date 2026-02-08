package simulator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
)

// SimulatorConfig configures the simulator behavior.
type SimulatorConfig struct {
	// Story to execute
	Story story.Story

	// Time scaling factor (1.0 = realtime, 100.0 = 100x faster)
	TimeScale float64

	// Feature flags
	EnableMesh      bool // Use mesh orchestration (Docker)
	EnableAdversary bool // Run adversary simulation
	EnableWorkflows bool // Run workflow tests

	// Overrides
	QuotaOverrideMB      int64                 // Override default quotas (0 = use story defaults)
	ExpiryOverride       time.Duration         // Override document expiries (0 = use story defaults)
	AdversaryAttempts    int                   // Number of adversary attempts to generate
	WorkflowTestsEnabled map[WorkflowType]bool // Which workflow types to test

	// Concurrency
	MaxConcurrentUploads int // Max parallel uploads (0 = unlimited)
}

// DefaultConfig returns a simulator config with sensible defaults.
func DefaultConfig(s story.Story) SimulatorConfig {
	return SimulatorConfig{
		Story:                s,
		TimeScale:            1.0,
		EnableMesh:           false, // Disabled by default (requires Docker)
		EnableAdversary:      true,
		EnableWorkflows:      true,
		AdversaryAttempts:    50,
		MaxConcurrentUploads: 10,
		WorkflowTestsEnabled: map[WorkflowType]bool{
			WorkflowDeletion:    true,
			WorkflowExpiration:  true,
			WorkflowPermissions: true,
			WorkflowQuota:       true,
			WorkflowRetention:   true,
		},
	}
}

// Simulator orchestrates the entire stress test scenario.
type Simulator struct {
	config SimulatorConfig

	// Component generators
	workloadGen  *WorkloadGenerator
	adversaryGen map[string]*AdversarySimulator // Map character ID to simulator
	workflowGen  *WorkflowGenerator
	meshOrch     *MeshOrchestrator

	// Execution state
	startTime time.Time
	tasks     []WorkloadTask
	attempts  map[string][]AdversaryAttempt // Map character ID to attempts
	workflows []WorkflowTest

	// Metrics
	metrics     *SimulatorMetrics
	metricsLock sync.RWMutex
}

// SimulatorMetrics tracks execution metrics.
type SimulatorMetrics struct {
	// Timing
	StartTime     time.Time
	EndTime       time.Time
	Duration      time.Duration
	StoryDuration time.Duration

	// Operations
	TasksGenerated int
	TasksCompleted int
	TasksFailed    int
	UploadCount    int
	UpdateCount    int
	DeleteCount    int
	DownloadCount  int

	// Data
	BytesUploaded    int64
	BytesDownloaded  int64
	DocumentsCreated int
	VersionsCreated  int

	// Performance
	AvgUploadLatency   time.Duration
	AvgDownloadLatency time.Duration
	P95UploadLatency   time.Duration
	P99UploadLatency   time.Duration

	// Storage efficiency
	LogicalBytes       int64
	PhysicalBytes      int64
	DeduplicationRatio float64
	CompressionRatio   float64

	// Adversary
	AdversaryAttempts   int
	AdversaryDenials    int
	AdversaryDenialRate float64

	// Mesh
	PeerJoins    int
	PeerLeaves   int
	MeshDowntime time.Duration

	// Workflows
	WorkflowTestsRun    int
	WorkflowTestsPassed int
	WorkflowTestsFailed int

	// Errors
	Errors []string
}

// NewSimulator creates a new simulator.
func NewSimulator(config SimulatorConfig) (*Simulator, error) {
	if config.Story == nil {
		return nil, fmt.Errorf("story is required")
	}

	if config.TimeScale <= 0 {
		config.TimeScale = 1.0
	}

	sim := &Simulator{
		config:       config,
		workloadGen:  NewWorkloadGenerator(config.Story, config.TimeScale),
		adversaryGen: make(map[string]*AdversarySimulator),
		workflowGen:  NewWorkflowGenerator(config.Story, config.TimeScale),
		attempts:     make(map[string][]AdversaryAttempt),
		metrics: &SimulatorMetrics{
			StoryDuration: config.Story.Duration(),
		},
	}

	// Initialize adversary simulators for dark characters
	if config.EnableAdversary {
		for _, char := range config.Story.Characters() {
			if char.IsAdversary() {
				// Use insider profile by default (can be customized per character)
				profile := DefaultBehaviorProfiles["insider"]
				sim.adversaryGen[char.ID] = NewAdversarySimulator(char, profile, config.TimeScale)
			}
		}
	}

	// Initialize mesh orchestrator if enabled
	if config.EnableMesh {
		// For now, use mock controller (Docker controller will be implemented later)
		controller := NewMockMeshController()
		sim.meshOrch = NewMeshOrchestrator(controller, config.TimeScale)
	}

	return sim, nil
}

// GenerateScenario generates all tasks, attempts, and workflows for the scenario.
func (s *Simulator) GenerateScenario(ctx context.Context) error {
	var err error

	// Generate workload tasks
	s.tasks, err = s.workloadGen.GenerateWorkload(ctx)
	if err != nil {
		return fmt.Errorf("generating workload: %w", err)
	}
	s.metrics.TasksGenerated = len(s.tasks)

	// Generate adversary attempts
	if s.config.EnableAdversary {
		for charID, advSim := range s.adversaryGen {
			attempts, err := advSim.GenerateAttempts(ctx, s.config.Story.Duration(), s.config.AdversaryAttempts)
			if err != nil {
				return fmt.Errorf("generating adversary attempts for %s: %w", charID, err)
			}
			s.attempts[charID] = attempts
			s.metrics.AdversaryAttempts += len(attempts)
		}
	}

	// Generate workflow tests
	if s.config.EnableWorkflows {
		allWorkflows, err := s.workflowGen.GenerateAllWorkflows(ctx)
		if err != nil {
			return fmt.Errorf("generating workflows: %w", err)
		}

		// Filter based on enabled workflow types
		for _, wf := range allWorkflows {
			if enabled, ok := s.config.WorkflowTestsEnabled[wf.Type]; !ok || enabled {
				s.workflows = append(s.workflows, wf)
			}
		}
		s.metrics.WorkflowTestsRun = len(s.workflows)
	}

	return nil
}

// Run executes the entire scenario.
// This is a placeholder that shows the orchestration flow.
// Actual S3 operations will be implemented by the integration layer.
func (s *Simulator) Run(ctx context.Context) (*SimulatorMetrics, error) {
	s.metrics.StartTime = time.Now()
	s.startTime = time.Now()

	// Generate scenario
	if err := s.GenerateScenario(ctx); err != nil {
		return nil, fmt.Errorf("generating scenario: %w", err)
	}

	// Handle mesh orchestration if enabled
	if s.config.EnableMesh {
		if err := s.orchestrateMesh(ctx); err != nil {
			return nil, fmt.Errorf("mesh orchestration: %w", err)
		}
	}

	// Execute tasks, attempts, and workflows
	// This is where the actual S3 operations would happen
	// For now, we just track the metrics
	s.executeScenario(ctx)

	s.metrics.EndTime = time.Now()
	s.metrics.Duration = time.Since(s.startTime)

	// Calculate derived metrics
	s.calculateDerivedMetrics()

	return s.metrics, nil
}

// orchestrateMesh handles peer join/leave based on character timelines.
func (s *Simulator) orchestrateMesh(ctx context.Context) error {
	if s.meshOrch == nil {
		return nil
	}

	characters := s.config.Story.Characters()

	// Start peers that should be online at T=0
	for _, char := range characters {
		if char.JoinTime == 0 {
			if err := s.meshOrch.JoinPeer(ctx, char); err != nil {
				return fmt.Errorf("joining peer %s at T=0: %w", char.ID, err)
			}
			s.metrics.PeerJoins++
		}
	}

	// Schedule future join/leave events
	// In a real implementation, these would be scheduled as goroutines
	// For now, we just track the events

	return nil
}

// executeScenario is a placeholder for executing the scenario.
// The actual implementation will:
// 1. Sort all events (tasks, attempts, workflows) by real time
// 2. Execute them in order, sleeping between events
// 3. Handle mesh join/leave events
// 4. Collect metrics for each operation
func (s *Simulator) executeScenario(ctx context.Context) {
	// Placeholder implementation - just mark tasks as completed
	for range s.tasks {
		s.metrics.TasksCompleted++
		// In real implementation, this would execute the S3 operation
	}

	// Mark all adversary attempts as denied (expected behavior)
	for _, attempts := range s.attempts {
		s.metrics.AdversaryDenials += len(attempts)
	}

	// Mark all workflow tests as passed (placeholder)
	s.metrics.WorkflowTestsPassed = len(s.workflows)
}

// calculateDerivedMetrics computes metrics that depend on collected data.
func (s *Simulator) calculateDerivedMetrics() {
	// Adversary denial rate
	if s.metrics.AdversaryAttempts > 0 {
		s.metrics.AdversaryDenialRate = float64(s.metrics.AdversaryDenials) / float64(s.metrics.AdversaryAttempts)
	}

	// Deduplication ratio
	if s.metrics.PhysicalBytes > 0 {
		s.metrics.DeduplicationRatio = float64(s.metrics.LogicalBytes) / float64(s.metrics.PhysicalBytes)
	}
}

// GetMetrics returns current simulator metrics.
func (s *Simulator) GetMetrics() *SimulatorMetrics {
	s.metricsLock.RLock()
	defer s.metricsLock.RUnlock()

	// Return a copy
	metrics := *s.metrics
	return &metrics
}

// GetProgress returns current progress through the scenario.
func (s *Simulator) GetProgress() SimulatorProgress {
	s.metricsLock.RLock()
	defer s.metricsLock.RUnlock()

	elapsed := time.Since(s.startTime)
	storyElapsed := time.Duration(float64(elapsed) * s.config.TimeScale)

	progress := SimulatorProgress{
		RealTimeElapsed:  elapsed,
		StoryTimeElapsed: storyElapsed,
		StoryDuration:    s.config.Story.Duration(),
		TasksCompleted:   s.metrics.TasksCompleted,
		TasksTotal:       s.metrics.TasksGenerated,
		OnlinePeers:      []string{},
	}

	// Calculate percentage
	if s.config.Story.Duration() > 0 {
		progress.PercentComplete = float64(storyElapsed) / float64(s.config.Story.Duration()) * 100
	}

	// Get online peers
	if s.meshOrch != nil {
		for _, char := range s.meshOrch.GetOnlinePeers() {
			progress.OnlinePeers = append(progress.OnlinePeers, char.Name)
		}
	}

	return progress
}

// SimulatorProgress tracks execution progress.
type SimulatorProgress struct {
	RealTimeElapsed  time.Duration // Actual time elapsed
	StoryTimeElapsed time.Duration // Scaled story time elapsed
	StoryDuration    time.Duration // Total story duration
	PercentComplete  float64       // Percentage complete (0-100)
	TasksCompleted   int           // Tasks completed
	TasksTotal       int           // Total tasks
	OnlinePeers      []string      // Currently online peer names
}

// GetScenarioSummary returns a summary of the generated scenario.
func (s *Simulator) GetScenarioSummary() ScenarioSummary {
	summary := ScenarioSummary{
		StoryName:         s.config.Story.Name(),
		StoryDescription:  s.config.Story.Description(),
		StoryDuration:     s.config.Story.Duration(),
		TimeScale:         s.config.TimeScale,
		RealDuration:      story.ScaledDuration(s.config.Story.Duration(), s.config.TimeScale),
		Characters:        len(s.config.Story.Characters()),
		Departments:       len(s.config.Story.Departments()),
		Tasks:             len(s.tasks),
		AdversaryAttempts: s.metrics.AdversaryAttempts,
		WorkflowTests:     len(s.workflows),
	}

	// Count document types
	docTypes := make(map[string]int)
	for _, task := range s.tasks {
		if task.Operation == "upload" {
			docTypes[task.DocType]++
		}
	}
	summary.DocumentTypes = len(docTypes)
	summary.DocumentsGenerated = len(docTypes)

	// Count character roles
	goodChars := 0
	darkChars := 0
	for _, char := range s.config.Story.Characters() {
		if char.IsAdversary() {
			darkChars++
		} else {
			goodChars++
		}
	}
	summary.GoodCharacters = goodChars
	summary.DarkCharacters = darkChars

	return summary
}

// ScenarioSummary provides an overview of the scenario.
type ScenarioSummary struct {
	StoryName          string
	StoryDescription   string
	StoryDuration      time.Duration
	TimeScale          float64
	RealDuration       time.Duration
	Characters         int
	GoodCharacters     int
	DarkCharacters     int
	Departments        int
	DocumentTypes      int
	DocumentsGenerated int
	Tasks              int
	AdversaryAttempts  int
	WorkflowTests      int
}

// String returns a human-readable summary.
func (s ScenarioSummary) String() string {
	return fmt.Sprintf(
		"Scenario: %s\n"+
			"Description: %s\n"+
			"Story Duration: %v (%.1fx speed = %v real time)\n"+
			"Characters: %d (%d good, %d adversary)\n"+
			"Departments: %d\n"+
			"Document Types: %d\n"+
			"Tasks: %d\n"+
			"Adversary Attempts: %d\n"+
			"Workflow Tests: %d",
		s.StoryName,
		s.StoryDescription,
		s.StoryDuration,
		s.TimeScale,
		s.RealDuration,
		s.Characters,
		s.GoodCharacters,
		s.DarkCharacters,
		s.Departments,
		s.DocumentTypes,
		s.Tasks,
		s.AdversaryAttempts,
		s.WorkflowTests,
	)
}
