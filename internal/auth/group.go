package auth

import (
	"errors"
	"sync"
	"time"
)

// Built-in group names.
const (
	GroupEveryone      = "everyone"        // All registered peers (peer = user)
	GroupAllAdminUsers = "all_admin_users" // All peers with admin role
)

// Group errors.
var (
	ErrGroupExists   = errors.New("group already exists")
	ErrGroupNotFound = errors.New("group not found")
	ErrBuiltinGroup  = errors.New("cannot modify built-in group")
)

// Group represents a collection of peers that can be assigned roles together.
type Group struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Members     []string  `json:"members"`
	CreatedAt   time.Time `json:"created_at"`
	Builtin     bool      `json:"builtin,omitempty"`
}

// NewGroup creates a new group with the given name and description.
func NewGroup(name, description string) *Group {
	return &Group{
		Name:        name,
		Description: description,
		Members:     []string{},
		CreatedAt:   time.Now().UTC(),
		Builtin:     false,
	}
}

// GroupStore manages groups in memory.
type GroupStore struct {
	groups map[string]*Group
	mu     sync.RWMutex
}

// NewGroupStore creates a new group store with built-in groups.
func NewGroupStore() *GroupStore {
	store := &GroupStore{
		groups: make(map[string]*Group),
	}

	// Initialize built-in groups
	store.groups[GroupEveryone] = &Group{
		Name:        GroupEveryone,
		Description: "All registered peers",
		Members:     []string{},
		CreatedAt:   time.Now().UTC(),
		Builtin:     true,
	}
	store.groups[GroupAllAdminUsers] = &Group{
		Name:        GroupAllAdminUsers,
		Description: "All admin peers",
		Members:     []string{},
		CreatedAt:   time.Now().UTC(),
		Builtin:     true,
	}

	return store
}

// Create creates a new group.
func (gs *GroupStore) Create(name, description string) (*Group, error) {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	if _, exists := gs.groups[name]; exists {
		return nil, ErrGroupExists
	}

	group := NewGroup(name, description)
	gs.groups[name] = group
	return group, nil
}

// Get returns a group by name, or nil if not found.
func (gs *GroupStore) Get(name string) *Group {
	gs.mu.RLock()
	defer gs.mu.RUnlock()

	return gs.groups[name]
}

// Delete removes a group by name.
func (gs *GroupStore) Delete(name string) error {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	group, exists := gs.groups[name]
	if !exists {
		return ErrGroupNotFound
	}

	if group.Builtin {
		return ErrBuiltinGroup
	}

	delete(gs.groups, name)
	return nil
}

// AddMember adds a peer to a group.
func (gs *GroupStore) AddMember(groupName, peerID string) error {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	group, exists := gs.groups[groupName]
	if !exists {
		return ErrGroupNotFound
	}

	// Check if already a member
	for _, m := range group.Members {
		if m == peerID {
			return nil // Already a member, idempotent
		}
	}

	group.Members = append(group.Members, peerID)
	return nil
}

// RemoveMember removes a peer from a group.
func (gs *GroupStore) RemoveMember(groupName, peerID string) error {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	group, exists := gs.groups[groupName]
	if !exists {
		return ErrGroupNotFound
	}

	// Find and remove the member
	for i, m := range group.Members {
		if m == peerID {
			group.Members = append(group.Members[:i], group.Members[i+1:]...)
			return nil
		}
	}

	return nil // Not a member, idempotent
}

// GetGroupsForPeer returns the names of all groups a peer belongs to.
func (gs *GroupStore) GetGroupsForPeer(peerID string) []string {
	gs.mu.RLock()
	defer gs.mu.RUnlock()

	var groups []string
	for name, group := range gs.groups {
		for _, member := range group.Members {
			if member == peerID {
				groups = append(groups, name)
				break
			}
		}
	}
	return groups
}

// IsMember checks if a peer is a member of a group.
func (gs *GroupStore) IsMember(groupName, peerID string) bool {
	gs.mu.RLock()
	defer gs.mu.RUnlock()

	group, exists := gs.groups[groupName]
	if !exists {
		return false
	}

	for _, m := range group.Members {
		if m == peerID {
			return true
		}
	}
	return false
}

// List returns all groups.
func (gs *GroupStore) List() []*Group {
	gs.mu.RLock()
	defer gs.mu.RUnlock()

	result := make([]*Group, 0, len(gs.groups))
	for _, g := range gs.groups {
		result = append(result, g)
	}
	return result
}

// LoadGroups loads groups from a slice (e.g., from JSON).
// This replaces existing groups while preserving built-in group structure.
func (gs *GroupStore) LoadGroups(groups []*Group) {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	// Reset to fresh built-in groups
	gs.groups = make(map[string]*Group)
	gs.groups[GroupEveryone] = &Group{
		Name:        GroupEveryone,
		Description: "All registered peers",
		Members:     []string{},
		CreatedAt:   time.Now().UTC(),
		Builtin:     true,
	}
	gs.groups[GroupAllAdminUsers] = &Group{
		Name:        GroupAllAdminUsers,
		Description: "All admin peers",
		Members:     []string{},
		CreatedAt:   time.Now().UTC(),
		Builtin:     true,
	}

	// Load groups from saved data
	for _, g := range groups {
		if existing, ok := gs.groups[g.Name]; ok && existing.Builtin {
			// For built-in groups, just update members
			existing.Members = g.Members
		} else {
			gs.groups[g.Name] = g
		}
	}
}

// RemovePeerFromAllGroups removes a peer from all groups.
// Used when expiring a peer account.
func (gs *GroupStore) RemovePeerFromAllGroups(peerID string) {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	for _, group := range gs.groups {
		for i, m := range group.Members {
			if m == peerID {
				group.Members = append(group.Members[:i], group.Members[i+1:]...)
				break
			}
		}
	}
}
