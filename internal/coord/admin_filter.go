package coord

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
)

// FilterRulesRequest is the request for adding/removing filter rules.
type FilterRulesRequest struct {
	PeerName   string `json:"peer"`        // Target peer name
	Port       uint16 `json:"port"`        // Port number
	Protocol   string `json:"protocol"`    // "tcp" or "udp"
	Action     string `json:"action"`      // "allow" or "deny"
	SourcePeer string `json:"source_peer"` // Source peer (optional, empty = any peer)
	TTL        int64  `json:"ttl"`         // Time to live in seconds (0 = permanent)
}

// FilterRulesResponse is the response for listing filter rules.
type FilterRulesResponse struct {
	PeerName    string           `json:"peer"`
	DefaultDeny bool             `json:"default_deny"`
	S3Enabled   bool             `json:"s3_enabled"` // Whether coordinator has S3 persistence enabled
	Rules       []FilterRuleInfo `json:"rules"`
	Error       string           `json:"error,omitempty"` // Set when query failed (peer offline/timeout)
}

// FilterRuleInfo represents a filter rule for API responses.
type FilterRuleInfo struct {
	Port       uint16 `json:"port"`
	Protocol   string `json:"protocol"`
	Action     string `json:"action"`
	Source     string `json:"source"`      // "coordinator", "config", "temporary"
	Expires    int64  `json:"expires"`     // Unix timestamp, 0=permanent
	SourcePeer string `json:"source_peer"` // Source peer (empty = any peer)
}

// handleFilterRules handles GET (list) and POST/DELETE for filter rules.
// GET: List rules for a peer (requires ?peer=name query param)
// POST: Add a temporary rule to a peer
// DELETE: Remove a temporary rule from a peer
func (s *Server) handleFilterRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleFilterRulesList(w, r)
	case http.MethodPost:
		s.handleFilterRuleAdd(w, r)
	case http.MethodDelete:
		s.handleFilterRuleRemove(w, r)
	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleFilterRulesList returns the filter rules for a peer by querying the peer directly.
func (s *Server) handleFilterRulesList(w http.ResponseWriter, r *http.Request) {
	peerName := r.URL.Query().Get("peer")
	if peerName == "" {
		s.jsonError(w, "peer parameter required", http.StatusBadRequest)
		return
	}

	// Query the peer for their current filter rules
	rulesJSON, err := s.relay.QueryFilterRules(r.Context(), peerName, 10*time.Second)
	if err != nil {
		// Peer not connected or timeout - return empty rules with error message
		log.Debug().Err(err).Str("peer", peerName).Msg("failed to query peer filter rules")
		resp := FilterRulesResponse{
			PeerName:    peerName,
			DefaultDeny: s.cfg.Filter.IsDefaultDeny(),
			S3Enabled:   s.s3SystemStore != nil,
			Rules:       []FilterRuleInfo{},
			Error:       "Peer offline or unreachable",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	// Parse the response from the peer
	var peerRules []struct {
		Port       uint16 `json:"port"`
		Protocol   string `json:"protocol"`
		Action     string `json:"action"`
		SourcePeer string `json:"source_peer"`
		Source     string `json:"source"`
		Expires    int64  `json:"expires"`
	}
	if err := json.Unmarshal(rulesJSON, &peerRules); err != nil {
		log.Error().Err(err).Str("peer", peerName).Msg("failed to parse peer filter rules")
		s.jsonError(w, "failed to parse peer filter rules", http.StatusInternalServerError)
		return
	}

	// Convert to response format
	rules := make([]FilterRuleInfo, 0, len(peerRules))
	for _, r := range peerRules {
		rules = append(rules, FilterRuleInfo{
			Port:       r.Port,
			Protocol:   r.Protocol,
			Action:     r.Action,
			Source:     r.Source,
			Expires:    r.Expires,
			SourcePeer: r.SourcePeer,
		})
	}

	resp := FilterRulesResponse{
		PeerName:    peerName,
		DefaultDeny: s.cfg.Filter.IsDefaultDeny(),
		S3Enabled:   s.s3SystemStore != nil,
		Rules:       rules,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleFilterRuleAdd adds a temporary filter rule to a peer.
func (s *Server) handleFilterRuleAdd(w http.ResponseWriter, r *http.Request) {
	var req FilterRulesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.PeerName == "" {
		s.jsonError(w, "peer is required", http.StatusBadRequest)
		return
	}
	if req.Port == 0 {
		s.jsonError(w, "port is required", http.StatusBadRequest)
		return
	}
	if req.Protocol != "tcp" && req.Protocol != "udp" {
		s.jsonError(w, "protocol must be 'tcp' or 'udp'", http.StatusBadRequest)
		return
	}
	if req.Action != "allow" && req.Action != "deny" {
		s.jsonError(w, "action must be 'allow' or 'deny'", http.StatusBadRequest)
		return
	}
	// Prevent self-targeting: a peer can't have a rule filtering traffic from itself
	if req.SourcePeer != "" && req.SourcePeer == req.PeerName {
		s.jsonError(w, "a peer cannot filter traffic from itself", http.StatusBadRequest)
		return
	}

	// Push the rule to peer(s) via relay
	if req.PeerName == "__all__" {
		// Broadcast to all connected peers (skip self-referencing rules)
		for _, peerName := range s.relay.GetConnectedPeerNames() {
			if req.SourcePeer != "" && req.SourcePeer == peerName {
				continue // Skip: peer can't filter traffic from itself
			}
			s.relay.PushFilterRuleAdd(peerName, req.Port, req.Protocol, req.Action, req.SourcePeer)
		}
	} else {
		s.relay.PushFilterRuleAdd(req.PeerName, req.Port, req.Protocol, req.Action, req.SourcePeer)
	}

	// Validate TTL bounds
	if req.TTL < 0 {
		s.jsonError(w, "ttl cannot be negative", http.StatusBadRequest)
		return
	}
	if req.TTL > 365*24*3600 { // Max 1 year
		s.jsonError(w, "ttl exceeds maximum (1 year)", http.StatusBadRequest)
		return
	}

	// Update coordinator's filter and persist to S3
	if s.filter != nil {
		// Calculate expiry if TTL specified
		var expires int64
		if req.TTL > 0 {
			expires = time.Now().Unix() + req.TTL
		}

		rule := routing.FilterRule{
			Port:       req.Port,
			Protocol:   routing.ProtocolFromString(req.Protocol),
			Action:     routing.ParseFilterAction(req.Action),
			Expires:    expires,
			SourcePeer: req.SourcePeer,
		}
		s.filter.AddTemporaryRule(rule)
		s.saveFilterRulesAsync()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleFilterRuleRemove removes a temporary filter rule from a peer.
func (s *Server) handleFilterRuleRemove(w http.ResponseWriter, r *http.Request) {
	var req FilterRulesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.PeerName == "" {
		s.jsonError(w, "peer is required", http.StatusBadRequest)
		return
	}
	if req.Port == 0 {
		s.jsonError(w, "port is required", http.StatusBadRequest)
		return
	}
	if req.Protocol != "tcp" && req.Protocol != "udp" {
		s.jsonError(w, "protocol must be 'tcp' or 'udp'", http.StatusBadRequest)
		return
	}
	// Prevent self-targeting
	if req.SourcePeer != "" && req.SourcePeer == req.PeerName {
		s.jsonError(w, "a peer cannot filter traffic from itself", http.StatusBadRequest)
		return
	}

	// Push the rule removal to peer(s) via relay (if relay is enabled)
	if req.PeerName == "__all__" {
		// Broadcast to all connected peers (skip self-referencing rules)
		for _, peerName := range s.relay.GetConnectedPeerNames() {
			if req.SourcePeer != "" && req.SourcePeer == peerName {
				continue
			}
			s.relay.PushFilterRuleRemove(peerName, req.Port, req.Protocol, req.SourcePeer)
		}
	} else {
		s.relay.PushFilterRuleRemove(req.PeerName, req.Port, req.Protocol, req.SourcePeer)
	}

	// Update coordinator's filter and persist to S3
	if s.filter != nil {
		proto := routing.ProtocolFromString(req.Protocol)
		s.filter.RemoveTemporaryRuleForPeer(req.Port, proto, req.SourcePeer)
		s.saveFilterRulesAsync()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
