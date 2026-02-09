package scenarios

import (
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
)

func TestAlienInvasion(t *testing.T) {
	ai := &AlienInvasion{}

	t.Run("basic properties", func(t *testing.T) {
		if ai.Name() != "alien_invasion" {
			t.Errorf("Name() = %v, want alien_invasion", ai.Name())
		}

		if ai.Duration() != 72*time.Hour {
			t.Errorf("Duration() = %v, want 72h", ai.Duration())
		}

		if ai.Description() == "" {
			t.Error("Description() should not be empty")
		}
	})

	t.Run("timeline", func(t *testing.T) {
		timeline := ai.Timeline()
		if len(timeline) == 0 {
			t.Fatal("Timeline() should not be empty")
		}

		// Verify timeline is ordered
		for i := 1; i < len(timeline); i++ {
			if timeline[i].Time < timeline[i-1].Time {
				t.Errorf("Timeline not ordered: event %d at %v comes after event %d at %v",
					i, timeline[i].Time, i-1, timeline[i-1].Time)
			}
		}

		// Verify critical events exist
		hasFirstContact := false
		hasInvasion := false
		for _, event := range timeline {
			if event.Severity == 5 {
				if event.Time < 24*time.Hour {
					hasFirstContact = true
				} else if event.Time >= 24*time.Hour && event.Time < 48*time.Hour {
					hasInvasion = true
				}
			}
		}
		if !hasFirstContact {
			t.Error("Timeline missing first contact critical event")
		}
		if !hasInvasion {
			t.Error("Timeline missing invasion critical event")
		}
	})

	t.Run("characters", func(t *testing.T) {
		characters := ai.Characters()
		if len(characters) != 3 {
			t.Fatalf("Characters() = %d, want 3", len(characters))
		}

		// Find specific characters
		var commander, scientist, eve *story.Character
		for i := range characters {
			switch characters[i].ID {
			case "commander":
				commander = &characters[i]
			case "scientist":
				scientist = &characters[i]
			case "eve":
				eve = &characters[i]
			}
		}

		testCharacter(t, "commander", commander, 5, false)
		testCharacter(t, "scientist", scientist, 4, false)
		testCharacterWithTiming(t, "eve", eve, 0, true, 12*time.Hour, 30*time.Hour)
	})

	t.Run("departments", func(t *testing.T) {
		departments := ai.Departments()
		if len(departments) != 4 {
			t.Fatalf("Departments() = %d, want 4", len(departments))
		}

		// Verify classified department is commander-only
		var classified *story.Department
		for i := range departments {
			if departments[i].ID == "classified" {
				classified = &departments[i]
				break
			}
		}
		if classified == nil {
			t.Fatal("Missing classified department")
		}
		if len(classified.Members) != 1 || classified.Members[0] != "commander" {
			t.Errorf("Classified members = %v, want [commander]", classified.Members)
		}
		if classified.GuestRead {
			t.Error("Classified should not allow guest read")
		}
	})

	t.Run("document rules", func(t *testing.T) {
		rules := ai.DocumentRules()
		if len(rules) < 15 {
			t.Errorf("DocumentRules() = %d, want at least 15 types", len(rules))
		}

		types := validateDocumentRules(t, rules)
		validateRequiredDocumentTypes(t, types)
		validateExpiringDocuments(t, rules)
	})
}

// testCharacter is a helper to validate basic character properties.
func testCharacter(t *testing.T, name string, char *story.Character, expectedClearance int, expectedAdversary bool) {
	t.Helper()
	if char == nil {
		t.Fatalf("Missing %s character", name)
	}
	if expectedClearance > 0 && char.Clearance != expectedClearance {
		t.Errorf("%s clearance = %d, want %d", name, char.Clearance, expectedClearance)
	}
	if char.IsAdversary() != expectedAdversary {
		t.Errorf("%s IsAdversary() = %v, want %v", name, char.IsAdversary(), expectedAdversary)
	}
}

// testCharacterWithTiming validates character properties including timing.
func testCharacterWithTiming(t *testing.T, name string, char *story.Character, expectedClearance int, expectedAdversary bool, expectedJoinTime, expectedLeaveTime time.Duration) {
	t.Helper()
	testCharacter(t, name, char, expectedClearance, expectedAdversary)
	if char.JoinTime != expectedJoinTime {
		t.Errorf("%s JoinTime = %v, want %v", name, char.JoinTime, expectedJoinTime)
	}
	if char.LeaveTime != expectedLeaveTime {
		t.Errorf("%s LeaveTime = %v, want %v", name, char.LeaveTime, expectedLeaveTime)
	}
}

// validateDocumentRules validates all document rules and returns a map of types.
func validateDocumentRules(t *testing.T, rules []story.DocumentRule) map[string]bool {
	t.Helper()
	types := make(map[string]bool)
	for _, rule := range rules {
		types[rule.Type] = true

		// Verify size ranges are valid
		if rule.SizeRange[0] <= 0 || rule.SizeRange[1] <= 0 {
			t.Errorf("Document type %s has invalid size range: %v", rule.Type, rule.SizeRange)
		}
		if rule.SizeRange[0] > rule.SizeRange[1] {
			t.Errorf("Document type %s has inverted size range: %v", rule.Type, rule.SizeRange)
		}

		// Verify frequency is positive
		if rule.Frequency <= 0 {
			t.Errorf("Document type %s has invalid frequency: %v", rule.Type, rule.Frequency)
		}

		// Verify authors exist
		if len(rule.Authors) == 0 {
			t.Errorf("Document type %s has no authors", rule.Type)
		}
	}
	return types
}

// validateRequiredDocumentTypes checks that all required document types exist.
func validateRequiredDocumentTypes(t *testing.T, types map[string]bool) {
	t.Helper()
	requiredTypes := []string{
		"battle_report", "sitrep", "scientific_analysis",
		"council_meeting", "telephone", "im_transcript", "casualty_list",
	}
	for _, reqType := range requiredTypes {
		if !types[reqType] {
			t.Errorf("Missing required document type: %s", reqType)
		}
	}
}

// validateExpiringDocuments ensures at least one document rule has expiration.
func validateExpiringDocuments(t *testing.T, rules []story.DocumentRule) {
	t.Helper()
	for _, rule := range rules {
		if rule.ExpiresIn > 0 {
			return // Found at least one expiring document
		}
	}
	t.Error("No document rules with expiration for workflow testing")
}

func TestAlienInvasionDocumentVolume(t *testing.T) {
	ai := &AlienInvasion{}
	rules := ai.DocumentRules()

	// Estimate total documents generated over 72h
	totalDocs := 0
	for _, rule := range rules {
		count := int(ai.Duration() / rule.Frequency)
		totalDocs += count
	}

	// Should generate 3000+ documents
	if totalDocs < 3000 {
		t.Errorf("Estimated document count = %d, want at least 3000", totalDocs)
	}

	t.Logf("Estimated document generation: %d documents over 72h", totalDocs)
}
