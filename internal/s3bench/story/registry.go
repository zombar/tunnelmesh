package story

import (
	"fmt"
	"sync"
)

// Registry manages available story scenarios.
type Registry struct {
	mu      sync.RWMutex
	stories map[string]Story
}

// NewRegistry creates a new story registry.
func NewRegistry() *Registry {
	return &Registry{
		stories: make(map[string]Story),
	}
}

// Register adds a story to the registry.
func (r *Registry) Register(s Story) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if s == nil {
		return fmt.Errorf("cannot register nil story")
	}

	name := s.Name()
	if name == "" {
		return fmt.Errorf("story must have a name")
	}

	if _, exists := r.stories[name]; exists {
		return fmt.Errorf("story %q already registered", name)
	}

	r.stories[name] = s
	return nil
}

// Get retrieves a story by name.
func (r *Registry) Get(name string) (Story, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.stories[name]
	if !ok {
		return nil, fmt.Errorf("story %q not found", name)
	}

	return s, nil
}

// List returns all registered story names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.stories))
	for name := range r.stories {
		names = append(names, name)
	}
	return names
}

// DefaultRegistry is the global registry with built-in stories.
var DefaultRegistry = NewRegistry()
