package scenarios

import (
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
)

func TestAlienInvasion(t *testing.T) {
	ai := &AlienInvasion{}

	// Test basic properties
	if ai.Name() != "alien_invasion" {
		t.Errorf("Name() = %v, want alien_invasion", ai.Name())
	}

	if ai.Duration() != 72*time.Hour {
		t.Errorf("Duration() = %v, want 72h", ai.Duration())
	}

	if ai.Description() == "" {
		t.Error("Description() should not be empty")
	}

	// Test timeline
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

	// Test characters
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

	if commander == nil {
		t.Fatal("Missing commander character")
	}
	if commander.Clearance != 5 {
		t.Errorf("Commander clearance = %d, want 5", commander.Clearance)
	}
	if commander.IsAdversary() {
		t.Error("Commander should not be adversary")
	}

	if scientist == nil {
		t.Fatal("Missing scientist character")
	}
	if scientist.Clearance != 4 {
		t.Errorf("Scientist clearance = %d, want 4", scientist.Clearance)
	}

	if eve == nil {
		t.Fatal("Missing eve character")
	}
	if !eve.IsAdversary() {
		t.Error("Eve should be adversary")
	}
	if eve.JoinTime != 12*time.Hour {
		t.Errorf("Eve JoinTime = %v, want 12h", eve.JoinTime)
	}
	if eve.LeaveTime != 30*time.Hour {
		t.Errorf("Eve LeaveTime = %v, want 30h", eve.LeaveTime)
	}

	// Test departments
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

	// Test document rules
	rules := ai.DocumentRules()
	if len(rules) < 15 {
		t.Errorf("DocumentRules() = %d, want at least 15 types", len(rules))
	}

	// Verify key document types exist
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

	// Check for key document types
	requiredTypes := []string{
		"battle_report", "sitrep", "autopsy", "scientific_analysis",
		"council_meeting", "telephone", "im_transcript", "casualty_list",
	}
	for _, reqType := range requiredTypes {
		if !types[reqType] {
			t.Errorf("Missing required document type: %s", reqType)
		}
	}

	// Verify expiring documents
	hasExpiring := false
	for _, rule := range rules {
		if rule.ExpiresIn > 0 {
			hasExpiring = true
			break
		}
	}
	if !hasExpiring {
		t.Error("No document rules with expiration for workflow testing")
	}
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
