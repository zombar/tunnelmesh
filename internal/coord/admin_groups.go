package coord

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
)

// GroupCreateRequest is the request body for creating a group.
type GroupCreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// GroupMemberRequest is the request body for adding a member.
type GroupMemberRequest struct {
	UserID string `json:"user_id"`
}

// GroupBindingRequest is the request body for granting a role to a group.
type GroupBindingRequest struct {
	RoleName     string `json:"role_name"`
	BucketScope  string `json:"bucket_scope,omitempty"`
	ObjectPrefix string `json:"object_prefix,omitempty"`
}

// handleGroups handles GET (list) and POST (create) for groups.
func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGroupsList(w, r)
	case http.MethodPost:
		s.handleGroupCreate(w, r)
	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGroupsList returns all groups.
func (s *Server) handleGroupsList(w http.ResponseWriter, _ *http.Request) {
	if s.s3Authorizer == nil || s.s3Authorizer.Groups == nil {
		s.jsonError(w, "groups not enabled", http.StatusServiceUnavailable)
		return
	}

	groups := s.s3Authorizer.Groups.List()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(groups)
}

// handleGroupCreate creates a new group.
func (s *Server) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
	if s.s3Authorizer == nil || s.s3Authorizer.Groups == nil {
		s.jsonError(w, "groups not enabled", http.StatusServiceUnavailable)
		return
	}

	var req GroupCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		s.jsonError(w, "name is required", http.StatusBadRequest)
		return
	}

	// Check if group already exists
	if s.s3Authorizer.Groups.Get(req.Name) != nil {
		s.jsonError(w, "group already exists", http.StatusConflict)
		return
	}

	// Create the group
	group, err := s.s3Authorizer.Groups.Create(req.Name, req.Description)
	if err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Persist
	if s.s3SystemStore != nil {
		if err := s.s3SystemStore.SaveGroups(r.Context(), s.s3Authorizer.Groups.List()); err != nil {
			log.Warn().Err(err).Msg("failed to persist groups")
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(group)
}

// handleGroupByName handles GET, DELETE, and sub-resources for a specific group.
func (s *Server) handleGroupByName(w http.ResponseWriter, r *http.Request) {
	if s.s3Authorizer == nil || s.s3Authorizer.Groups == nil {
		s.jsonError(w, "groups not enabled", http.StatusServiceUnavailable)
		return
	}

	// Parse path: /api/groups/{name} or /api/groups/{name}/members[/{id}]
	path := strings.TrimPrefix(r.URL.Path, "/api/groups/")
	parts := strings.Split(path, "/")
	groupName := parts[0]

	if groupName == "" {
		s.jsonError(w, "group name required", http.StatusBadRequest)
		return
	}

	// Check if this is a sub-resource request
	if len(parts) > 1 {
		switch parts[1] {
		case "members":
			s.handleGroupMembers(w, r, groupName, parts[2:])
			return
		case "bindings":
			s.handleGroupBindings(w, r, groupName)
			return
		}
	}

	// Direct group operations
	switch r.Method {
	case http.MethodGet:
		group := s.s3Authorizer.Groups.Get(groupName)
		if group == nil {
			s.jsonError(w, "group not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(group)

	case http.MethodDelete:
		group := s.s3Authorizer.Groups.Get(groupName)
		if group == nil {
			s.jsonError(w, "group not found", http.StatusNotFound)
			return
		}
		if group.Builtin {
			s.jsonError(w, "cannot delete built-in group", http.StatusForbidden)
			return
		}
		_ = s.s3Authorizer.Groups.Delete(groupName)

		// Persist
		if s.s3SystemStore != nil {
			if err := s.s3SystemStore.SaveGroups(r.Context(), s.s3Authorizer.Groups.List()); err != nil {
				log.Warn().Err(err).Msg("failed to persist groups")
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})

	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGroupMembers handles member management for a group.
func (s *Server) handleGroupMembers(w http.ResponseWriter, r *http.Request, groupName string, pathParts []string) {
	group := s.s3Authorizer.Groups.Get(groupName)
	if group == nil {
		s.jsonError(w, "group not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		// List members
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(group.Members)

	case http.MethodPost:
		// Add member
		var req GroupMemberRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.UserID == "" {
			s.jsonError(w, "user_id is required", http.StatusBadRequest)
			return
		}

		if err := s.s3Authorizer.Groups.AddMember(groupName, req.UserID); err != nil {
			s.jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Persist
		if s.s3SystemStore != nil {
			if err := s.s3SystemStore.SaveGroups(r.Context(), s.s3Authorizer.Groups.List()); err != nil {
				log.Warn().Err(err).Msg("failed to persist groups")
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "added"})

	case http.MethodDelete:
		// Remove member - user ID in path
		if len(pathParts) == 0 || pathParts[0] == "" {
			s.jsonError(w, "peer_id required in path", http.StatusBadRequest)
			return
		}
		peerID := pathParts[0]

		if err := s.s3Authorizer.Groups.RemoveMember(groupName, peerID); err != nil {
			s.jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Persist
		if s.s3SystemStore != nil {
			if err := s.s3SystemStore.SaveGroups(r.Context(), s.s3Authorizer.Groups.List()); err != nil {
				log.Warn().Err(err).Msg("failed to persist groups")
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "removed"})

	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGroupBindings handles role bindings for a group.
func (s *Server) handleGroupBindings(w http.ResponseWriter, r *http.Request, groupName string) {
	group := s.s3Authorizer.Groups.Get(groupName)
	if group == nil {
		s.jsonError(w, "group not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		bindings := s.s3Authorizer.GroupBindings.GetForGroup(groupName)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(bindings)

	case http.MethodPost:
		var req GroupBindingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.RoleName == "" {
			s.jsonError(w, "role_name is required", http.StatusBadRequest)
			return
		}

		binding := auth.NewGroupBindingWithPrefix(groupName, req.RoleName, req.BucketScope, req.ObjectPrefix)
		s.s3Authorizer.GroupBindings.Add(binding)

		// Persist
		if s.s3SystemStore != nil {
			if err := s.s3SystemStore.SaveGroupBindings(r.Context(), s.s3Authorizer.GroupBindings.List()); err != nil {
				log.Warn().Err(err).Msg("failed to persist group bindings")
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(binding)

	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
