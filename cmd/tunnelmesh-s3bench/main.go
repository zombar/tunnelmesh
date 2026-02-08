// tunnelmesh-s3bench is a story-driven S3 stress testing tool.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/simulator"
	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story/scenarios"
)

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// Global flags
var (
	logLevel    string
	outputJSON  string
	verbose     bool
	storageRoot string
	endpoint    string
)

// Run command flags
var (
	timeScale            float64
	concurrency          int
	enableMesh           bool
	enableAdversary      bool
	enableWorkflows      bool
	testDeletion         bool
	testExpiration       bool
	testPermissions      bool
	testQuota            bool
	testRetention        bool
	quotaOverrideMB      int64
	expiryOverride       time.Duration
	adversaryAttempts    int
	maxConcurrentUploads int
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "tunnelmesh-s3bench",
	Short: "Story-driven S3 stress testing tool",
	Long: `tunnelmesh-s3bench is a narrative-driven S3 stress testing tool that generates
realistic workloads based on story scenarios. It tests S3 storage, deduplication,
versioning, RBAC, file shares, and mesh dynamics.`,
	Version: fmt.Sprintf("%s (commit: %s, built: %s)", Version, Commit, BuildTime),
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		setupLogging()
	},
}

func init() {
	// Global flags
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "Log level (debug, info, warn, error)")
	rootCmd.PersistentFlags().StringVar(&outputJSON, "output-json", "", "Write results to JSON file")
	rootCmd.PersistentFlags().BoolVar(&verbose, "verbose", false, "Verbose output")
	rootCmd.PersistentFlags().StringVar(&storageRoot, "storage-root", "", "Storage root directory (default: temp dir)")
	rootCmd.PersistentFlags().StringVar(&endpoint, "endpoint", "http://localhost:8080", "S3 endpoint URL")

	// Add subcommands
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(describeCmd)

	// Run command flags
	runCmd.Flags().Float64Var(&timeScale, "time-scale", 1.0, "Time scaling factor (1.0=realtime, 100.0=100x faster)")
	runCmd.Flags().IntVar(&concurrency, "concurrency", 3, "Number of parallel users")
	runCmd.Flags().BoolVar(&enableMesh, "enable-mesh", false, "Use Docker mesh orchestration")
	runCmd.Flags().BoolVar(&enableAdversary, "enable-adversary", true, "Enable adversarial characters")
	runCmd.Flags().BoolVar(&enableWorkflows, "enable-workflows", true, "Enable workflow tests")
	runCmd.Flags().BoolVar(&testDeletion, "test-deletion", true, "Enable deletion workflow tests")
	runCmd.Flags().BoolVar(&testExpiration, "test-expiration", true, "Enable expiration workflow tests")
	runCmd.Flags().BoolVar(&testPermissions, "test-permissions", true, "Enable permission workflow tests")
	runCmd.Flags().BoolVar(&testQuota, "test-quota", true, "Enable quota workflow tests")
	runCmd.Flags().BoolVar(&testRetention, "test-retention", true, "Enable retention workflow tests")
	runCmd.Flags().Int64Var(&quotaOverrideMB, "quota-override", 0, "Override file share quotas in MB (0=use story defaults)")
	runCmd.Flags().DurationVar(&expiryOverride, "expiry-override", 0, "Override document expiries (0=use story defaults)")
	runCmd.Flags().IntVar(&adversaryAttempts, "adversary-attempts", 50, "Number of adversary attempts to generate")
	runCmd.Flags().IntVar(&maxConcurrentUploads, "max-concurrent-uploads", 10, "Maximum concurrent uploads")
}

var runCmd = &cobra.Command{
	Use:   "run <story>",
	Short: "Execute a story scenario",
	Long: `Execute a story-driven stress test scenario. Available stories:
  - alien_invasion: 72h alien invasion with 3 characters, 4 departments (default)

Example:
  # Quick test (1 minute)
  s3bench run alien_invasion --time-scale 4320

  # Stress test (5 minutes)
  s3bench run alien_invasion --time-scale 864 --enable-mesh --enable-adversary

  # Realistic demo (2 hours)
  s3bench run alien_invasion --time-scale 36`,
	Args: cobra.ExactArgs(1),
	RunE: runScenario,
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List available story scenarios",
	RunE: func(cmd *cobra.Command, args []string) error {
		stories := getAvailableStories()
		fmt.Println("Available story scenarios:")
		fmt.Println()
		for name, st := range stories {
			fmt.Printf("  %s\n", name)
			fmt.Printf("    %s\n", st.Description())
			fmt.Printf("    Duration: %v\n", st.Duration())
			fmt.Printf("    Characters: %d\n", len(st.Characters()))
			fmt.Printf("    Departments: %d\n", len(st.Departments()))
			fmt.Println()
		}
		return nil
	},
}

var describeCmd = &cobra.Command{
	Use:   "describe <story>",
	Short: "Show detailed information about a story",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		storyName := args[0]
		stories := getAvailableStories()
		st, ok := stories[storyName]
		if !ok {
			return fmt.Errorf("story %q not found", storyName)
		}

		printStoryDetails(st)
		return nil
	},
}

func setupLogging() {
	// Parse log level
	level, err := zerolog.ParseLevel(logLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)

	// Setup console output with colors
	if !verbose {
		log.Logger = log.Output(zerolog.ConsoleWriter{
			Out:        os.Stderr,
			TimeFormat: time.RFC3339,
			NoColor:    false,
		})
	}
}

func getAvailableStories() map[string]story.Story {
	return map[string]story.Story{
		"alien_invasion": &scenarios.AlienInvasion{},
	}
}

func runScenario(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Info().Msg("Received interrupt signal, shutting down...")
		cancel()
	}()

	// Get story
	storyName := args[0]
	stories := getAvailableStories()
	st, ok := stories[storyName]
	if !ok {
		return fmt.Errorf("story %q not found. Use 'list' to see available stories", storyName)
	}

	// Create storage root
	root := storageRoot
	if root == "" {
		var err error
		root, err = os.MkdirTemp("", "s3bench-*")
		if err != nil {
			return fmt.Errorf("creating temp dir: %w", err)
		}
		defer func() {
			if err := os.RemoveAll(root); err != nil {
				log.Warn().Err(err).Str("path", root).Msg("Failed to remove temp directory")
			}
		}()
		log.Info().Str("path", root).Msg("Created temporary storage directory")
	}

	// Create store
	quotaMgr := s3.NewQuotaManager(100 * 1024 * 1024 * 1024) // 100GB
	masterKey := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	store, err := s3.NewStoreWithCAS(root, quotaMgr, masterKey)
	if err != nil {
		return fmt.Errorf("creating store: %w", err)
	}

	// Create components
	credentials := s3.NewCredentialStore()
	authorizer := auth.NewAuthorizerWithGroups()
	systemStore, err := s3.NewSystemStore(store, "s3bench")
	if err != nil {
		return fmt.Errorf("creating system store: %w", err)
	}
	shareManager := s3.NewFileShareManager(store, systemStore, authorizer)

	// Create user manager
	userMgr := simulator.NewUserManager(store, credentials, authorizer, shareManager, st)
	if quotaOverrideMB > 0 {
		userMgr.SetQuotaOverride(quotaOverrideMB)
	}

	// Setup users and file shares
	log.Info().Msg("Setting up users, credentials, and file shares...")
	if err := userMgr.Setup(ctx, endpoint); err != nil {
		return fmt.Errorf("setting up users: %w", err)
	}
	defer func() {
		if err := userMgr.Cleanup(); err != nil {
			log.Warn().Err(err).Msg("Failed to cleanup users")
		}
	}()

	// Configure simulator
	config := simulator.SimulatorConfig{
		Story:                st,
		TimeScale:            timeScale,
		EnableMesh:           enableMesh,
		EnableAdversary:      enableAdversary,
		EnableWorkflows:      enableWorkflows,
		QuotaOverrideMB:      quotaOverrideMB,
		ExpiryOverride:       expiryOverride,
		AdversaryAttempts:    adversaryAttempts,
		MaxConcurrentUploads: maxConcurrentUploads,
		UserManager:          userMgr, // Pass user manager for actual S3 operations
		WorkflowTestsEnabled: map[simulator.WorkflowType]bool{
			simulator.WorkflowDeletion:    testDeletion,
			simulator.WorkflowExpiration:  testExpiration,
			simulator.WorkflowPermissions: testPermissions,
			simulator.WorkflowQuota:       testQuota,
			simulator.WorkflowRetention:   testRetention,
		},
	}

	// Create simulator
	log.Info().Msg("Creating simulator...")
	sim, err := simulator.NewSimulator(config)
	if err != nil {
		return fmt.Errorf("creating simulator: %w", err)
	}

	// Print scenario summary
	printScenarioIntro(st, timeScale)

	// Generate scenario
	log.Info().Msg("Generating scenario...")
	if err := sim.GenerateScenario(ctx); err != nil {
		return fmt.Errorf("generating scenario: %w", err)
	}

	summary := sim.GetScenarioSummary()
	fmt.Println()
	fmt.Println(summary.String())
	fmt.Println()

	// Run scenario
	log.Info().Msg("Starting scenario execution...")
	fmt.Println("Press Ctrl+C to stop")
	fmt.Println()

	metrics, err := sim.Run(ctx)
	if err != nil {
		return fmt.Errorf("running scenario: %w", err)
	}

	// Print final report
	printFinalReport(metrics)

	// Write JSON output if requested
	if outputJSON != "" {
		// TODO: Implement JSON output
		log.Info().Str("file", outputJSON).Msg("JSON output not yet implemented")
	}

	return nil
}

func printScenarioIntro(st story.Story, timeScale float64) {
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Printf("║  %-55s  ║\n", "SCENARIO: "+st.Name())
	fmt.Println("║                                                            ║")
	fmt.Printf("║  %-55s  ║\n", st.Description())
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
	fmt.Println()

	realDuration := story.ScaledDuration(st.Duration(), timeScale)
	fmt.Printf("Story Duration: %v (%.1fx speed = %v real time)\n",
		st.Duration(), timeScale, realDuration)
	fmt.Println()
}

func printStoryDetails(st story.Story) {
	fmt.Println()
	fmt.Println("Story:", st.Name())
	fmt.Println("Description:", st.Description())
	fmt.Println("Duration:", st.Duration())
	fmt.Println()

	fmt.Println("Characters:")
	for _, char := range st.Characters() {
		alignment := "good"
		if char.IsAdversary() {
			alignment = "ADVERSARY"
		}
		fmt.Printf("  - %s (%s) - %s [%s]\n", char.Name, char.Role, alignment, char.Department)
		fmt.Printf("    Clearance: %d, Joins: %v", char.Clearance, char.JoinTime)
		if char.LeaveTime > 0 {
			fmt.Printf(", Leaves: %v", char.LeaveTime)
		}
		fmt.Println()
	}
	fmt.Println()

	fmt.Println("Departments (File Shares):")
	for _, dept := range st.Departments() {
		fmt.Printf("  - %s (%s)\n", dept.Name, dept.FileShare)
		fmt.Printf("    Members: %v\n", dept.Members)
		fmt.Printf("    Quota: %d MB, Guest Read: %v\n", dept.QuotaMB, dept.GuestRead)
	}
	fmt.Println()
}

func printFinalReport(metrics *simulator.SimulatorMetrics) {
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║                     FINAL REPORT                          ║")
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
	fmt.Println()

	// Timing
	fmt.Println("Timing:")
	fmt.Printf("  Started:        %s\n", metrics.StartTime.Format(time.RFC3339))
	fmt.Printf("  Ended:          %s\n", metrics.EndTime.Format(time.RFC3339))
	fmt.Printf("  Real Duration:  %v\n", metrics.Duration)
	fmt.Printf("  Story Duration: %v\n", metrics.StoryDuration)
	fmt.Println()

	// Operations
	fmt.Println("Operations:")
	fmt.Printf("  Tasks Generated: %d\n", metrics.TasksGenerated)
	fmt.Printf("  Tasks Completed: %d\n", metrics.TasksCompleted)
	fmt.Printf("  Tasks Failed:    %d\n", metrics.TasksFailed)
	fmt.Printf("  Uploads:         %d\n", metrics.UploadCount)
	fmt.Printf("  Downloads:       %d\n", metrics.DownloadCount)
	fmt.Printf("  Updates:         %d\n", metrics.UpdateCount)
	fmt.Printf("  Deletes:         %d\n", metrics.DeleteCount)
	fmt.Println()

	// Data
	fmt.Println("Data:")
	fmt.Printf("  Bytes Uploaded:   %d (%.2f MB)\n", metrics.BytesUploaded, float64(metrics.BytesUploaded)/1024/1024)
	fmt.Printf("  Bytes Downloaded: %d (%.2f MB)\n", metrics.BytesDownloaded, float64(metrics.BytesDownloaded)/1024/1024)
	fmt.Printf("  Documents:        %d\n", metrics.DocumentsCreated)
	fmt.Printf("  Versions:         %d\n", metrics.VersionsCreated)
	fmt.Println()

	// Storage Efficiency
	if metrics.PhysicalBytes > 0 {
		fmt.Println("Storage Efficiency:")
		fmt.Printf("  Logical Bytes:      %d (%.2f MB)\n", metrics.LogicalBytes, float64(metrics.LogicalBytes)/1024/1024)
		fmt.Printf("  Physical Bytes:     %d (%.2f MB)\n", metrics.PhysicalBytes, float64(metrics.PhysicalBytes)/1024/1024)
		fmt.Printf("  Deduplication:      %.2fx\n", metrics.DeduplicationRatio)
		fmt.Printf("  Compression:        %.2fx\n", metrics.CompressionRatio)
		fmt.Println()
	}

	// Security
	if metrics.AdversaryAttempts > 0 {
		fmt.Println("Security (Adversary Simulation):")
		fmt.Printf("  Unauthorized Attempts: %d\n", metrics.AdversaryAttempts)
		fmt.Printf("  Denials:               %d\n", metrics.AdversaryDenials)
		fmt.Printf("  Denial Rate:           %.1f%%\n", metrics.AdversaryDenialRate*100)
		fmt.Println()
	}

	// Workflows
	if metrics.WorkflowTestsRun > 0 {
		fmt.Println("Workflow Tests:")
		fmt.Printf("  Tests Run:    %d\n", metrics.WorkflowTestsRun)
		fmt.Printf("  Tests Passed: %d\n", metrics.WorkflowTestsPassed)
		fmt.Printf("  Tests Failed: %d\n", metrics.WorkflowTestsFailed)
		fmt.Println()
	}

	// Mesh
	if metrics.PeerJoins > 0 || metrics.PeerLeaves > 0 {
		fmt.Println("Mesh Dynamics:")
		fmt.Printf("  Peer Joins:  %d\n", metrics.PeerJoins)
		fmt.Printf("  Peer Leaves: %d\n", metrics.PeerLeaves)
		if metrics.MeshDowntime > 0 {
			fmt.Printf("  Downtime:    %v\n", metrics.MeshDowntime)
		}
		fmt.Println()
	}

	// Errors
	if len(metrics.Errors) > 0 {
		fmt.Println("Errors:")
		for _, err := range metrics.Errors {
			fmt.Printf("  - %s\n", err)
		}
		fmt.Println()
	}

	fmt.Println("═══════════════════════════════════════════════════════════")
}
