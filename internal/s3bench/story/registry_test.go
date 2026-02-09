package story

import (
	"testing"
	"time"
)

type mockStory struct {
	name string
}

func (m *mockStory) Name() string                  { return m.name }
func (m *mockStory) Description() string           { return "mock story" }
func (m *mockStory) Duration() time.Duration       { return 1 * time.Hour }
func (m *mockStory) Timeline() []TimelineEvent     { return nil }
func (m *mockStory) Characters() []Character       { return nil }
func (m *mockStory) Departments() []Department     { return nil }
func (m *mockStory) DocumentRules() []DocumentRule { return nil }

func TestRegistryRegister(t *testing.T) {
	r := NewRegistry()

	// Test successful registration
	s1 := &mockStory{name: "test1"}
	if err := r.Register(s1); err != nil {
		t.Fatalf("Register(test1) failed: %v", err)
	}

	// Test duplicate registration
	if err := r.Register(s1); err == nil {
		t.Error("Register(duplicate) should fail")
	}

	// Test nil story
	if err := r.Register(nil); err == nil {
		t.Error("Register(nil) should fail")
	}

	// Test empty name
	s2 := &mockStory{name: ""}
	if err := r.Register(s2); err == nil {
		t.Error("Register(empty name) should fail")
	}
}

func TestRegistryGet(t *testing.T) {
	r := NewRegistry()
	s1 := &mockStory{name: "test1"}
	if err := r.Register(s1); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Test successful get
	got, err := r.Get("test1")
	if err != nil {
		t.Fatalf("Get(test1) failed: %v", err)
	}
	if got.Name() != "test1" {
		t.Errorf("Get(test1) returned wrong story: got %v, want test1", got.Name())
	}

	// Test missing story
	if _, err := r.Get("missing"); err == nil {
		t.Error("Get(missing) should fail")
	}
}

func TestRegistryList(t *testing.T) {
	r := NewRegistry()

	// Empty registry
	if got := r.List(); len(got) != 0 {
		t.Errorf("List() on empty registry = %v, want empty", got)
	}

	// Add stories
	s1 := &mockStory{name: "test1"}
	s2 := &mockStory{name: "test2"}
	_ = r.Register(s1)
	_ = r.Register(s2)

	got := r.List()
	if len(got) != 2 {
		t.Errorf("List() = %v, want 2 stories", len(got))
	}

	// Check both stories are present
	found := make(map[string]bool)
	for _, name := range got {
		found[name] = true
	}
	if !found["test1"] || !found["test2"] {
		t.Errorf("List() = %v, want test1 and test2", got)
	}
}
