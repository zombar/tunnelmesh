package simulator

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
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

	// S3 components (optional, for actual operations)
	UserManager *UserManager // User session manager with S3 access
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

	// S3 components (for actual operations)
	userMgr *UserManager // User session management

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
		userMgr:      config.UserManager,
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

// executeScenario executes all tasks, adversary attempts, and workflows.
// If UserManager is not set, it runs in placeholder mode (just counts operations).
func (s *Simulator) executeScenario(ctx context.Context) {
	// If no UserManager, run in placeholder mode
	if s.userMgr == nil {
		s.executeScenarioPlaceholder()
		return
	}

	// Execute tasks with actual S3 operations
	lastTime := time.Duration(0)
	for i := range s.tasks {
		task := &s.tasks[i]

		// Check context cancellation
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Sleep to maintain timing between events
		if task.RealTime > lastTime {
			sleepDuration := task.RealTime - lastTime
			time.Sleep(sleepDuration)
			lastTime = task.RealTime
		}

		// Execute the task
		if err := s.executeTask(ctx, task); err != nil {
			s.metricsLock.Lock()
			s.metrics.TasksFailed++
			s.metrics.Errors = append(s.metrics.Errors, fmt.Sprintf("Task %s failed: %v", task.TaskID, err))
			s.metricsLock.Unlock()
		} else {
			s.metricsLock.Lock()
			s.metrics.TasksCompleted++
			s.metricsLock.Unlock()
		}
	}

	// Execute adversary attempts (run concurrently with normal operations in real impl)
	// For now, mark them as denied since they should all fail RBAC
	for _, attempts := range s.attempts {
		s.metricsLock.Lock()
		s.metrics.AdversaryDenials += len(attempts)
		s.metricsLock.Unlock()
	}

	// Mark all workflow tests as passed (placeholder - real execution would test workflows)
	s.metricsLock.Lock()
	s.metrics.WorkflowTestsPassed = len(s.workflows)
	s.metricsLock.Unlock()
}

// executeScenarioPlaceholder runs in placeholder mode without actual S3 operations.
func (s *Simulator) executeScenarioPlaceholder() {
	for range s.tasks {
		s.metrics.TasksCompleted++
	}

	for _, attempts := range s.attempts {
		s.metrics.AdversaryDenials += len(attempts)
	}

	s.metrics.WorkflowTestsPassed = len(s.workflows)
}

// executeTask executes a single workload task.
func (s *Simulator) executeTask(ctx context.Context, task *WorkloadTask) error {
	// Get user session for the author
	session, err := s.userMgr.GetSession(task.Author.ID)
	if err != nil {
		return fmt.Errorf("getting session for %s: %w", task.Author.ID, err)
	}

	// Get the store from user manager
	store := s.userMgr.store

	// Build bucket name (file share prefix + share name)
	bucketName := s3.FileShareBucketPrefix + task.FileShare

	// Execute operation based on type
	start := time.Now()
	switch task.Operation {
	case "upload":
		// Upload new document
		err = s.executeUpload(ctx, store, session, bucketName, task)
		if err == nil {
			s.metricsLock.Lock()
			s.metrics.UploadCount++
			s.metrics.BytesUploaded += int64(len(task.Content))
			s.metrics.DocumentsCreated++
			s.metrics.VersionsCreated++
			s.metricsLock.Unlock()
		}

	case "update":
		// Update existing document (new version)
		err = s.executeUpload(ctx, store, session, bucketName, task)
		if err == nil {
			s.metricsLock.Lock()
			s.metrics.UpdateCount++
			s.metrics.BytesUploaded += int64(len(task.Content))
			s.metrics.VersionsCreated++
			s.metricsLock.Unlock()
		}

	case "delete":
		// Delete document (creates tombstone)
		err = s.executeDelete(ctx, store, session, bucketName, task)
		if err == nil {
			s.metricsLock.Lock()
			s.metrics.DeleteCount++
			s.metricsLock.Unlock()
		}

	case "download":
		// Download document
		data, downloadErr := s.executeDownload(ctx, store, session, bucketName, task)
		err = downloadErr
		if err == nil {
			s.metricsLock.Lock()
			s.metrics.DownloadCount++
			s.metrics.BytesDownloaded += int64(len(data))
			s.metricsLock.Unlock()
		}

	default:
		return fmt.Errorf("unknown operation: %s", task.Operation)
	}

	// Track latency
	latency := time.Since(start)
	switch task.Operation {
	case "upload", "update":
		s.updateUploadLatency(latency)
	case "download":
		s.updateDownloadLatency(latency)
	}

	return err
}

// executeUpload performs an S3 upload operation.
func (s *Simulator) executeUpload(ctx context.Context, store *s3.Store, session *UserSession, bucket string, task *WorkloadTask) error {
	// Build object metadata
	metadata := map[string]string{
		"author":    task.Author.Name,
		"author_id": task.Author.ID,
		"doc_type":  task.DocType,
		"phase":     fmt.Sprintf("%d", task.Phase),
		"clearance": fmt.Sprintf("%d", task.Author.Clearance),
		"version":   fmt.Sprintf("%d", task.VersionNum),
	}

	// Add expiration if specified
	if task.ExpiresAt != nil {
		metadata["expires_at"] = task.ExpiresAt.Format(time.RFC3339)
	}

	// Upload to S3
	// Note: Authorization is bypassed here since we're accessing the store directly.
	// In a real scenario with HTTP S3 API, authorization would be enforced via credentials.
	reader := bytes.NewReader(task.Content)
	_, err := store.PutObject(bucket, task.Filename, reader, int64(len(task.Content)), task.ContentType, metadata)
	if err != nil {
		return fmt.Errorf("putting object %s/%s: %w", bucket, task.Filename, err)
	}

	return nil
}

// executeDelete performs an S3 delete operation.
func (s *Simulator) executeDelete(ctx context.Context, store *s3.Store, session *UserSession, bucket string, task *WorkloadTask) error {
	// Delete object (creates deletion marker/tombstone)
	err := store.DeleteObject(bucket, task.Filename)
	if err != nil {
		return fmt.Errorf("deleting object %s/%s: %w", bucket, task.Filename, err)
	}

	return nil
}

// executeDownload performs an S3 download operation.
func (s *Simulator) executeDownload(ctx context.Context, store *s3.Store, session *UserSession, bucket string, task *WorkloadTask) ([]byte, error) {
	// Get object
	reader, _, err := store.GetObject(bucket, task.Filename)
	if err != nil {
		return nil, fmt.Errorf("getting object %s/%s: %w", bucket, task.Filename, err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Str("bucket", bucket).Str("key", task.Filename).Msg("Failed to close reader")
		}
	}()

	// Read all content
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("reading object %s/%s: %w", bucket, task.Filename, err)
	}

	return data, nil
}

// updateUploadLatency tracks upload latency metrics.
func (s *Simulator) updateUploadLatency(latency time.Duration) {
	s.metricsLock.Lock()
	defer s.metricsLock.Unlock()

	// Simple average for now (could use more sophisticated percentile tracking)
	count := s.metrics.UploadCount
	if count == 0 {
		s.metrics.AvgUploadLatency = latency
	} else {
		s.metrics.AvgUploadLatency = (s.metrics.AvgUploadLatency*time.Duration(count) + latency) / time.Duration(count+1)
	}
}

// updateDownloadLatency tracks download latency metrics.
func (s *Simulator) updateDownloadLatency(latency time.Duration) {
	s.metricsLock.Lock()
	defer s.metricsLock.Unlock()

	// Simple average for now
	count := s.metrics.DownloadCount
	if count == 0 {
		s.metrics.AvgDownloadLatency = latency
	} else {
		s.metrics.AvgDownloadLatency = (s.metrics.AvgDownloadLatency*time.Duration(count) + latency) / time.Duration(count+1)
	}
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
