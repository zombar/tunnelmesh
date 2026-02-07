// Package context provides management of multiple TunnelMesh configurations.
package context

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Context represents a named mesh configuration.
type Context struct {
	Name       string `json:"name"`
	ConfigPath string `json:"config_path"`
	Server     string `json:"server,omitempty"`
	Domain     string `json:"domain,omitempty"`
	MeshIP     string `json:"mesh_ip,omitempty"`
	DNSListen  string `json:"dns_listen,omitempty"`
}

// ServiceName returns the derived service name for this context.
// Convention: "default" -> "tunnelmesh", others -> "tunnelmesh-{name}"
func (c *Context) ServiceName() string {
	if c.Name == "default" {
		return "tunnelmesh"
	}
	return "tunnelmesh-" + c.Name
}

// Store manages multiple contexts and tracks the active one.
type Store struct {
	Active   string             `json:"active"`
	Contexts map[string]Context `json:"contexts"`

	path string // file path for persistence
}

// DefaultStorePath returns the default path for the context store.
func DefaultStorePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}
	return filepath.Join(homeDir, ".tunnelmesh", ".context"), nil
}

// Load reads the context store from the default location.
// Returns an empty store if the file doesn't exist.
func Load() (*Store, error) {
	path, err := DefaultStorePath()
	if err != nil {
		return nil, err
	}
	return LoadFromPath(path)
}

// LoadFromPath reads the context store from a specific path.
// Returns an empty store if the file doesn't exist.
func LoadFromPath(path string) (*Store, error) {
	store := &Store{
		Contexts: make(map[string]Context),
		path:     path,
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read context file: %w", err)
	}

	if err := json.Unmarshal(data, store); err != nil {
		return nil, fmt.Errorf("parse context file: %w", err)
	}

	// Ensure contexts map is initialized
	if store.Contexts == nil {
		store.Contexts = make(map[string]Context)
	}

	store.path = path
	return store, nil
}

// Save writes the context store to disk.
func (s *Store) Save() error {
	// Ensure directory exists
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create context directory: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal context store: %w", err)
	}

	if err := os.WriteFile(s.path, data, 0600); err != nil {
		return fmt.Errorf("write context file: %w", err)
	}

	return nil
}

// Add adds or updates a context in the store.
func (s *Store) Add(ctx Context) {
	if s.Contexts == nil {
		s.Contexts = make(map[string]Context)
	}
	s.Contexts[ctx.Name] = ctx
}

// Get returns a context by name, or nil if not found.
func (s *Store) Get(name string) *Context {
	if ctx, ok := s.Contexts[name]; ok {
		return &ctx
	}
	return nil
}

// Remove deletes a context from the store.
// If the removed context was active, clears the active context.
func (s *Store) Remove(name string) {
	delete(s.Contexts, name)
	if s.Active == name {
		s.Active = ""
	}
}

// SetActive sets the active context by name.
// Returns an error if the context doesn't exist.
func (s *Store) SetActive(name string) error {
	if _, ok := s.Contexts[name]; !ok {
		return fmt.Errorf("context %q not found", name)
	}
	s.Active = name
	return nil
}

// GetActive returns the currently active context, or nil if none.
func (s *Store) GetActive() *Context {
	if s.Active == "" {
		return nil
	}
	return s.Get(s.Active)
}

// List returns all contexts sorted by name.
func (s *Store) List() []Context {
	contexts := make([]Context, 0, len(s.Contexts))
	for _, ctx := range s.Contexts {
		contexts = append(contexts, ctx)
	}
	sort.Slice(contexts, func(i, j int) bool {
		return contexts[i].Name < contexts[j].Name
	})
	return contexts
}

// Count returns the number of contexts.
func (s *Store) Count() int {
	return len(s.Contexts)
}

// IsEmpty returns true if there are no contexts.
func (s *Store) IsEmpty() bool {
	return len(s.Contexts) == 0
}

// HasActive returns true if there is an active context set.
func (s *Store) HasActive() bool {
	return s.Active != "" && s.Get(s.Active) != nil
}
