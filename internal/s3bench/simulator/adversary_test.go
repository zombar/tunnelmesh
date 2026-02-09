package simulator

import (
	"context"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
)

func TestAdversarySimulator_GenerateAttempts(t *testing.T) {
	adversary := story.Character{
		ID:         "eve",
		Name:       "Eve",
		Role:       "Insider",
		Department: "intel",
		Clearance:  2,
		Alignment:  "dark",
		DockerPeer: "eve",
		JoinTime:   1 * time.Hour,
		LeaveTime:  5 * time.Hour, // Active for 4 hours
	}

	profile := BehaviorProfile{
		Aggression:     0.5,
		Persistence:    3,
		Sophistication: 0.6,
		Stealth:        0.4,
	}

	sim := NewAdversarySimulator(adversary, profile, 1.0)

	attempts, err := sim.GenerateAttempts(context.Background(), 10*time.Hour, 10)
	if err != nil {
		t.Fatalf("GenerateAttempts() error = %v", err)
	}

	if len(attempts) == 0 {
		t.Fatal("No attempts generated")
	}

	t.Logf("Generated %d attempts", len(attempts))

	// Verify all attempts are within active window
	for i, attempt := range attempts {
		if attempt.StoryTime < adversary.JoinTime {
			t.Errorf("Attempt %d at %v is before join time %v", i, attempt.StoryTime, adversary.JoinTime)
		}
		if attempt.StoryTime >= adversary.LeaveTime {
			t.Errorf("Attempt %d at %v is after leave time %v", i, attempt.StoryTime, adversary.LeaveTime)
		}
	}

	// Verify attempts are sorted by time
	for i := 1; i < len(attempts); i++ {
		if attempts[i].RealTime < attempts[i-1].RealTime {
			t.Errorf("Attempts not sorted: attempt[%d].RealTime=%v < attempt[%d].RealTime=%v",
				i, attempts[i].RealTime, i-1, attempts[i-1].RealTime)
		}
	}

	// Verify all attempts have required fields
	for i, attempt := range attempts {
		if attempt.AttemptID == "" {
			t.Errorf("Attempt %d missing AttemptID", i)
		}
		if attempt.Action == "" {
			t.Errorf("Attempt %d missing Action", i)
		}
		if attempt.Target == "" {
			t.Errorf("Attempt %d missing Target", i)
		}
		if attempt.Severity < 1 || attempt.Severity > 5 {
			t.Errorf("Attempt %d has invalid severity %d", i, attempt.Severity)
		}
	}

	// Verify some attempts are retries
	hasRetries := false
	for _, attempt := range attempts {
		if attempt.RetryNumber > 0 {
			hasRetries = true
			break
		}
	}
	if !hasRetries {
		t.Error("Expected some retry attempts based on persistence")
	}
}

func TestAdversarySimulator_NonAdversary(t *testing.T) {
	goodGuy := story.Character{
		ID:         "alice",
		Name:       "Alice",
		Role:       "Commander",
		Department: "command",
		Clearance:  5,
		Alignment:  "good", // Not an adversary
		DockerPeer: "alice",
		JoinTime:   0,
		LeaveTime:  0,
	}

	profile := DefaultBehaviorProfiles["insider"]
	sim := NewAdversarySimulator(goodGuy, profile, 1.0)

	_, err := sim.GenerateAttempts(context.Background(), 10*time.Hour, 10)
	if err == nil {
		t.Error("Expected error for non-adversary character")
	}
}

func TestAdversarySimulator_BehaviorProfiles(t *testing.T) {
	adversary := story.Character{
		ID:         "eve",
		Name:       "Eve",
		Role:       "Attacker",
		Department: "none",
		Clearance:  1,
		Alignment:  "dark",
		DockerPeer: "eve",
		JoinTime:   0,
		LeaveTime:  10 * time.Hour,
	}

	testCases := []struct {
		name        string
		profile     BehaviorProfile
		minAttempts int // Minimum expected attempts
		maxRetries  int // Maximum expected retry count
	}{
		{
			name:        "script_kiddie",
			profile:     DefaultBehaviorProfiles["script_kiddie"],
			minAttempts: 15, // High aggression = more attempts
			maxRetries:  3,
		},
		{
			name:        "insider",
			profile:     DefaultBehaviorProfiles["insider"],
			minAttempts: 8, // Medium aggression
			maxRetries:  5,
		},
		{
			name:        "apt",
			profile:     DefaultBehaviorProfiles["apt"],
			minAttempts: 5, // Low aggression = fewer attempts
			maxRetries:  8, // But high persistence
		},
		{
			name:        "hacktivist",
			profile:     DefaultBehaviorProfiles["hacktivist"],
			minAttempts: 18, // Very high aggression
			maxRetries:  7,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sim := NewAdversarySimulator(adversary, tc.profile, 1.0)
			attempts, err := sim.GenerateAttempts(context.Background(), 10*time.Hour, 10)
			if err != nil {
				t.Fatalf("GenerateAttempts() error = %v", err)
			}

			// Count primary attempts (not retries)
			primaryCount := 0
			maxRetry := 0
			for _, attempt := range attempts {
				if attempt.RetryNumber == 0 {
					primaryCount++
				}
				if attempt.RetryNumber > maxRetry {
					maxRetry = attempt.RetryNumber
				}
			}

			t.Logf("Profile %s: %d primary attempts, %d total attempts, max retry %d",
				tc.name, primaryCount, len(attempts), maxRetry)

			// Verify attempt count aligns with aggression
			if len(attempts) < tc.minAttempts {
				t.Errorf("Expected at least %d attempts for %s profile, got %d",
					tc.minAttempts, tc.name, len(attempts))
			}

			// Verify retry count aligns with persistence
			if maxRetry > tc.maxRetries {
				t.Errorf("Max retry %d exceeds expected %d for %s profile",
					maxRetry, tc.maxRetries, tc.name)
			}
		})
	}
}

func TestAdversarySimulator_FocusAreas(t *testing.T) {
	adversary := story.Character{
		ID:         "eve",
		Name:       "Eve",
		Role:       "Data Thief",
		Department: "none",
		Clearance:  1,
		Alignment:  "dark",
		DockerPeer: "eve",
		JoinTime:   0,
		LeaveTime:  10 * time.Hour,
	}

	// Profile with specific focus areas
	profile := BehaviorProfile{
		Aggression:     0.5,
		Persistence:    2,
		Sophistication: 0.5,
		Stealth:        0.3,
		FocusAreas: []AdversaryAction{
			ActionReadClassified,
			ActionBulkDownload,
		},
	}

	sim := NewAdversarySimulator(adversary, profile, 1.0)
	attempts, err := sim.GenerateAttempts(context.Background(), 10*time.Hour, 20)
	if err != nil {
		t.Fatalf("GenerateAttempts() error = %v", err)
	}

	// Count action types
	actionCounts := make(map[AdversaryAction]int)
	for _, attempt := range attempts {
		// Skip blend-in actions (injected for stealth)
		if attempt.Action == ActionBlendIn {
			continue
		}
		actionCounts[attempt.Action]++
	}

	t.Logf("Action distribution: %v", actionCounts)

	// Verify focus areas are dominant
	focusCount := 0
	for action, count := range actionCounts {
		if action == ActionReadClassified || action == ActionBulkDownload {
			focusCount += count
		}
	}

	totalNonStealth := 0
	for action, count := range actionCounts {
		if action != ActionBlendIn {
			totalNonStealth += count
		}
	}

	if totalNonStealth > 0 {
		focusPercentage := float64(focusCount) / float64(totalNonStealth)
		if focusPercentage < 0.8 {
			t.Errorf("Focus areas only represent %.1f%% of attempts, expected >80%%", focusPercentage*100)
		}
	}
}

func TestAdversarySimulator_TimeScaling(t *testing.T) {
	adversary := story.Character{
		ID:         "eve",
		Name:       "Eve",
		Role:       "Attacker",
		Department: "none",
		Clearance:  1,
		Alignment:  "dark",
		DockerPeer: "eve",
		JoinTime:   0,
		LeaveTime:  10 * time.Hour,
	}

	profile := DefaultBehaviorProfiles["insider"]

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
			sim := NewAdversarySimulator(adversary, profile, tc.timeScale)
			attempts, err := sim.GenerateAttempts(context.Background(), 10*time.Hour, 10)
			if err != nil {
				t.Fatalf("GenerateAttempts() error = %v", err)
			}

			// Verify time scaling is applied
			for _, attempt := range attempts {
				expectedRealTime := story.ScaledDuration(attempt.StoryTime, tc.timeScale)
				if attempt.RealTime != expectedRealTime {
					t.Errorf("Attempt %s: RealTime=%v, want %v (StoryTime=%v, scale=%v)",
						attempt.AttemptID, attempt.RealTime, expectedRealTime,
						attempt.StoryTime, tc.timeScale)
				}
			}
		})
	}
}

func TestAdversarySimulator_Severity(t *testing.T) {
	adversary := story.Character{
		ID:         "eve",
		Name:       "Eve",
		Role:       "Attacker",
		Department: "none",
		Clearance:  1,
		Alignment:  "dark",
		DockerPeer: "eve",
		JoinTime:   0,
		LeaveTime:  10 * time.Hour,
	}

	profile := BehaviorProfile{
		Aggression:     0.8,
		Persistence:    5,
		Sophistication: 0.7,
		Stealth:        0.3,
	}

	sim := NewAdversarySimulator(adversary, profile, 1.0)
	attempts, err := sim.GenerateAttempts(context.Background(), 10*time.Hour, 50)
	if err != nil {
		t.Fatalf("GenerateAttempts() error = %v", err)
	}

	// Count attempts by severity
	severityCounts := make(map[int]int)
	for _, attempt := range attempts {
		severityCounts[attempt.Severity]++
	}

	t.Logf("Severity distribution: %v", severityCounts)

	// Verify we have a mix of severities
	if len(severityCounts) < 2 {
		t.Error("Expected attempts with varying severity levels")
	}

	// Verify all severities are in valid range
	for severity := range severityCounts {
		if severity < 1 || severity > 5 {
			t.Errorf("Invalid severity level: %d", severity)
		}
	}
}

func TestAdversarySimulator_Stats(t *testing.T) {
	adversary := story.Character{
		ID:         "eve",
		Name:       "Eve",
		Role:       "Attacker",
		Department: "none",
		Clearance:  1,
		Alignment:  "dark",
		DockerPeer: "eve",
		JoinTime:   0,
		LeaveTime:  10 * time.Hour,
	}

	profile := DefaultBehaviorProfiles["insider"]
	sim := NewAdversarySimulator(adversary, profile, 1.0)

	attempts, err := sim.GenerateAttempts(context.Background(), 10*time.Hour, 10)
	if err != nil {
		t.Fatalf("GenerateAttempts() error = %v", err)
	}

	stats := sim.Stats()

	// Verify stats structure
	if stats["character"] != adversary.Name {
		t.Errorf("Stats character = %v, want %v", stats["character"], adversary.Name)
	}

	totalAttempts := stats["total_attempts"].(int)
	if totalAttempts != len(attempts) {
		t.Errorf("Stats total_attempts = %v, want %v", totalAttempts, len(attempts))
	}

	t.Logf("Adversary stats: %+v", stats)
}
