package coord

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/docker"
)

// DockerContainerResponse represents the response for listing Docker containers.
type DockerContainerResponse struct {
	Containers []docker.ContainerInfo `json:"containers"`
	Total      int                    `json:"total"`
}

// handleDockerContainers returns all Docker containers from the local Docker daemon.
// Only available when the coordinator has joined the mesh with Docker enabled.
// GET /api/docker/containers
func (s *Server) handleDockerContainers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check RBAC authorization
	peerID := s.getRequestOwner(r)
	if !s.s3Authorizer.CanAccessPanel(peerID, auth.PanelDocker) {
		log.Info().Str("peer_id", peerID).Msg("Docker API access denied")
		s.jsonError(w, "access denied", http.StatusForbidden)
		return
	}

	// Check if Docker manager is available
	if s.dockerMgr == nil {
		s.jsonError(w, "Docker not enabled on this coordinator", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	containers, err := s.dockerMgr.ListContainers(ctx)
	if err != nil {
		log.Error().Err(err).Msg("Failed to list Docker containers")
		s.jsonError(w, "Failed to list containers", http.StatusInternalServerError)
		return
	}

	// Filter by status if requested
	status := r.URL.Query().Get("status")
	if status != "" && status != "all" {
		filtered := []docker.ContainerInfo{}
		for _, c := range containers {
			if c.State == status {
				filtered = append(filtered, c)
			}
		}
		containers = filtered
	}

	response := DockerContainerResponse{
		Containers: containers,
		Total:      len(containers),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Error().Err(err).Msg("Failed to encode Docker containers response")
	}
}

// handleDockerContainerInspect returns detailed information about a specific container.
// GET /api/docker/containers/{id}
func (s *Server) handleDockerContainerInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check RBAC authorization
	peerID := s.getRequestOwner(r)
	if !s.s3Authorizer.CanAccessPanel(peerID, auth.PanelDocker) {
		log.Info().Str("peer_id", peerID).Msg("Docker API access denied")
		s.jsonError(w, "access denied", http.StatusForbidden)
		return
	}

	if s.dockerMgr == nil {
		s.jsonError(w, "Docker not enabled on this coordinator", http.StatusServiceUnavailable)
		return
	}

	// Extract container ID from path
	// Path format: /api/docker/containers/{id}
	containerID := r.URL.Path[len("/api/docker/containers/"):]
	if containerID == "" {
		s.jsonError(w, "Container ID required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	container, err := s.dockerMgr.InspectContainer(ctx, containerID)
	if err != nil {
		log.Error().Err(err).Str("id", containerID).Msg("Failed to inspect Docker container")
		s.jsonError(w, "Failed to inspect container", http.StatusInternalServerError)
		return
	}

	if container == nil {
		s.jsonError(w, "Container not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(container); err != nil {
		log.Error().Err(err).Msg("Failed to encode Docker container inspect response")
	}
}

// DockerControlRequest represents a request to control a Docker container.
type DockerControlRequest struct {
	Action string `json:"action"` // "start", "stop", "restart"
}

// DockerControlResponse represents the response for a control action.
type DockerControlResponse struct {
	Success     bool   `json:"success"`
	ContainerID string `json:"container_id"`
	Action      string `json:"action"`
	Message     string `json:"message,omitempty"`
}

// handleDockerControl controls Docker container lifecycle (start, stop, restart).
// POST /api/docker/containers/{id}/control
func (s *Server) handleDockerControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check RBAC authorization
	peerID := s.getRequestOwner(r)
	if !s.s3Authorizer.CanAccessPanel(peerID, auth.PanelDocker) {
		log.Info().Str("peer_id", peerID).Msg("Docker API access denied")
		s.jsonError(w, "access denied", http.StatusForbidden)
		return
	}

	if s.dockerMgr == nil {
		s.jsonError(w, "Docker not enabled on this coordinator", http.StatusServiceUnavailable)
		return
	}

	// Extract container ID from path
	path := r.URL.Path
	if len(path) < len("/api/docker/containers/") {
		s.jsonError(w, "Container ID required", http.StatusBadRequest)
		return
	}

	// Remove /control suffix to get container ID
	containerID := path[len("/api/docker/containers/"):]
	containerID = strings.TrimSuffix(containerID, "/control")

	if containerID == "" {
		s.jsonError(w, "Container ID required", http.StatusBadRequest)
		return
	}

	var req DockerControlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate action
	switch req.Action {
	case "start", "stop", "restart":
		// Valid actions
	default:
		s.jsonError(w, "Invalid action (must be start, stop, or restart)", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var err error
	switch req.Action {
	case "start":
		err = s.dockerMgr.StartContainer(ctx, containerID)
	case "stop":
		err = s.dockerMgr.StopContainer(ctx, containerID)
	case "restart":
		err = s.dockerMgr.RestartContainer(ctx, containerID)
	}

	if err != nil {
		log.Error().
			Err(err).
			Str("container", containerID).
			Str("action", req.Action).
			Msg("Docker container control action failed")
		s.jsonError(w, "Failed to "+req.Action+" container: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Info().
		Str("container", containerID).
		Str("action", req.Action).
		Msg("Docker container control action succeeded")

	response := DockerControlResponse{
		Success:     true,
		ContainerID: containerID,
		Action:      req.Action,
		Message:     "Container " + req.Action + " succeeded",
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Error().Err(err).Msg("Failed to encode Docker control response")
	}
}
