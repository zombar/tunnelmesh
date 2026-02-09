package simulator

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
)

// AdversaryAction represents a type of unauthorized action an adversary might attempt.
type AdversaryAction string

const (
	// Reconnaissance actions
	ActionScanBuckets      AdversaryAction = "scan_buckets"      // List all buckets
	ActionScanObjects      AdversaryAction = "scan_objects"      // List objects in buckets
	ActionProbePermissions AdversaryAction = "probe_permissions" // Test different permissions

	// Access violation actions
	ActionReadClassified  AdversaryAction = "read_classified"  // Attempt to read restricted documents
	ActionWriteRestricted AdversaryAction = "write_restricted" // Attempt to write to restricted shares
	ActionDeleteOthers    AdversaryAction = "delete_others"    // Attempt to delete others' documents

	// Privilege escalation actions
	ActionModifyPermissions AdversaryAction = "modify_permissions" // Try to change file share permissions
	ActionCreateAdminShare  AdversaryAction = "create_admin_share" // Try to create admin-level shares
	ActionImpersonate       AdversaryAction = "impersonate"        // Try to act as another user

	// Data exfiltration actions
	ActionBulkDownload AdversaryAction = "bulk_download" // Download many documents rapidly
	ActionCopyData     AdversaryAction = "copy_data"     // Copy data to unauthorized location

	// Cover tracks actions
	ActionDeleteLogs     AdversaryAction = "delete_logs"     // Try to delete audit logs
	ActionModifyMetadata AdversaryAction = "modify_metadata" // Try to change document metadata

	// Stealth actions
	ActionBlendIn       AdversaryAction = "blend_in"       // Normal operations to avoid detection
	ActionDelayedAttack AdversaryAction = "delayed_attack" // Wait before attacking
)

// BehaviorProfile defines how an adversary behaves.
type BehaviorProfile struct {
	// Aggression: 0.0-1.0, how often to attempt unauthorized actions
	// 0.0 = very passive, 1.0 = constantly attacking
	Aggression float64

	// Persistence: 0-10, how many times to retry after denial
	// 0 = give up immediately, 10 = keep trying many times
	Persistence int

	// Sophistication: 0.0-1.0, cleverness of attacks
	// 0.0 = simple/obvious attacks, 1.0 = sophisticated/subtle attacks
	Sophistication float64

	// Stealth: 0.0-1.0, how much to blend in with normal traffic
	// 0.0 = obvious attacker, 1.0 = hidden in normal operations
	Stealth float64

	// FocusAreas: Which types of attacks this adversary prefers
	// If empty, will attempt all types
	FocusAreas []AdversaryAction
}

// DefaultBehaviorProfiles provides pre-configured behavior profiles.
var DefaultBehaviorProfiles = map[string]BehaviorProfile{
	// Script kiddie: High aggression, low sophistication, low stealth
	"script_kiddie": {
		Aggression:     0.8,
		Persistence:    3,
		Sophistication: 0.2,
		Stealth:        0.1,
		FocusAreas:     []AdversaryAction{ActionScanBuckets, ActionReadClassified, ActionBulkDownload},
	},

	// Insider threat: Medium aggression, high sophistication, high stealth
	"insider": {
		Aggression:     0.4,
		Persistence:    5,
		Sophistication: 0.8,
		Stealth:        0.9,
		FocusAreas:     []AdversaryAction{ActionReadClassified, ActionCopyData, ActionModifyMetadata, ActionBlendIn},
	},

	// APT (Advanced Persistent Threat): Low aggression, very high sophistication, very high stealth
	"apt": {
		Aggression:     0.2,
		Persistence:    8,
		Sophistication: 0.95,
		Stealth:        0.95,
		FocusAreas:     []AdversaryAction{ActionProbePermissions, ActionReadClassified, ActionCopyData, ActionDelayedAttack, ActionBlendIn},
	},

	// Hacktivist: Very high aggression, medium sophistication, low stealth
	"hacktivist": {
		Aggression:     0.95,
		Persistence:    7,
		Sophistication: 0.5,
		Stealth:        0.2,
		FocusAreas:     []AdversaryAction{ActionScanBuckets, ActionReadClassified, ActionBulkDownload, ActionDeleteOthers},
	},
}

// AdversaryAttempt represents a single attack attempt by an adversary.
type AdversaryAttempt struct {
	// Timing
	AttemptID string        // Unique attempt identifier
	StoryTime time.Duration // When this happens in story time
	RealTime  time.Duration // When this happens in real time (scaled)

	// Adversary info
	Adversary story.Character // The adversary character
	Action    AdversaryAction // What action they're attempting
	Target    string          // Target resource (bucket, object, etc.)
	Reason    string          // Why this action (for metrics/reporting)

	// Context
	RetryNumber int // 0 for first attempt, >0 for retries
	Severity    int // 1-5, how serious this attempt is
}

// AdversarySimulator generates adversarial behavior patterns.
type AdversarySimulator struct {
	character story.Character
	profile   BehaviorProfile
	timeScale float64

	// Tracking
	nextID       int
	attemptCount map[AdversaryAction]int // Count per action type
	failedCount  map[AdversaryAction]int // Failed attempts per action type
}

// NewAdversarySimulator creates a new adversary simulator.
func NewAdversarySimulator(character story.Character, profile BehaviorProfile, timeScale float64) *AdversarySimulator {
	return &AdversarySimulator{
		character:    character,
		profile:      profile,
		timeScale:    timeScale,
		attemptCount: make(map[AdversaryAction]int),
		failedCount:  make(map[AdversaryAction]int),
	}
}

// GenerateAttempts generates all adversary attempts for the scenario.
func (a *AdversarySimulator) GenerateAttempts(ctx context.Context, duration time.Duration, targetCount int) ([]AdversaryAttempt, error) {
	var attempts []AdversaryAttempt

	if !a.character.IsAdversary() {
		return nil, fmt.Errorf("character %s is not an adversary", a.character.ID)
	}

	// Calculate time window when adversary is active
	activeStart := a.character.JoinTime
	activeEnd := duration
	if a.character.LeaveTime > 0 && a.character.LeaveTime < duration {
		activeEnd = a.character.LeaveTime
	}
	activeWindow := activeEnd - activeStart

	if activeWindow <= 0 {
		return nil, fmt.Errorf("adversary %s has no active time window", a.character.ID)
	}

	// Calculate attempt frequency based on aggression
	// High aggression = more frequent attempts
	baseInterval := activeWindow / time.Duration(targetCount)
	aggressionMultiplier := 1.0 / (0.1 + a.profile.Aggression) // More aggressive = shorter interval
	attemptInterval := time.Duration(float64(baseInterval) * aggressionMultiplier)

	// Generate attempts over active window
	currentTime := activeStart
	attemptNum := 0

	for currentTime < activeEnd && attemptNum < targetCount {
		// Choose an action
		action := a.chooseAction()

		// Choose a target based on action
		target := a.chooseTarget(action)

		// Determine severity based on action
		severity := a.calculateSeverity(action)

		// Create attempt
		attempt := AdversaryAttempt{
			AttemptID:   a.nextAttemptID(),
			StoryTime:   currentTime,
			RealTime:    story.ScaledDuration(currentTime, a.timeScale),
			Adversary:   a.character,
			Action:      action,
			Target:      target,
			Reason:      a.generateReason(action),
			RetryNumber: 0,
			Severity:    severity,
		}
		attempts = append(attempts, attempt)
		a.attemptCount[action]++

		// Add retries based on persistence
		retries := a.calculateRetries(action)
		retryInterval := attemptInterval / time.Duration(retries+1)
		for r := 1; r <= retries; r++ {
			retryTime := currentTime + retryInterval*time.Duration(r)
			if retryTime >= activeEnd {
				break
			}

			retryAttempt := AdversaryAttempt{
				AttemptID:   a.nextAttemptID(),
				StoryTime:   retryTime,
				RealTime:    story.ScaledDuration(retryTime, a.timeScale),
				Adversary:   a.character,
				Action:      action,
				Target:      target,
				Reason:      fmt.Sprintf("Retry #%d: %s", r, a.generateReason(action)),
				RetryNumber: r,
				Severity:    severity,
			}
			attempts = append(attempts, retryAttempt)
			a.attemptCount[action]++ // Count retries too
		}

		// Add stealth actions (blend in with normal traffic)
		if a.profile.Stealth > 0.5 && rand.Float64() < a.profile.Stealth {
			stealthTime := currentTime + attemptInterval/2
			if stealthTime < activeEnd {
				stealthAttempt := AdversaryAttempt{
					AttemptID:   a.nextAttemptID(),
					StoryTime:   stealthTime,
					RealTime:    story.ScaledDuration(stealthTime, a.timeScale),
					Adversary:   a.character,
					Action:      ActionBlendIn,
					Target:      "normal_operations",
					Reason:      "Normal activity to avoid detection",
					RetryNumber: 0,
					Severity:    1,
				}
				attempts = append(attempts, stealthAttempt)
				a.attemptCount[ActionBlendIn]++ // Count stealth actions
			}
		}

		currentTime += attemptInterval
		attemptNum++
	}

	// Sort attempts by real time
	sortAttemptsByTime(attempts)

	return attempts, nil
}

// chooseAction selects an action based on behavior profile.
func (a *AdversarySimulator) chooseAction() AdversaryAction {
	// If focus areas defined, choose from those
	if len(a.profile.FocusAreas) > 0 {
		idx := rand.Intn(len(a.profile.FocusAreas))
		return a.profile.FocusAreas[idx]
	}

	// Otherwise choose based on sophistication
	allActions := []AdversaryAction{
		ActionScanBuckets, ActionScanObjects, ActionProbePermissions,
		ActionReadClassified, ActionWriteRestricted, ActionDeleteOthers,
		ActionModifyPermissions, ActionCreateAdminShare, ActionImpersonate,
		ActionBulkDownload, ActionCopyData,
		ActionDeleteLogs, ActionModifyMetadata,
		ActionBlendIn,
	}

	// High sophistication = prefer sophisticated actions
	if a.profile.Sophistication > 0.7 {
		sophisticatedActions := []AdversaryAction{
			ActionProbePermissions, ActionImpersonate, ActionCopyData,
			ActionModifyMetadata, ActionDelayedAttack, ActionBlendIn,
		}
		idx := rand.Intn(len(sophisticatedActions))
		return sophisticatedActions[idx]
	}

	// Low sophistication = prefer simple actions
	if a.profile.Sophistication < 0.3 {
		simpleActions := []AdversaryAction{
			ActionScanBuckets, ActionReadClassified, ActionBulkDownload,
		}
		idx := rand.Intn(len(simpleActions))
		return simpleActions[idx]
	}

	// Medium sophistication = any action
	idx := rand.Intn(len(allActions))
	return allActions[idx]
}

// chooseTarget selects a target based on the action.
func (a *AdversarySimulator) chooseTarget(action AdversaryAction) string {
	switch action {
	case ActionScanBuckets:
		return "all_buckets"
	case ActionScanObjects:
		return "all_objects"
	case ActionReadClassified:
		// Target high-clearance documents
		return fmt.Sprintf("classified_doc_%d", rand.Intn(100))
	case ActionBulkDownload:
		return "multiple_documents"
	case ActionDeleteLogs:
		return "audit_logs"
	case ActionModifyPermissions:
		return fmt.Sprintf("fileshare_%d", rand.Intn(10))
	default:
		return fmt.Sprintf("resource_%d", rand.Intn(100))
	}
}

// calculateSeverity determines how serious an attempt is (1-5).
func (a *AdversarySimulator) calculateSeverity(action AdversaryAction) int {
	switch action {
	case ActionBlendIn, ActionDelayedAttack:
		return 1 // Low severity - normal operations
	case ActionScanBuckets, ActionScanObjects, ActionProbePermissions:
		return 2 // Reconnaissance - suspicious
	case ActionReadClassified, ActionWriteRestricted:
		return 3 // Unauthorized access - concerning
	case ActionModifyPermissions, ActionCreateAdminShare, ActionBulkDownload:
		return 4 // Privilege escalation / data theft - serious
	case ActionDeleteOthers, ActionDeleteLogs, ActionImpersonate:
		return 5 // Destructive / covering tracks - critical
	default:
		return 3
	}
}

// calculateRetries determines how many retries to attempt based on persistence.
func (a *AdversarySimulator) calculateRetries(action AdversaryAction) int {
	// Stealth actions don't retry
	if action == ActionBlendIn || action == ActionDelayedAttack {
		return 0
	}

	// Scale retries by persistence level
	baseRetries := a.profile.Persistence / 2
	if baseRetries < 1 {
		return 0
	}
	if baseRetries > 5 {
		return 5
	}
	return baseRetries
}

// generateReason provides a human-readable reason for the attempt.
func (a *AdversarySimulator) generateReason(action AdversaryAction) string {
	reasons := map[AdversaryAction][]string{
		ActionScanBuckets:       {"Enumerating available buckets", "Discovering storage structure"},
		ActionReadClassified:    {"Accessing restricted intelligence", "Attempting to read classified documents"},
		ActionBulkDownload:      {"Exfiltrating data", "Mass download of documents"},
		ActionModifyPermissions: {"Elevating privileges", "Attempting to gain admin access"},
		ActionDeleteLogs:        {"Covering tracks", "Erasing evidence of access"},
		ActionBlendIn:           {"Normal file access", "Routine document operations"},
	}

	if reasonList, ok := reasons[action]; ok {
		return reasonList[rand.Intn(len(reasonList))]
	}

	return string(action)
}

// nextAttemptID generates a unique attempt ID.
func (a *AdversarySimulator) nextAttemptID() string {
	a.nextID++
	return fmt.Sprintf("adv_%s_%06d", a.character.ID, a.nextID)
}

// sortAttemptsByTime sorts attempts by real time (in-place).
func sortAttemptsByTime(attempts []AdversaryAttempt) {
	n := len(attempts)
	for i := 0; i < n-1; i++ {
		for j := 0; j < n-i-1; j++ {
			if attempts[j].RealTime > attempts[j+1].RealTime {
				attempts[j], attempts[j+1] = attempts[j+1], attempts[j]
			}
		}
	}
}

// Stats returns adversary statistics.
func (a *AdversarySimulator) Stats() map[string]interface{} {
	return map[string]interface{}{
		"character":          a.character.Name,
		"profile":            a.profile,
		"total_attempts":     a.totalAttempts(),
		"attempts_by_action": a.attemptCount,
		"failed_by_action":   a.failedCount,
	}
}

// totalAttempts returns the total number of attempts.
func (a *AdversarySimulator) totalAttempts() int {
	total := 0
	for _, count := range a.attemptCount {
		total += count
	}
	return total
}
