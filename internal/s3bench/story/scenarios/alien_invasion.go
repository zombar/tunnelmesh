// Package scenarios contains built-in story scenarios for S3 stress testing.
package scenarios

import (
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
)

// AlienInvasion is a 72-hour scenario of first contact, invasion, and resistance.
type AlienInvasion struct{}

func (a *AlienInvasion) Name() string {
	return "alien_invasion"
}

func (a *AlienInvasion) Description() string {
	return "72 hours of first contact, invasion, and human resistance"
}

func (a *AlienInvasion) Duration() time.Duration {
	return 72 * time.Hour
}

func (a *AlienInvasion) Timeline() []story.TimelineEvent {
	return []story.TimelineEvent{
		// Phase 1: First Contact (0-24h)
		{Time: 0, Type: "incident", Description: "Radio silence from Mars colony", Severity: 2, Actors: []string{"scientist"}},
		{Time: 2 * time.Hour, Type: "incident", Description: "Massive unidentified objects detected near Jupiter", Severity: 3, Actors: []string{"commander", "scientist"}},
		{Time: 6 * time.Hour, Type: "escalation", Description: "Objects changing course toward Earth", Severity: 4, Actors: []string{"commander"}},
		{Time: 8 * time.Hour, Type: "workflow", Description: "Science file share hits quota", Severity: 2, Actors: []string{"scientist"}},
		{Time: 12 * time.Hour, Type: "security", Description: "Unauthorized access attempt detected", Severity: 3, Actors: []string{"eve"}},
		{Time: 16 * time.Hour, Type: "workflow", Description: "Classified battle report leaked", Severity: 4, Actors: []string{"commander", "eve"}},
		{Time: 18 * time.Hour, Type: "critical", Description: "Fleet enters Earth orbit - first contact imminent", Severity: 5, Actors: []string{"commander", "scientist"}},
		{Time: 20 * time.Hour, Type: "workflow", Description: "Permission request denied", Severity: 2, Actors: []string{"scientist", "commander"}},
		{Time: 24 * time.Hour, Type: "critical", Description: "First contact attempt - no response - they're here", Severity: 5, Actors: []string{"commander", "scientist"}},

		// Phase 2: Invasion (24-48h)
		{Time: 25 * time.Hour, Type: "critical", Description: "Global attacks commence - cities under siege", Severity: 5, Actors: []string{"commander"}},
		{Time: 26 * time.Hour, Type: "incident", Description: "First alien specimen recovered for autopsy", Severity: 4, Actors: []string{"scientist"}},
		{Time: 28 * time.Hour, Type: "escalation", Description: "Mass casualties reported - hospitals overwhelmed", Severity: 5, Actors: []string{"commander"}},
		{Time: 30 * time.Hour, Type: "security", Description: "Insider suddenly disconnects from mesh", Severity: 3, Actors: []string{"eve"}},
		{Time: 32 * time.Hour, Type: "incident", Description: "Evacuation orders issued for major population centers", Severity: 4, Actors: []string{"commander"}},
		{Time: 36 * time.Hour, Type: "workflow", Description: "Old meeting minutes pruned per retention policy", Severity: 1, Actors: []string{"commander"}},
		{Time: 40 * time.Hour, Type: "escalation", Description: "Alien communications partially decoded", Severity: 4, Actors: []string{"scientist"}},
		{Time: 48 * time.Hour, Type: "critical", Description: "Coordinated counterattack begins", Severity: 5, Actors: []string{"commander"}},

		// Phase 3: Resistance (48-72h)
		{Time: 50 * time.Hour, Type: "incident", Description: "First successful defense of major city", Severity: 4, Actors: []string{"commander"}},
		{Time: 54 * time.Hour, Type: "escalation", Description: "Alien weakness identified in autopsy findings", Severity: 4, Actors: []string{"scientist"}},
		{Time: 60 * time.Hour, Type: "incident", Description: "Supply lines re-established", Severity: 3, Actors: []string{"commander"}},
		{Time: 66 * time.Hour, Type: "escalation", Description: "Major alien fleet withdrawal detected", Severity: 4, Actors: []string{"commander", "scientist"}},
		{Time: 72 * time.Hour, Type: "resolution", Description: "48-hour ceasefire established - humanity survives", Severity: 3, Actors: []string{"commander", "scientist"}},
	}
}

func (a *AlienInvasion) Characters() []story.Character {
	return []story.Character{
		{
			ID:         "commander",
			Name:       "General Sarah Chen",
			Role:       "Military Commander",
			Department: "command",
			Clearance:  5, // Top Secret
			Alignment:  "good",
			DockerPeer: "alice",
			JoinTime:   0,
			LeaveTime:  0, // Stays entire scenario
		},
		{
			ID:         "scientist",
			Name:       "Dr. James Wright",
			Role:       "Lead Xenobiologist",
			Department: "science",
			Clearance:  4, // Secret
			Alignment:  "good",
			DockerPeer: "bob",
			JoinTime:   0,
			LeaveTime:  0, // Stays entire scenario
		},
		{
			ID:         "eve",
			Name:       "Eve Martinez",
			Role:       "Conspiracy Theorist",
			Department: "public",
			Clearance:  2, // Confidential
			Alignment:  "dark",
			DockerPeer: "eve",
			JoinTime:   12 * time.Hour, // Late arrival
			LeaveTime:  30 * time.Hour, // Sudden departure
		},
	}
}

func (a *AlienInvasion) Departments() []story.Department {
	return []story.Department{
		{
			ID:        "command",
			Name:      "Military Command",
			FileShare: "alien-command",
			Members:   []string{"commander"},
			QuotaMB:   1000,
			GuestRead: false,
		},
		{
			ID:        "science",
			Name:      "Scientific Research Division",
			FileShare: "alien-science",
			Members:   []string{"scientist"},
			QuotaMB:   500, // Lower quota to trigger workflow test
			GuestRead: true,
		},
		{
			ID:        "public",
			Name:      "Public Information",
			FileShare: "alien-public",
			Members:   []string{"commander", "scientist", "eve"},
			QuotaMB:   2000,
			GuestRead: true,
		},
		{
			ID:        "classified",
			Name:      "Classified Operations",
			FileShare: "alien-classified",
			Members:   []string{"commander"}, // Commander only
			QuotaMB:   0,                     // Unlimited
			GuestRead: false,
		},
	}
}

func (a *AlienInvasion) DocumentRules() []story.DocumentRule {
	return []story.DocumentRule{
		// Battle reports - frequent, high dedup
		{
			Type:        "battle_report",
			Frequency:   2 * time.Hour,
			SizeRange:   [2]int64{8000, 50000},
			Authors:     []string{"commander"},
			DataPattern: "compressible",
			Versions:    3, // Updated as battle progresses
		},
		// SITREPs - very frequent, highly structured (high compression)
		{
			Type:        "sitrep",
			Frequency:   1 * time.Hour,
			SizeRange:   [2]int64{3000, 15000},
			Authors:     []string{"commander"},
			DataPattern: "compressible",
			Versions:    2,
		},
		// Scientific analyses - periodic
		{
			Type:        "scientific_analysis",
			Frequency:   6 * time.Hour,
			SizeRange:   [2]int64{10000, 50000},
			Authors:     []string{"scientist"},
			DataPattern: "realistic",
			Versions:    2,
		},
		// Traffic reports - very frequent, shows escalation
		{
			Type:        "traffic_report",
			Frequency:   15 * time.Minute, // Doubled frequency
			SizeRange:   [2]int64{2000, 8000},
			Authors:     []string{"commander"},
			DataPattern: "compressible",
			Versions:    2,
		},
		// Council meetings - rare, very large
		{
			Type:        "council_meeting",
			Frequency:   18 * time.Hour, // 4 major meetings
			SizeRange:   [2]int64{30000, 120000},
			Authors:     []string{"commander"},
			DataPattern: "realistic",
			Versions:    2,
		},
		// Phone transcripts - frequent
		{
			Type:        "telephone",
			Frequency:   3 * time.Hour,
			SizeRange:   [2]int64{2000, 12000},
			Authors:     []string{"commander", "scientist"},
			DataPattern: "realistic",
			Versions:    2,
		},
		// IM transcripts - very frequent
		{
			Type:        "im_transcript",
			Frequency:   20 * time.Minute, // Much more frequent
			SizeRange:   [2]int64{1000, 8000},
			Authors:     []string{"commander", "scientist", "eve"},
			DataPattern: "realistic",
			Versions:    2,
		},
		// Casualty lists - periodic, growing
		{
			Type:        "casualty_list",
			Frequency:   4 * time.Hour,
			SizeRange:   [2]int64{5000, 25000},
			Authors:     []string{"commander"},
			DataPattern: "compressible",
			Versions:    2,
		},
		// Supply manifests - daily
		{
			Type:        "supply_manifest",
			Frequency:   24 * time.Hour,
			SizeRange:   [2]int64{3000, 15000},
			Authors:     []string{"commander"},
			DataPattern: "compressible",
			Versions:    4, // Updated as supplies deplete
		},
		// Intel briefs - periodic
		{
			Type:        "intel_brief",
			Frequency:   6 * time.Hour,
			SizeRange:   [2]int64{8000, 35000},
			Authors:     []string{"commander"},
			DataPattern: "realistic",
			Versions:    2,
		},
		// Radio logs - frequent, dramatic
		{
			Type:        "radio_log",
			Frequency:   2 * time.Hour,
			SizeRange:   [2]int64{4000, 25000},
			Authors:     []string{"commander"},
			DataPattern: "realistic",
			Versions:    2,
		},
		// Press releases - periodic
		{
			Type:        "press_release",
			Frequency:   8 * time.Hour,
			SizeRange:   [2]int64{1000, 5000},
			Authors:     []string{"commander"},
			DataPattern: "compressible",
			Versions:    2,
		},
		// Private diaries - frequent, personal
		{
			Type:        "private_diary",
			Frequency:   4 * time.Hour,
			SizeRange:   [2]int64{1500, 8000},
			Authors:     []string{"scientist", "eve"},
			DataPattern: "realistic",
			Versions:    2,
		},
		// Hospital records - frequent
		{
			Type:        "hospital_records",
			Frequency:   30 * time.Minute, // Much more frequent
			SizeRange:   [2]int64{3000, 15000},
			Authors:     []string{"scientist"},
			DataPattern: "realistic",
			Versions:    2,
		},
		// Lab results - frequent
		{
			Type:        "lab_results",
			Frequency:   3 * time.Hour,
			SizeRange:   [2]int64{2000, 12000},
			Authors:     []string{"scientist"},
			DataPattern: "compressible",
			Versions:    2,
		},
		// Field notes - frequent
		{
			Type:        "field_notes",
			Frequency:   3 * time.Hour,
			SizeRange:   [2]int64{1000, 6000},
			Authors:     []string{"commander"},
			DataPattern: "realistic",
			Versions:    2,
		},
		// Drone footage metadata - very frequent
		{
			Type:        "drone_footage",
			Frequency:   30 * time.Minute, // More frequent
			SizeRange:   [2]int64{800, 4000},
			Authors:     []string{"commander"},
			DataPattern: "compressible",
			Versions:    2,
		},
		// Status updates - very frequent, brief
		{
			Type:        "status_update",
			Frequency:   20 * time.Minute,
			SizeRange:   [2]int64{500, 3000},
			Authors:     []string{"commander", "scientist"},
			DataPattern: "compressible",
			Versions:    2,
		},
		// Incident reports - frequent
		{
			Type:        "incident_report",
			Frequency:   40 * time.Minute,
			SizeRange:   [2]int64{2000, 10000},
			Authors:     []string{"commander"},
			DataPattern: "realistic",
			Versions:    2,
		},
		// Sensor readings - very frequent
		{
			Type:        "sensor_reading",
			Frequency:   25 * time.Minute,
			SizeRange:   [2]int64{1000, 5000},
			Authors:     []string{"scientist"},
			DataPattern: "compressible",
			Versions:    2,
		},
		// Social media posts - extremely frequent
		{
			Type:        "social_media",
			Frequency:   8 * time.Minute, // Even more frequent
			SizeRange:   [2]int64{300, 2000},
			Authors:     []string{"eve"},
			DataPattern: "realistic",
			Versions:    2,
		},
		// Email messages - extremely frequent
		{
			Type:        "email",
			Frequency:   12 * time.Minute,
			SizeRange:   [2]int64{500, 3000},
			Authors:     []string{"commander", "scientist", "eve"},
			DataPattern: "realistic",
			Versions:    2,
		},
		// Alert messages - very frequent
		{
			Type:        "alert_message",
			Frequency:   15 * time.Minute,
			SizeRange:   [2]int64{400, 2000},
			Authors:     []string{"commander"},
			DataPattern: "compressible",
			Versions:    2,
		},
		// News bulletins - frequent
		{
			Type:        "news_bulletin",
			Frequency:   30 * time.Minute,
			SizeRange:   [2]int64{1000, 5000},
			Authors:     []string{"commander"},
			DataPattern: "compressible",
			Versions:    2,
		},
		// Logistics reports - frequent
		{
			Type:        "logistics_report",
			Frequency:   45 * time.Minute,
			SizeRange:   [2]int64{2000, 8000},
			Authors:     []string{"commander"},
			DataPattern: "compressible",
			Versions:    2,
		},
		// Personnel status - frequent
		{
			Type:        "personnel_status",
			Frequency:   35 * time.Minute,
			SizeRange:   [2]int64{1500, 6000},
			Authors:     []string{"commander"},
			DataPattern: "compressible",
			Versions:    2,
		},
		// Weather/environmental reports - frequent
		{
			Type:        "environmental_report",
			Frequency:   40 * time.Minute,
			SizeRange:   [2]int64{1000, 4000},
			Authors:     []string{"scientist"},
			DataPattern: "compressible",
			Versions:    2,
		},
		// Intercepted communications - rare, mysterious
		{
			Type:        "intercepted_comms",
			Frequency:   12 * time.Hour,
			SizeRange:   [2]int64{2000, 35000},
			Authors:     []string{"scientist"},
			DataPattern: "random", // Alien data
			Versions:    2,        // Decoded over time
		},
		// Evacuation orders - periodic, with expiration
		{
			Type:        "evacuation_order",
			Frequency:   8 * time.Hour,
			SizeRange:   [2]int64{2000, 7000},
			Authors:     []string{"commander"},
			DataPattern: "compressible",
			Versions:    2,
			ExpiresIn:   6 * time.Hour, // Orders expire after 6h
		},
	}
}

func init() {
	// Register alien invasion scenario with default registry
	_ = story.DefaultRegistry.Register(&AlienInvasion{})
}
