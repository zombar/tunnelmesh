package simulator

import (
	"context"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
)

// mockStory is a simple story for testing workload generation.
type mockStory struct{}

func (m *mockStory) Name() string {
	return "mock_story"
}

func (m *mockStory) Description() string {
	return "Mock story for testing"
}

func (m *mockStory) Duration() time.Duration {
	return 10 * time.Hour
}

func (m *mockStory) Timeline() []story.TimelineEvent {
	return []story.TimelineEvent{
		{Time: 0, Type: "incident", Description: "Start", Severity: 1},
		{Time: 5 * time.Hour, Type: "escalation", Description: "Middle", Severity: 3},
		{Time: 10 * time.Hour, Type: "resolution", Description: "End", Severity: 2},
	}
}

func (m *mockStory) Characters() []story.Character {
	return []story.Character{
		{
			ID:         "alice",
			Name:       "Alice",
			Role:       "Commander",
			Department: "command",
			Clearance:  5,
			Alignment:  "good",
			DockerPeer: "alice",
			JoinTime:   0,
			LeaveTime:  0, // Stays entire scenario
		},
		{
			ID:         "bob",
			Name:       "Bob",
			Role:       "Analyst",
			Department: "intel",
			Clearance:  3,
			Alignment:  "good",
			DockerPeer: "bob",
			JoinTime:   2 * time.Hour, // Joins later
			LeaveTime:  8 * time.Hour, // Leaves before end
		},
	}
}

func (m *mockStory) Departments() []story.Department {
	return []story.Department{
		{
			ID:        "command",
			Name:      "Command",
			FileShare: "cmd-share",
			Members:   []string{"alice"},
			QuotaMB:   100,
			GuestRead: false,
		},
		{
			ID:        "intel",
			Name:      "Intelligence",
			FileShare: "intel-share",
			Members:   []string{"bob"},
			QuotaMB:   50,
			GuestRead: true,
		},
	}
}

func (m *mockStory) DocumentRules() []story.DocumentRule {
	return []story.DocumentRule{
		{
			Type:        "battle_report",
			Frequency:   2 * time.Hour, // 5 documents over 10h
			SizeRange:   [2]int64{1000, 5000},
			Authors:     []string{"alice"},
			DataPattern: "realistic",
			Versions:    1,
		},
		{
			Type:        "sitrep",
			Frequency:   1 * time.Hour, // 10 documents over 10h
			SizeRange:   [2]int64{500, 2000},
			Authors:     []string{"alice", "bob"},
			DataPattern: "compressible",
			Versions:    2, // Each sitrep has 2 versions
		},
		{
			Type:        "evacuation_order",
			Frequency:   3 * time.Hour, // 3 documents over 10h
			SizeRange:   [2]int64{500, 1000},
			Authors:     []string{"alice"},
			DataPattern: "realistic",
			Versions:    1,
			DeleteAfter: 2 * time.Hour, // Deleted 2h after creation
		},
	}
}

func TestWorkloadGenerator_GenerateWorkload(t *testing.T) {
	mock := &mockStory{}
	gen := NewWorkloadGenerator(mock, 1.0) // 1x time scale

	tasks, err := gen.GenerateWorkload(context.Background())
	if err != nil {
		t.Fatalf("GenerateWorkload() error = %v", err)
	}

	// Calculate expected tasks:
	// - battle_reports: 5 uploads (every 2h: 0, 2, 4, 6, 8)
	// - sitreps: 10 uploads + 10 updates (every 1h: 0-9h, but bob only active 2h-8h)
	//   - alice sitreps: at 0, 2, 4, 6, 8 (5 sitreps * 2 versions = 10 tasks)
	//   - bob sitreps: at 3, 5, 7 (3 sitreps * 2 versions = 6 tasks, only generated during 2h-8h)
	// - evacuation_orders: 3 uploads + 3 deletes (every 3h: 0, 3, 6, with deletes at 2, 5, 8)
	//
	// Total: 5 + 16 + 6 = 27 tasks

	if len(tasks) == 0 {
		t.Fatal("No tasks generated")
	}

	t.Logf("Generated %d tasks", len(tasks))

	// Verify tasks are sorted by time
	for i := 1; i < len(tasks); i++ {
		if tasks[i].RealTime < tasks[i-1].RealTime {
			t.Errorf("Tasks not sorted: task[%d].RealTime=%v > task[%d].RealTime=%v",
				i-1, tasks[i-1].RealTime, i, tasks[i].RealTime)
		}
	}

	// Count operations by type
	opCounts := make(map[string]int)
	for _, task := range tasks {
		opCounts[task.Operation]++
	}

	t.Logf("Operation counts: %+v", opCounts)

	// Verify we have uploads
	if opCounts["upload"] == 0 {
		t.Error("Expected upload operations")
	}

	// Verify we have updates (from briefs with 2 versions)
	if opCounts["update"] == 0 {
		t.Error("Expected update operations from versioned documents")
	}

	// Verify we have deletes (from temp_docs)
	if opCounts["delete"] == 0 {
		t.Error("Expected delete operations from DeleteAfter rule")
	}

	// Verify all tasks have required fields
	for i, task := range tasks {
		if task.TaskID == "" {
			t.Errorf("Task %d missing TaskID", i)
		}
		if task.DocType == "" {
			t.Errorf("Task %d missing DocType", i)
		}
		if task.Filename == "" && task.Operation != "delete" {
			t.Errorf("Task %d missing Filename (operation=%s)", i, task.Operation)
		}
		if task.FileShare == "" {
			t.Errorf("Task %d missing FileShare", i)
		}
		if task.Author.ID == "" {
			t.Errorf("Task %d missing Author", i)
		}
		if task.Operation == "upload" || task.Operation == "update" {
			if len(task.Content) == 0 {
				t.Errorf("Task %d (operation=%s) missing Content", i, task.Operation)
			}
		}
	}
}

func TestWorkloadGenerator_TimeScaling(t *testing.T) {
	mock := &mockStory{}

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
			gen := NewWorkloadGenerator(mock, tc.timeScale)
			tasks, err := gen.GenerateWorkload(context.Background())
			if err != nil {
				t.Fatalf("GenerateWorkload() error = %v", err)
			}

			// Verify time scaling is applied
			for _, task := range tasks {
				expectedRealTime := story.ScaledDuration(task.StoryTime, tc.timeScale)
				if task.RealTime != expectedRealTime {
					t.Errorf("Task %s: RealTime=%v, want %v (StoryTime=%v, scale=%v)",
						task.TaskID, task.RealTime, expectedRealTime, task.StoryTime, tc.timeScale)
				}
			}

			// Verify fastest task is at time 0
			if len(tasks) > 0 && tasks[0].RealTime != 0 {
				t.Errorf("First task RealTime = %v, want 0", tasks[0].RealTime)
			}

			// Verify last task time matches scaled story duration
			if len(tasks) > 0 {
				lastTask := tasks[len(tasks)-1]
				maxRealTime := story.ScaledDuration(mock.Duration(), tc.timeScale)
				if lastTask.RealTime > maxRealTime {
					t.Errorf("Last task RealTime=%v exceeds scaled duration=%v",
						lastTask.RealTime, maxRealTime)
				}
			}
		})
	}
}

func TestWorkloadGenerator_AuthorJoinLeave(t *testing.T) {
	mock := &mockStory{}
	gen := NewWorkloadGenerator(mock, 1.0)

	tasks, err := gen.GenerateWorkload(context.Background())
	if err != nil {
		t.Fatalf("GenerateWorkload() error = %v", err)
	}

	// Bob joins at 2h and leaves at 8h
	// Verify no write tasks from Bob before 2h or after 8h
	// (download tasks are excluded since they're reads that can happen anytime)
	for _, task := range tasks {
		if task.Author.ID == "bob" && task.Operation != "download" {
			if task.StoryTime < 2*time.Hour {
				t.Errorf("Bob %s task at %v (before join time 2h)", task.Operation, task.StoryTime)
			}
			if task.StoryTime >= 8*time.Hour {
				t.Errorf("Bob %s task at %v (after leave time 8h)", task.Operation, task.StoryTime)
			}
		}
	}

	// Verify Alice (who stays entire scenario) has write tasks throughout
	aliceTasks := 0
	for _, task := range tasks {
		if task.Author.ID == "alice" && task.Operation != "download" {
			aliceTasks++
		}
	}
	if aliceTasks == 0 {
		t.Error("Alice should have tasks throughout scenario")
	}
}

func TestWorkloadGenerator_Versioning(t *testing.T) {
	mock := &mockStory{}
	gen := NewWorkloadGenerator(mock, 1.0)

	tasks, err := gen.GenerateWorkload(context.Background())
	if err != nil {
		t.Fatalf("GenerateWorkload() error = %v", err)
	}

	// Find sitrep upload/update tasks (which have 2 versions)
	// Exclude download tasks which are randomly generated
	sitrepsByFilename := make(map[string][]WorkloadTask)
	for _, task := range tasks {
		if task.DocType == "sitrep" && (task.Operation == "upload" || task.Operation == "update") {
			sitrepsByFilename[task.Filename] = append(sitrepsByFilename[task.Filename], task)
		}
	}

	// Each sitrep should have 2 tasks (upload + update)
	for filename, fileTasks := range sitrepsByFilename {
		if len(fileTasks) != 2 {
			t.Errorf("Sitrep %s has %d upload/update tasks, want 2", filename, len(fileTasks))
			continue
		}

		// First should be upload
		if fileTasks[0].Operation != "upload" {
			t.Errorf("Sitrep %s first operation = %s, want upload", filename, fileTasks[0].Operation)
		}
		if fileTasks[0].VersionNum != 1 {
			t.Errorf("Sitrep %s first version = %d, want 1", filename, fileTasks[0].VersionNum)
		}

		// Second should be update
		if fileTasks[1].Operation != "update" {
			t.Errorf("Sitrep %s second operation = %s, want update", filename, fileTasks[1].Operation)
		}
		if fileTasks[1].VersionNum != 2 {
			t.Errorf("Sitrep %s second version = %d, want 2", filename, fileTasks[1].VersionNum)
		}

		// Update should be after upload
		if fileTasks[1].RealTime <= fileTasks[0].RealTime {
			t.Errorf("Sitrep %s update time %v not after upload time %v",
				filename, fileTasks[1].RealTime, fileTasks[0].RealTime)
		}
	}
}

func TestWorkloadGenerator_Deletion(t *testing.T) {
	mock := &mockStory{}
	gen := NewWorkloadGenerator(mock, 1.0)

	tasks, err := gen.GenerateWorkload(context.Background())
	if err != nil {
		t.Fatalf("GenerateWorkload() error = %v", err)
	}

	// Find evacuation_order upload/delete tasks (which have DeleteAfter=2h)
	// Exclude download tasks which are randomly generated and don't affect the upload+delete pairing
	evacOrdersByFilename := make(map[string][]WorkloadTask)
	for _, task := range tasks {
		if task.DocType == "evacuation_order" && (task.Operation == "upload" || task.Operation == "delete") {
			evacOrdersByFilename[task.Filename] = append(evacOrdersByFilename[task.Filename], task)
		}
	}

	// Each evacuation_order should have 2 tasks (upload + delete)
	for filename, fileTasks := range evacOrdersByFilename {
		if len(fileTasks) != 2 {
			t.Errorf("Evacuation order %s has %d upload/delete tasks, want 2", filename, len(fileTasks))
			continue
		}

		// First should be upload
		if fileTasks[0].Operation != "upload" {
			t.Errorf("Evacuation order %s first operation = %s, want upload", filename, fileTasks[0].Operation)
		}

		// Second should be delete
		if fileTasks[1].Operation != "delete" {
			t.Errorf("Evacuation order %s second operation = %s, want delete", filename, fileTasks[1].Operation)
		}

		// Delete should be 2h after upload
		expectedDeleteTime := fileTasks[0].StoryTime + 2*time.Hour
		if fileTasks[1].StoryTime != expectedDeleteTime {
			t.Errorf("Evacuation order %s delete time = %v, want %v (2h after upload)",
				filename, fileTasks[1].StoryTime, expectedDeleteTime)
		}
	}
}
