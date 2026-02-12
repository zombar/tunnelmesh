// Package story defines story-driven stress test scenarios with characters, timelines, and document generation rules.
package story

import (
	"math"
	"time"
)

// Story defines a narrative scenario for stress testing the S3 system.
// Stories include characters, timeline events, departments (file shares), and document generation rules.
type Story interface {
	// Name returns the story identifier (e.g., "alien_invasion")
	Name() string

	// Description returns a human-readable description of the story
	Description() string

	// Duration returns the total story duration (e.g., 72h for alien invasion)
	Duration() time.Duration

	// Timeline returns the sequence of story events
	Timeline() []TimelineEvent

	// Characters returns all characters (users) in the story
	Characters() []Character

	// Departments returns all departments (file shares) in the story
	Departments() []Department

	// DocumentRules returns rules for document generation
	DocumentRules() []DocumentRule
}

// TimelineEvent represents a significant event in the story timeline.
type TimelineEvent struct {
	// Time is when this event occurs relative to story start
	Time time.Duration

	// Type categorizes the event (e.g., "incident", "escalation", "critical")
	Type string

	// Description is the narrative description of the event
	Description string

	// Severity is 1-5, used for color-coding and priority
	Severity int

	// Actors are character IDs involved in this event
	Actors []string
}

// Character represents a user in the story with roles, clearance, and mesh participation timing.
type Character struct {
	// ID is the unique character identifier (e.g., "commander", "scientist", "eve")
	ID string

	// Name is the character's full name (e.g., "General Sarah Chen")
	Name string

	// Role describes their function (e.g., "commander", "scientist", "medic", "adversary")
	Role string

	// Department is the primary department this character belongs to
	Department string

	// Clearance is security level 1-5 (1=public, 5=top secret)
	Clearance int

	// Alignment is "good" for normal users or "dark" for adversaries
	Alignment string

	// DockerPeer is the Docker container name to use (e.g., "alice", "bob", "eve")
	DockerPeer string

	// JoinTime is when the character's peer joins the mesh (0 = start immediately)
	JoinTime time.Duration

	// LeaveTime is when the character's peer leaves the mesh (0 = never leaves)
	LeaveTime time.Duration
}

// IsAdversary returns true if this character is a dark character.
func (c Character) IsAdversary() bool {
	return c.Alignment == "dark"
}

// Department represents a file share with members, quotas, and permissions.
type Department struct {
	// ID is the unique department identifier
	ID string

	// Name is the department's full name
	Name string

	// FileShare is the file share name to create
	FileShare string

	// Members are character IDs with access to this department
	Members []string

	// QuotaMB is the storage quota in megabytes (0 = unlimited)
	QuotaMB int64

	// GuestRead enables public read access
	GuestRead bool
}

// DocumentRule defines when and how to generate documents during the story.
type DocumentRule struct {
	// Type is the document type (e.g., "report", "autopsy", "transcript")
	Type string

	// Frequency is how often to generate this document type
	Frequency time.Duration

	// SizeRange defines min/max bytes for generated documents [min, max]
	SizeRange [2]int64

	// Authors are character IDs who can author this document type
	Authors []string

	// DataPattern is "random", "compressible", or "realistic"
	DataPattern string

	// Versions is how many versions/edits to create for each document
	Versions int

	// DeleteAfter specifies when to delete the document (0 = never)
	DeleteAfter time.Duration

	// ExpiresIn sets document expiration time (0 = never expires)
	ExpiresIn time.Duration

	// ShareWith are character IDs to request file share access with
	ShareWith []string

	// FileShare explicitly specifies which file share to write to (optional, defaults to author's department)
	FileShare string
}

// Config holds configuration for running a story scenario.
type Config struct {
	// Story is the story name to run
	Story string

	// Concurrency is number of parallel users/operations
	Concurrency int

	// TimeScale speeds up the scenario (1.0=realtime, 100.0=100x faster, 4320.0=72h→1min)
	TimeScale float64

	// EnableAudit enables audit logging
	EnableAudit bool

	// EnableMesh enables Docker mesh orchestration
	EnableMesh bool

	// EnableDark enables adversarial character behavior
	EnableDark bool

	// TestDeletion enables document deletion workflow testing
	TestDeletion bool

	// TestExpiration enables document expiration workflow testing
	TestExpiration bool

	// TestPermissions enables file share permission request testing
	TestPermissions bool

	// QuotaOverride overrides default file share quotas (0 = use story defaults)
	QuotaOverride int64

	// ExpiryOverride overrides document expiration times (0 = use story defaults)
	// Note: This is in story time and gets scaled by TimeScale
	ExpiryOverride time.Duration

	// RetentionOverride overrides version retention policy
	RetentionOverride *VersionRetentionPolicy
}

// VersionRetentionPolicy defines how many versions to keep.
type VersionRetentionPolicy struct {
	// RecentDays keeps all versions from the last N days
	RecentDays int

	// WeeklyWeeks keeps one version per week for N weeks
	WeeklyWeeks int

	// MonthlyMonths keeps one version per month for N months
	MonthlyMonths int
}

// ScaledDuration returns the real-time duration for a story duration given the time scale.
// Example: ScaledDuration(12h, 100.0) = 7.2 minutes
func ScaledDuration(storyDuration time.Duration, timeScale float64) time.Duration {
	if timeScale <= 0 {
		timeScale = 1.0
	}

	// Calculate scaled duration with overflow protection
	scaled := float64(storyDuration) / timeScale

	// Check for overflow (time.Duration is int64)
	if scaled > float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	if scaled < float64(math.MinInt64) {
		return time.Duration(math.MinInt64)
	}

	return time.Duration(scaled)
}

// Context provides contextual information for document generation.
type Context struct {
	// Story is the current story being executed
	Story Story

	// Event is the current timeline event (may be nil)
	Event *TimelineEvent

	// Author is the character authoring the document
	Author Character

	// Timestamp is the current story time
	Timestamp time.Time

	// StoryElapsed is how much story time has elapsed
	StoryElapsed time.Duration

	// Data is arbitrary context data for template expansion
	Data map[string]interface{}
}
