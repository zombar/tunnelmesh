package docker

import (
	"context"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
)

// mockFilter is a mock implementation of packetFilter for testing.
type mockFilter struct {
	rules []routing.FilterRule
}

func (m *mockFilter) AddTemporaryRule(rule routing.FilterRule) {
	m.rules = append(m.rules, rule)
}

func TestSyncPortForwards_Bridge(t *testing.T) {
	autoForward := true
	cfg := &config.DockerConfig{
		Socket:          "unix:///var/run/docker.sock",
		AutoPortForward: &autoForward,
	}

	filter := &mockFilter{}
	mgr := NewManager(cfg, "test-peer", filter, nil)

	// Mock container with bridge network and ports
	now := time.Now()
	container := &ContainerInfo{
		ID:        "abc123",
		Name:      "test-nginx",
		Status:    "running",
		CreatedAt: now,
		StartedAt: now,
		Ports: []PortBinding{
			{ContainerPort: 80, HostPort: 8080, Protocol: "tcp"},
			{ContainerPort: 443, HostPort: 8443, Protocol: "tcp"},
		},
		NetworkMode: "bridge",
	}

	mgr.client = &mockDockerClient{containers: []ContainerInfo{*container}}

	ctx := context.Background()
	err := mgr.syncPortForwards(ctx, "abc123")
	if err != nil {
		t.Fatalf("syncPortForwards failed: %v", err)
	}

	// Should have created 2 rules
	if len(filter.rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(filter.rules))
	}

	// Check first rule
	if filter.rules[0].Port != 8080 {
		t.Errorf("expected port 8080, got %d", filter.rules[0].Port)
	}
	if filter.rules[0].Protocol != routing.ProtoTCP {
		t.Errorf("expected TCP protocol, got %d", filter.rules[0].Protocol)
	}
	if filter.rules[0].Action != routing.ActionAllow {
		t.Errorf("expected Allow action, got %v", filter.rules[0].Action)
	}
	if filter.rules[0].Expires == 0 {
		t.Error("expected non-zero expiry")
	}

	// Check second rule
	if filter.rules[1].Port != 8443 {
		t.Errorf("expected port 8443, got %d", filter.rules[1].Port)
	}
}

func TestSyncPortForwards_HostNetwork(t *testing.T) {
	autoForward := true
	cfg := &config.DockerConfig{
		Socket:          "unix:///var/run/docker.sock",
		AutoPortForward: &autoForward,
	}

	filter := &mockFilter{}
	mgr := NewManager(cfg, "test-peer", filter, nil)

	// Mock container with host network (should NOT create rules)
	now := time.Now()
	container := &ContainerInfo{
		ID:        "abc123",
		Name:      "test-nginx",
		Status:    "running",
		CreatedAt: now,
		StartedAt: now,
		Ports: []PortBinding{
			{ContainerPort: 80, HostPort: 80, Protocol: "tcp"},
		},
		NetworkMode: "host",
	}

	mgr.client = &mockDockerClient{containers: []ContainerInfo{*container}}

	ctx := context.Background()
	err := mgr.syncPortForwards(ctx, "abc123")
	if err != nil {
		t.Fatalf("syncPortForwards failed: %v", err)
	}

	// Should NOT have created any rules for host network
	if len(filter.rules) != 0 {
		t.Errorf("expected 0 rules for host network, got %d", len(filter.rules))
	}
}

func TestSyncPortForwards_NoHostPort(t *testing.T) {
	autoForward := true
	cfg := &config.DockerConfig{
		Socket:          "unix:///var/run/docker.sock",
		AutoPortForward: &autoForward,
	}

	filter := &mockFilter{}
	mgr := NewManager(cfg, "test-peer", filter, nil)

	// Mock container with ports but no host binding
	now := time.Now()
	container := &ContainerInfo{
		ID:          "abc123",
		Name:        "test-nginx",
		Status:      "running",
		CreatedAt:   now,
		StartedAt:   now,
		Ports:       []PortBinding{{ContainerPort: 80, HostPort: 0, Protocol: "tcp"}},
		NetworkMode: "bridge",
	}

	mgr.client = &mockDockerClient{containers: []ContainerInfo{*container}}

	ctx := context.Background()
	err := mgr.syncPortForwards(ctx, "abc123")
	if err != nil {
		t.Fatalf("syncPortForwards failed: %v", err)
	}

	// Should NOT have created rules for unpublished ports
	if len(filter.rules) != 0 {
		t.Errorf("expected 0 rules for unpublished ports, got %d", len(filter.rules))
	}
}

func TestSyncPortForwards_Disabled(t *testing.T) {
	autoForward := false
	cfg := &config.DockerConfig{
		Socket:          "unix:///var/run/docker.sock",
		AutoPortForward: &autoForward, // Disabled
	}

	filter := &mockFilter{}
	mgr := NewManager(cfg, "test-peer", filter, nil)

	// Mock container
	now := time.Now()
	container := &ContainerInfo{
		ID:          "abc123",
		Name:        "test-nginx",
		Status:      "running",
		CreatedAt:   now,
		StartedAt:   now,
		Ports:       []PortBinding{{ContainerPort: 80, HostPort: 8080, Protocol: "tcp"}},
		NetworkMode: "bridge",
	}

	mgr.client = &mockDockerClient{containers: []ContainerInfo{*container}}

	ctx := context.Background()
	err := mgr.syncPortForwards(ctx, "abc123")
	if err != nil {
		t.Fatalf("syncPortForwards failed: %v", err)
	}

	// Should NOT have created rules when AutoPortForward is disabled
	if len(filter.rules) != 0 {
		t.Errorf("expected 0 rules when AutoPortForward=false, got %d", len(filter.rules))
	}
}
