package coord

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
)

// RoleBindingRequest is the request body for creating a peer role binding.
type RoleBindingRequest struct {
	PeerID       string `json:"peer_id"`
	RoleName     string `json:"role_name"`
	BucketScope  string `json:"bucket_scope,omitempty"`
	ObjectPrefix string `json:"object_prefix,omitempty"`
}

// handleBindings handles GET (list) and POST (create) for peer role bindings.
// BindingInfo represents a role binding (peer or group) for the UI.
type BindingInfo struct {
	Name         string    `json:"name"`
	PeerID       string    `json:"peer_id,omitempty"`
	GroupName    string    `json:"group_name,omitempty"`
	RoleName     string    `json:"role_name"`
	BucketScope  string    `json:"bucket_scope,omitempty"`
	ObjectPrefix string    `json:"object_prefix,omitempty"`
	PanelScope   string    `json:"panel_scope,omitempty"`
	Protected    bool      `json:"protected"`
	CreatedAt    time.Time `json:"created_at"`
}

func (s *Server) handleBindings(w http.ResponseWriter, r *http.Request) {
	if s.s3Authorizer == nil {
		s.jsonError(w, "authorization not enabled", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		// Collect both peer and group bindings into a unified format
		var result []BindingInfo

		// Add peer bindings
		for _, b := range s.s3Authorizer.Bindings.List() {
			protected := s.fileShareMgr != nil && s.fileShareMgr.IsProtectedBinding(b)
			result = append(result, BindingInfo{
				Name:         b.Name,
				PeerID:       b.PeerID,
				RoleName:     b.RoleName,
				BucketScope:  b.BucketScope,
				ObjectPrefix: b.ObjectPrefix,
				PanelScope:   b.PanelScope,
				Protected:    protected,
				CreatedAt:    b.CreatedAt,
			})
		}

		// Add group bindings
		for _, gb := range s.s3Authorizer.GroupBindings.List() {
			protected := s.isProtectedGroupBinding(gb)
			result = append(result, BindingInfo{
				Name:         gb.Name,
				GroupName:    gb.GroupName,
				RoleName:     gb.RoleName,
				BucketScope:  gb.BucketScope,
				ObjectPrefix: gb.ObjectPrefix,
				PanelScope:   gb.PanelScope,
				Protected:    protected,
				CreatedAt:    gb.CreatedAt,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)

	case http.MethodPost:
		var req RoleBindingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.PeerID == "" {
			s.jsonError(w, "peer_id is required", http.StatusBadRequest)
			return
		}
		if req.RoleName == "" {
			s.jsonError(w, "role_name is required", http.StatusBadRequest)
			return
		}

		binding := auth.NewRoleBindingWithPrefix(req.PeerID, req.RoleName, req.BucketScope, req.ObjectPrefix)
		s.s3Authorizer.Bindings.Add(binding)

		// Persist
		if s.s3SystemStore != nil {
			if err := s.s3SystemStore.SaveBindings(r.Context(), s.s3Authorizer.Bindings.List()); err != nil {
				log.Warn().Err(err).Msg("failed to persist bindings")
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(binding)

	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleBindingByName handles GET and DELETE for a specific role binding.
func (s *Server) handleBindingByName(w http.ResponseWriter, r *http.Request) {
	if s.s3Authorizer == nil {
		s.jsonError(w, "authorization not enabled", http.StatusServiceUnavailable)
		return
	}

	// Parse path: /api/bindings/{name}
	path := strings.TrimPrefix(r.URL.Path, "/api/bindings/")
	bindingName := strings.TrimSuffix(path, "/")

	if bindingName == "" {
		s.jsonError(w, "binding name required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		binding := s.s3Authorizer.Bindings.Get(bindingName)
		if binding == nil {
			s.jsonError(w, "binding not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(binding)

	case http.MethodDelete:
		// Try user binding first
		binding := s.s3Authorizer.Bindings.Get(bindingName)
		if binding != nil {
			// Check if this is a protected file share owner binding
			if s.fileShareMgr != nil && s.fileShareMgr.IsProtectedBinding(binding) {
				s.jsonError(w, "cannot delete file share owner binding; delete the file share instead", http.StatusForbidden)
				return
			}

			s.s3Authorizer.Bindings.Remove(bindingName)

			// Persist
			if s.s3SystemStore != nil {
				if err := s.s3SystemStore.SaveBindings(r.Context(), s.s3Authorizer.Bindings.List()); err != nil {
					log.Warn().Err(err).Msg("failed to persist bindings")
				}
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
			return
		}

		// Try group binding
		groupBinding := s.s3Authorizer.GroupBindings.Get(bindingName)
		if groupBinding != nil {
			// Check if this is a protected group binding
			if s.isProtectedGroupBinding(groupBinding) {
				s.jsonError(w, "cannot delete built-in or file share group binding", http.StatusForbidden)
				return
			}

			s.s3Authorizer.GroupBindings.Remove(bindingName)

			// Persist
			if s.s3SystemStore != nil {
				if err := s.s3SystemStore.SaveGroupBindings(r.Context(), s.s3Authorizer.GroupBindings.List()); err != nil {
					log.Warn().Err(err).Msg("failed to persist group bindings")
				}
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
			return
		}

		s.jsonError(w, "binding not found", http.StatusNotFound)

	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// isProtectedGroupBinding checks if a group binding is protected from deletion.
// Bindings for built-in groups and file share bindings are protected.
func (s *Server) isProtectedGroupBinding(gb *auth.GroupBinding) bool {
	// Check if the group is a built-in group (protected by RBAC)
	if s.s3Authorizer != nil {
		group := s.s3Authorizer.Groups.Get(gb.GroupName)
		if group != nil && group.Builtin {
			return true
		}
	}

	// File share bindings are also protected
	if s.fileShareMgr != nil && s.fileShareMgr.IsProtectedGroupBinding(gb) {
		return true
	}

	return false
}
