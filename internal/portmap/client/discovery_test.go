package client

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/portmap/testutil"
)

func TestDiscoveryClient_Probe_PCP(t *testing.T) {
	// Create IGD with only PCP enabled
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PCP: true, PMP: false})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewDiscoveryClient(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	gw, err := client.Probe(ctx)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}

	if !gw.Equal(net.ParseIP("127.0.0.1")) {
		t.Errorf("expected gateway 127.0.0.1, got %v", gw)
	}

	// Should have selected PCP
	if client.ActiveProtocol() != "pcp" {
		t.Errorf("expected active protocol 'pcp', got '%s'", client.ActiveProtocol())
	}
}

func TestDiscoveryClient_Probe_NATPMP(t *testing.T) {
	// Create IGD with only NAT-PMP enabled
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PMP: true, PCP: false})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewDiscoveryClient(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), igd.PxPPort())

	// Use longer timeout to allow PCP to timeout first, then NAT-PMP to succeed
	// PCP will timeout (no response), then NAT-PMP will succeed
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	gw, err := client.Probe(ctx)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}

	if !gw.Equal(net.ParseIP("127.0.0.1")) {
		t.Errorf("expected gateway 127.0.0.1, got %v", gw)
	}

	// Should have selected NAT-PMP (after PCP failed)
	if client.ActiveProtocol() != "natpmp" {
		t.Errorf("expected active protocol 'natpmp', got '%s'", client.ActiveProtocol())
	}
}

func TestDiscoveryClient_Probe_PreferPCP(t *testing.T) {
	// Create IGD with both PCP and NAT-PMP enabled
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PCP: true, PMP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewDiscoveryClient(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err = client.Probe(ctx)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}

	// Should prefer PCP over NAT-PMP
	if client.ActiveProtocol() != "pcp" {
		t.Errorf("expected active protocol 'pcp' (preferred), got '%s'", client.ActiveProtocol())
	}
}

func TestDiscoveryClient_Probe_NoProtocol(t *testing.T) {
	// Create IGD with no protocols enabled
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PCP: false, PMP: false})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewDiscoveryClient(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err = client.Probe(ctx)
	if err == nil {
		t.Error("expected probe to fail when no protocols enabled")
	}

	// Should have no active protocol
	if client.ActiveProtocol() != "" {
		t.Errorf("expected no active protocol, got '%s'", client.ActiveProtocol())
	}
}

func TestDiscoveryClient_RequestMapping(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PCP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewDiscoveryClient(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// Probe first to discover protocol
	_, err = client.Probe(ctx)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}

	// Request mapping
	mapping, err := client.RequestMapping(ctx, UDP, 51820, 2*time.Hour)
	if err != nil {
		t.Fatalf("request mapping failed: %v", err)
	}

	if mapping.Protocol != UDP {
		t.Errorf("expected UDP protocol, got %v", mapping.Protocol)
	}
	if mapping.InternalPort != 51820 {
		t.Errorf("expected internal port 51820, got %d", mapping.InternalPort)
	}
}

func TestDiscoveryClient_RequestMapping_NoActiveClient(t *testing.T) {
	client := NewDiscoveryClient(net.ParseIP("192.168.1.1"), net.ParseIP("192.168.1.100"), 0)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// Try to request mapping without probing first
	_, err := client.RequestMapping(ctx, UDP, 51820, 2*time.Hour)
	if !errors.Is(err, ErrNoActiveClient) {
		t.Errorf("expected ErrNoActiveClient, got %v", err)
	}
}

func TestDiscoveryClient_RefreshMapping(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PCP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewDiscoveryClient(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err = client.Probe(ctx)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}

	// Create initial mapping
	original, err := client.RequestMapping(ctx, UDP, 51820, 2*time.Hour)
	if err != nil {
		t.Fatalf("request mapping failed: %v", err)
	}

	// Refresh it
	refreshed, err := client.RefreshMapping(ctx, original)
	if err != nil {
		t.Fatalf("refresh mapping failed: %v", err)
	}

	if refreshed.InternalPort != original.InternalPort {
		t.Errorf("internal port mismatch: got %d, want %d", refreshed.InternalPort, original.InternalPort)
	}
}

func TestDiscoveryClient_DeleteMapping(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PCP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewDiscoveryClient(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err = client.Probe(ctx)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}

	// Create mapping
	mapping, err := client.RequestMapping(ctx, UDP, 51820, 2*time.Hour)
	if err != nil {
		t.Fatalf("request mapping failed: %v", err)
	}

	// Delete it
	err = client.DeleteMapping(ctx, mapping)
	if err != nil {
		t.Fatalf("delete mapping failed: %v", err)
	}
}

func TestDiscoveryClient_Close(t *testing.T) {
	client := NewDiscoveryClient(net.ParseIP("192.168.1.1"), net.ParseIP("192.168.1.100"), 0)

	err := client.Close()
	if err != nil {
		t.Errorf("close returned error: %v", err)
	}

	// After close, active should be nil
	if client.ActiveProtocol() != "" {
		t.Errorf("expected no active protocol after close, got '%s'", client.ActiveProtocol())
	}
}

func TestDiscoveryClient_Name(t *testing.T) {
	client := NewDiscoveryClient(net.ParseIP("192.168.1.1"), net.ParseIP("192.168.1.100"), 0)

	// Before probe, name should be "discovery"
	if client.Name() != "discovery" {
		t.Errorf("expected name 'discovery' before probe, got '%s'", client.Name())
	}
}

func TestDiscoveryClient_Gateway(t *testing.T) {
	gw := net.ParseIP("192.168.1.1")
	client := NewDiscoveryClient(gw, net.ParseIP("192.168.1.100"), 0)

	if !client.Gateway().Equal(gw) {
		t.Errorf("expected gateway %v, got %v", gw, client.Gateway())
	}
}
