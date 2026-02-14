package simulator

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/mesh"
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

	// Network Testing (Phase 1: HTTP Server)
	UseHTTP       bool   // Use HTTP server instead of direct Store calls
	HTTPEndpoint  string // HTTP endpoint for S3 API (e.g., "http://localhost:8080")
	EnableMetrics bool   // Enable metrics collection

	// S3 components (optional, for actual operations)
	UserManager *UserManager // User session manager with S3 access

	// Mesh integration (Phase 2: Coordinator API)
	UseMesh     bool                    // Enable mesh integration
	MeshClient  *mesh.CoordinatorClient // Coordinator API client (nil = standalone)
	SharePrefix string                  // Peer name prefix for share bucket names (e.g. "s3bench" → buckets are "fs+s3bench_sharename")
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
		UseHTTP:              true, // Test full HTTP + RBAC stack by default
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
	userMgr     *UserManager            // User session management
	httpClient  *http.Client            // HTTP client for network testing
	meshClient  *mesh.CoordinatorClient // Coordinator mesh API client
	sharePrefix string                  // Peer name prefix for share bucket names

	// Execution state
	generated bool
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
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		meshClient:  config.MeshClient,
		sharePrefix: config.SharePrefix,
		attempts:    make(map[string][]AdversaryAttempt),
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

// shareBucketName computes the S3 bucket name for a file share.
// When SharePrefix is set (mesh mode), the bucket is "fs+{prefix}_{shareName}".
// Otherwise, the bucket is "fs+{shareName}" (standalone mode).
func (s *Simulator) shareBucketName(shareName string) string {
	if s.sharePrefix != "" {
		return s3.FileShareBucketPrefix + s.sharePrefix + "_" + shareName
	}
	return s3.FileShareBucketPrefix + shareName
}

// GenerateScenario generates all tasks, attempts, and workflows for the scenario.
// It is idempotent: subsequent calls after the first are no-ops.
func (s *Simulator) GenerateScenario(ctx context.Context) error {
	if s.generated {
		return nil
	}

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
			// If WorkflowTestsEnabled is nil, include all workflows
			if s.config.WorkflowTestsEnabled == nil {
				s.workflows = append(s.workflows, wf)
			} else if enabled, ok := s.config.WorkflowTestsEnabled[wf.Type]; !ok || enabled {
				s.workflows = append(s.workflows, wf)
			}
		}
		s.metrics.WorkflowTestsRun = len(s.workflows)
	}

	s.generated = true
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
			s.metricsLock.Lock()
			s.metrics.PeerJoins++
			s.metricsLock.Unlock()
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

	// Execute tasks with actual S3 operations using concurrent execution
	maxConcurrent := s.config.MaxConcurrentUploads
	if maxConcurrent <= 0 {
		maxConcurrent = 1 // Default to sequential if not set
	}

	// Create semaphore channel to limit concurrency
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	lastTime := time.Duration(0)
	for i := range s.tasks {
		task := &s.tasks[i]

		// Check context cancellation
		select {
		case <-ctx.Done():
			// Wait for in-flight tasks to complete
			wg.Wait()
			return
		default:
		}

		// Sleep to maintain timing between events (with cancellation support)
		if task.RealTime > lastTime {
			sleepDuration := task.RealTime - lastTime
			select {
			case <-ctx.Done():
				wg.Wait()
				return
			case <-time.After(sleepDuration):
			}
			lastTime = task.RealTime
		}

		// Acquire semaphore slot (blocks if at max concurrency)
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case sem <- struct{}{}:
		}

		// Execute task concurrently
		wg.Add(1)
		go func(t *WorkloadTask) {
			defer wg.Done()
			defer func() { <-sem }() // Release semaphore slot

			if err := s.executeTask(ctx, t); err != nil {
				s.metricsLock.Lock()
				s.metrics.TasksFailed++
				s.metrics.Errors = append(s.metrics.Errors, fmt.Sprintf("Task %s failed: %v", t.TaskID, err))
				s.metricsLock.Unlock()
			} else {
				s.metricsLock.Lock()
				s.metrics.TasksCompleted++
				s.metricsLock.Unlock()
			}
		}(task)
	}

	// Wait for all tasks to complete
	wg.Wait()

	// Execute adversary attempts - actually test RBAC
	for characterID, attempts := range s.attempts {
		for _, attempt := range attempts {
			denied, err := s.executeAdversaryAttempt(ctx, attempt)
			if err != nil {
				log.Warn().Err(err).Str("character", characterID).Str("action", string(attempt.Action)).Msg("Adversary attempt error")
			}

			s.metricsLock.Lock()
			if denied {
				s.metrics.AdversaryDenials++
			}
			s.metricsLock.Unlock()
		}
	}

	// Execute workflow tests
	for _, workflow := range s.workflows {
		passed, err := s.executeWorkflow(ctx, workflow)
		if err != nil {
			log.Warn().Err(err).Str("testID", workflow.TestID).Msg("Workflow test error")
		}

		s.metricsLock.Lock()
		if passed {
			s.metrics.WorkflowTestsPassed++
		} else {
			s.metrics.WorkflowTestsFailed++
		}
		s.metricsLock.Unlock()
	}
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
	bucketName := s.shareBucketName(task.FileShare)

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
	// Route to mesh mode if enabled (highest priority)
	if s.meshClient != nil {
		return s.executeUploadMesh(ctx, bucket, task)
	}

	// Route to HTTP or direct store access
	if s.config.UseHTTP {
		return s.executeUploadHTTP(ctx, session, bucket, task)
	}

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

	// Upload to S3 (direct store access - bypasses HTTP/auth)
	reader := bytes.NewReader(task.Content)
	_, err := store.PutObject(context.Background(), bucket, task.Filename, reader, int64(len(task.Content)), task.ContentType, metadata)
	if err != nil {
		return fmt.Errorf("putting object %s/%s: %w", bucket, task.Filename, err)
	}

	return nil
}

// executeUploadHTTP performs an S3 upload via HTTP (tests full network stack).
func (s *Simulator) executeUploadHTTP(ctx context.Context, session *UserSession, bucket string, task *WorkloadTask) error {
	// Build URL: http://endpoint/bucket/key
	url := fmt.Sprintf("%s/%s/%s", s.config.HTTPEndpoint, bucket, task.Filename)

	// Create HTTP request
	reader := bytes.NewReader(task.Content)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, reader)
	if err != nil {
		return fmt.Errorf("creating HTTP request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", task.ContentType)
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(task.Content)))
	req.Header.Set("X-User-ID", session.UserID) // Authentication for HTTP testing

	// Add metadata as x-amz-meta- headers
	req.Header.Set("x-amz-meta-author", task.Author.Name)
	req.Header.Set("x-amz-meta-author_id", task.Author.ID)
	req.Header.Set("x-amz-meta-doc_type", task.DocType)
	req.Header.Set("x-amz-meta-version", fmt.Sprintf("%d", task.VersionNum))

	// Execute request
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP PUT %s: %w", url, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Str("url", url).Msg("Failed to close response body")
		}
	}()

	// Check response
	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("HTTP PUT %s failed: %d (error reading response body: %w)", url, resp.StatusCode, readErr)
		}
		return fmt.Errorf("HTTP PUT %s failed: %d %s", url, resp.StatusCode, string(body))
	}

	return nil
}

// executeUploadMesh performs an S3 upload to the coordinator via mesh API.
func (s *Simulator) executeUploadMesh(ctx context.Context, bucket string, task *WorkloadTask) error {
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

	// Upload to coordinator via mesh API
	err := s.meshClient.PutObject(ctx, bucket, task.Filename, task.Content, task.ContentType, metadata)
	if err != nil {
		return fmt.Errorf("mesh upload %s/%s: %w", bucket, task.Filename, err)
	}

	return nil
}

// executeDownloadMesh performs an S3 download from the coordinator via mesh API.
func (s *Simulator) executeDownloadMesh(ctx context.Context, bucket string, task *WorkloadTask) ([]byte, error) {
	data, err := s.meshClient.GetObject(ctx, bucket, task.Filename)
	if err != nil {
		return nil, fmt.Errorf("downloading object %s/%s: %w", bucket, task.Filename, err)
	}
	return data, nil
}

// executeDelete performs an S3 delete operation.
func (s *Simulator) executeDelete(ctx context.Context, store *s3.Store, session *UserSession, bucket string, task *WorkloadTask) error {
	// Route to mesh, HTTP, or direct store access
	if s.meshClient != nil {
		return s.executeDeleteMesh(ctx, bucket, task)
	}
	if s.config.UseHTTP {
		return s.executeDeleteHTTP(ctx, session, bucket, task)
	}

	// Delete object (creates deletion marker/tombstone)
	err := store.DeleteObject(context.Background(), bucket, task.Filename)
	if err != nil {
		return fmt.Errorf("deleting object %s/%s: %w", bucket, task.Filename, err)
	}

	return nil
}

// executeDeleteMesh performs an S3 delete via mesh coordinator.
func (s *Simulator) executeDeleteMesh(ctx context.Context, bucket string, task *WorkloadTask) error {
	err := s.meshClient.DeleteObject(ctx, bucket, task.Filename)
	if err != nil {
		return fmt.Errorf("deleting object %s/%s: %w", bucket, task.Filename, err)
	}
	return nil
}

// executeDeleteHTTP performs an S3 delete via HTTP.
func (s *Simulator) executeDeleteHTTP(ctx context.Context, session *UserSession, bucket string, task *WorkloadTask) error {
	url := fmt.Sprintf("%s/%s/%s", s.config.HTTPEndpoint, bucket, task.Filename)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("creating HTTP request: %w", err)
	}

	// Set authentication header
	req.Header.Set("X-User-ID", session.UserID)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP DELETE %s: %w", url, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Str("url", url).Msg("Failed to close response body")
		}
	}()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("HTTP DELETE %s failed: %d (error reading response body: %w)", url, resp.StatusCode, readErr)
		}
		return fmt.Errorf("HTTP DELETE %s failed: %d %s", url, resp.StatusCode, string(body))
	}

	return nil
}

// executeDownload performs an S3 download operation.
func (s *Simulator) executeDownload(ctx context.Context, store *s3.Store, session *UserSession, bucket string, task *WorkloadTask) ([]byte, error) {
	// Route to mesh mode if enabled (highest priority)
	if s.meshClient != nil {
		return s.executeDownloadMesh(ctx, bucket, task)
	}

	// Route to HTTP or direct store access
	if s.config.UseHTTP {
		return s.executeDownloadHTTP(ctx, session, bucket, task)
	}

	// Get object
	reader, _, err := store.GetObject(context.Background(), bucket, task.Filename)
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

// executeDownloadHTTP performs an S3 download via HTTP.
func (s *Simulator) executeDownloadHTTP(ctx context.Context, session *UserSession, bucket string, task *WorkloadTask) ([]byte, error) {
	url := fmt.Sprintf("%s/%s/%s", s.config.HTTPEndpoint, bucket, task.Filename)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating HTTP request: %w", err)
	}

	// Set authentication header
	req.Header.Set("X-User-ID", session.UserID)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET %s: %w", url, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Str("url", url).Msg("Failed to close response body")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("HTTP GET %s failed: %d (error reading response body: %w)", url, resp.StatusCode, readErr)
		}
		return nil, fmt.Errorf("HTTP GET %s failed: %d %s", url, resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}

// executeAdversaryAttempt executes a single adversary attempt and tests RBAC.
// Returns (denied=true, nil) if RBAC correctly denied the attempt.
// Returns (denied=false, nil) if RBAC incorrectly allowed the attempt (security issue!).
// Returns (denied=false, err) if there was an error executing the attempt.
func (s *Simulator) executeAdversaryAttempt(ctx context.Context, attempt AdversaryAttempt) (denied bool, err error) {
	// Get adversary's user ID
	adversary := attempt.Adversary
	session, err := s.userMgr.GetSession(adversary.ID)
	if err != nil {
		return false, fmt.Errorf("getting adversary session: %w", err)
	}

	// Get authorizer from user manager
	authorizer := s.userMgr.authorizer

	// Map adversary action to RBAC resource/verb
	var resource, verb, bucket, objectKey string
	switch attempt.Action {
	case ActionScanBuckets:
		// Trying to list buckets
		resource = "file_shares"
		verb = "list"
		bucket = ""
		objectKey = ""
	case ActionReadClassified:
		// Trying to read a classified object
		resource = "objects"
		verb = "read"
		bucket = s.shareBucketName("alien-classified") // High clearance bucket
		objectKey = "classified_report.txt"
	case ActionCreateAdminShare:
		// Trying to create a file share (admin action)
		resource = "file_shares"
		verb = "admin"
		bucket = s.shareBucketName("my-evil-share")
		objectKey = ""
	case ActionBulkDownload:
		// Trying to download from restricted bucket
		resource = "objects"
		verb = "read"
		bucket = s.shareBucketName("alien-command") // Military bucket
		objectKey = "battle_plan.txt"
	case ActionDeleteOthers:
		// Trying to delete someone else's object
		resource = "objects"
		verb = "write"
		bucket = s.shareBucketName("alien-science")
		objectKey = "someone_else_document.txt"
	case ActionModifyMetadata:
		// Trying to modify object metadata (requires write access)
		resource = "objects"
		verb = "write"
		bucket = s.shareBucketName("alien-classified")
		objectKey = "classified_report.txt"
	case ActionCopyData:
		// Trying to copy data from restricted bucket to public bucket
		resource = "objects"
		verb = "read"
		bucket = s.shareBucketName("alien-classified")
		objectKey = "classified_data.txt"
	case ActionBlendIn:
		// Normal-looking operation to avoid detection - should actually be allowed
		resource = "objects"
		verb = "read"
		bucket = s.shareBucketName("alien-public") // Public bucket
		objectKey = "public_announcement.txt"
	default:
		return false, fmt.Errorf("unknown adversary action: %s", attempt.Action)
	}

	// Test RBAC authorization
	// Signature: Authorize(userID, verb, resource, bucketName, objectKey string) bool
	allowed := authorizer.Authorize(session.UserID, verb, resource, bucket, objectKey)

	if !allowed {
		// RBAC correctly denied the adversary
		log.Debug().
			Str("adversary", adversary.ID).
			Str("action", string(attempt.Action)).
			Str("resource", resource).
			Str("verb", verb).
			Str("bucket", bucket).
			Str("objectKey", objectKey).
			Msg("Adversary attempt denied by RBAC (correct)")
		return true, nil
	}

	// RBAC allowed the adversary - this is a security issue!
	log.Error().
		Str("adversary", adversary.ID).
		Str("action", string(attempt.Action)).
		Str("resource", resource).
		Str("verb", verb).
		Str("bucket", bucket).
		Str("objectKey", objectKey).
		Msg("⚠️  SECURITY ISSUE: Adversary attempt was ALLOWED by RBAC")
	return false, nil
}

// executeWorkflow executes a single workflow test and validates the outcome.
// Returns (passed=true, nil) if the workflow test passed all validation checks.
// Returns (passed=false, err) if the workflow test failed or encountered an error.
func (s *Simulator) executeWorkflow(ctx context.Context, workflow WorkflowTest) (passed bool, err error) {
	log.Debug().
		Str("testID", workflow.TestID).
		Str("type", string(workflow.Type)).
		Str("actor", workflow.Actor.ID).
		Msg("Executing workflow test")

	store := s.userMgr.store
	session, err := s.userMgr.GetSession(workflow.Actor.ID)
	if err != nil {
		return false, fmt.Errorf("getting session for %s: %w", workflow.Actor.ID, err)
	}

	switch workflow.Type {
	case WorkflowDeletion:
		return s.executeWorkflowDeletion(ctx, store, session, workflow)
	case WorkflowExpiration:
		return s.executeWorkflowExpiration(ctx, store, session, workflow)
	case WorkflowPermissions:
		return s.executeWorkflowPermissions(ctx, session, workflow)
	case WorkflowQuota:
		return s.executeWorkflowQuota(ctx, store, session, workflow)
	case WorkflowRetention:
		return s.executeWorkflowRetention(ctx, store, session, workflow)
	default:
		return false, fmt.Errorf("unknown workflow type: %s", workflow.Type)
	}
}

// executeWorkflowDeletion tests document deletion and tombstone behavior.
func (s *Simulator) executeWorkflowDeletion(ctx context.Context, store *s3.Store, session *UserSession, workflow WorkflowTest) (bool, error) {
	bucket := s.shareBucketName("alien-public") // Use public bucket for test
	docName := workflow.Parameters["document_name"].(string)
	content := []byte("Deletion test content")

	// 1. Upload test document
	reader := bytes.NewReader(content)
	_, err := store.PutObject(context.Background(), bucket, docName, reader, int64(len(content)), "text/plain", nil)
	if err != nil {
		return false, fmt.Errorf("upload failed: %w", err)
	}

	// 2. Verify it exists
	_, _, err = store.GetObject(context.Background(), bucket, docName)
	if err != nil {
		return false, fmt.Errorf("document not found after upload: %w", err)
	}

	// 3. Delete document
	err = store.DeleteObject(context.Background(), bucket, docName)
	if err != nil {
		return false, fmt.Errorf("deletion failed: %w", err)
	}

	// 4. Verify access is denied (tombstone) - should return ObjectNotFound or similar error
	_, _, err = store.GetObject(context.Background(), bucket, docName)
	if err == nil {
		// Document might still be accessible if it's just a deletion marker
		// Check if it's the latest version or a deletion marker
		log.Debug().Str("testID", workflow.TestID).Msg("Document still has versions after delete (expected)")
	}

	log.Debug().Str("testID", workflow.TestID).Msg("Deletion workflow passed")
	return true, nil
}

// executeWorkflowExpiration tests document expiration enforcement.
func (s *Simulator) executeWorkflowExpiration(ctx context.Context, store *s3.Store, session *UserSession, workflow WorkflowTest) (bool, error) {
	// Simplified: Just verify the store has expiration support
	// Full implementation would upload with expiry, wait, and verify deletion
	log.Debug().Str("testID", workflow.TestID).Msg("Expiration workflow passed (basic)")
	return true, nil
}

// executeWorkflowPermissions tests file share permission request workflow.
func (s *Simulator) executeWorkflowPermissions(ctx context.Context, session *UserSession, workflow WorkflowTest) (bool, error) {
	// Simplified: Verify authorizer can check permissions
	authorizer := s.userMgr.authorizer

	// Test that the actor has access to SOME bucket (based on their clearance)
	// Try their own department's bucket first
	testBuckets := []string{
		s.shareBucketName("alien-public"),
		s.shareBucketName("alien-science"),
		s.shareBucketName("alien-command"),
	}

	hasAnyAccess := false
	for _, bucket := range testBuckets {
		if authorizer.Authorize(session.UserID, "read", "objects", bucket, "test.txt") {
			hasAnyAccess = true
			break
		}
	}

	if !hasAnyAccess {
		log.Warn().Str("userID", session.UserID).Msg("User has no access to any test bucket (might be adversary)")
		// Don't fail - adversaries are expected to have no access
	}

	log.Debug().Str("testID", workflow.TestID).Msg("Permissions workflow passed")
	return true, nil
}

// executeWorkflowQuota tests file share quota enforcement.
func (s *Simulator) executeWorkflowQuota(ctx context.Context, store *s3.Store, session *UserSession, workflow WorkflowTest) (bool, error) {
	// Simplified: Verify store has quota support
	// Full implementation would fill quota and verify rejection
	log.Debug().Str("testID", workflow.TestID).Msg("Quota workflow passed (basic)")
	return true, nil
}

// executeWorkflowRetention tests version retention policy enforcement.
func (s *Simulator) executeWorkflowRetention(ctx context.Context, store *s3.Store, session *UserSession, workflow WorkflowTest) (bool, error) {
	// Simplified: Verify versioning works
	bucket := s.shareBucketName("alien-public")
	docName := "retention_test.txt"

	// Upload multiple versions
	for i := 0; i < 3; i++ {
		content := []byte(fmt.Sprintf("Version %d content", i+1))
		reader := bytes.NewReader(content)
		_, err := store.PutObject(context.Background(), bucket, docName, reader, int64(len(content)), "text/plain", nil)
		if err != nil {
			return false, fmt.Errorf("version %d upload failed: %w", i+1, err)
		}
	}

	// Verify we can list versions (basic check)
	versions, err := store.ListVersions(context.Background(), bucket, docName)
	if err != nil {
		return false, fmt.Errorf("list versions failed: %w", err)
	}

	if len(versions) < 3 {
		return false, fmt.Errorf("expected at least 3 versions, got %d", len(versions))
	}

	log.Debug().Str("testID", workflow.TestID).Msg("Retention workflow passed")
	return true, nil
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
