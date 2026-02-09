package story

import (
	"testing"
	"time"
)

func TestScaledDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		scale    float64
		want     time.Duration
	}{
		{"realtime", 72 * time.Hour, 1.0, 72 * time.Hour},
		{"10x faster", 72 * time.Hour, 10.0, 432 * time.Minute},
		{"100x faster", 72 * time.Hour, 100.0, 43*time.Minute + 12*time.Second},
		{"4320x fastest", 72 * time.Hour, 4320.0, 60 * time.Second},
		{"1h at 100x", 1 * time.Hour, 100.0, 36 * time.Second},
		{"6h at 100x", 6 * time.Hour, 100.0, 216 * time.Second},
		{"zero scale fallback", 1 * time.Hour, 0, 1 * time.Hour},
		{"negative scale fallback", 1 * time.Hour, -5.0, 1 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScaledDuration(tt.duration, tt.scale)
			// Allow 1 second tolerance for floating point rounding
			diff := got - tt.want
			if diff < 0 {
				diff = -diff
			}
			if diff > time.Second {
				t.Errorf("ScaledDuration(%v, %v) = %v, want %v", tt.duration, tt.scale, got, tt.want)
			}
		})
	}
}

func TestCharacterIsAdversary(t *testing.T) {
	tests := []struct {
		name      string
		character Character
		want      bool
	}{
		{
			name:      "good character",
			character: Character{ID: "alice", Alignment: "good"},
			want:      false,
		},
		{
			name:      "dark character",
			character: Character{ID: "eve", Alignment: "dark"},
			want:      true,
		},
		{
			name:      "no alignment",
			character: Character{ID: "bob", Alignment: ""},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.character.IsAdversary(); got != tt.want {
				t.Errorf("Character.IsAdversary() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestContext(t *testing.T) {
	event := &TimelineEvent{
		Time:        24 * time.Hour,
		Type:        "critical",
		Description: "Test event",
		Severity:    5,
	}

	character := Character{
		ID:   "commander",
		Name: "General Sarah Chen",
	}

	ctx := Context{
		Event:        event,
		Author:       character,
		Timestamp:    time.Now(),
		StoryElapsed: 24 * time.Hour,
		Data:         map[string]interface{}{"test": "value"},
	}

	if ctx.Event.Description != "Test event" {
		t.Errorf("Context.Event.Description = %v, want 'Test event'", ctx.Event.Description)
	}

	if ctx.Author.Name != "General Sarah Chen" {
		t.Errorf("Context.Author.Name = %v, want 'General Sarah Chen'", ctx.Author.Name)
	}

	if ctx.Data["test"] != "value" {
		t.Errorf("Context.Data[test] = %v, want 'value'", ctx.Data["test"])
	}
}
