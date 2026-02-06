package client

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/portmap/testutil"
)

func TestPCPClient_Probe(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PCP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewPCPClient(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	gw, err := client.Probe(ctx)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}

	if !gw.Equal(net.ParseIP("127.0.0.1")) {
		t.Errorf("expected gateway 127.0.0.1, got %v", gw)
	}

	counters := igd.Counters.Snapshot()
	if counters.PCPAnnounceRecv != 1 {
		t.Errorf("expected 1 PCP announce, got %d", counters.PCPAnnounceRecv)
	}
}

func TestPCPClient_ProbeTimeout(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PCP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	igd.SetFailProbe(true)

	client := NewPCPClient(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = client.Probe(ctx)
	if err == nil {
		t.Error("expected probe to fail")
	}
}

func TestPCPClient_RequestMapping(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PCP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewPCPClient(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

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
	if mapping.ExternalPort == 0 {
		t.Error("expected non-zero external port")
	}
	if mapping.ExternalIP == nil {
		t.Error("expected non-nil external IP")
	}
	if mapping.Lifetime == 0 {
		t.Error("expected non-zero lifetime")
	}

	counters := igd.Counters.Snapshot()
	if counters.PCPMapRecv != 1 {
		t.Errorf("expected 1 PCP map request, got %d", counters.PCPMapRecv)
	}
}

func TestPCPClient_RequestMappingTCP(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PCP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewPCPClient(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	mapping, err := client.RequestMapping(ctx, TCP, 22, 2*time.Hour)
	if err != nil {
		t.Fatalf("request mapping failed: %v", err)
	}

	if mapping.Protocol != TCP {
		t.Errorf("expected TCP protocol, got %v", mapping.Protocol)
	}
	if mapping.InternalPort != 22 {
		t.Errorf("expected internal port 22, got %d", mapping.InternalPort)
	}
}

func TestPCPClient_RefreshMapping(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PCP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewPCPClient(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// First create a mapping
	original, err := client.RequestMapping(ctx, UDP, 51820, 2*time.Hour)
	if err != nil {
		t.Fatalf("request mapping failed: %v", err)
	}

	// Then refresh it
	refreshed, err := client.RefreshMapping(ctx, original)
	if err != nil {
		t.Fatalf("refresh mapping failed: %v", err)
	}

	if refreshed.Protocol != original.Protocol {
		t.Errorf("protocol mismatch: got %v, want %v", refreshed.Protocol, original.Protocol)
	}
	if refreshed.InternalPort != original.InternalPort {
		t.Errorf("internal port mismatch: got %d, want %d", refreshed.InternalPort, original.InternalPort)
	}

	counters := igd.Counters.Snapshot()
	if counters.PCPMapRecv != 2 {
		t.Errorf("expected 2 PCP map requests, got %d", counters.PCPMapRecv)
	}
}

func TestPCPClient_DeleteMapping(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PCP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewPCPClient(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// Create a mapping
	mapping, err := client.RequestMapping(ctx, UDP, 51820, 2*time.Hour)
	if err != nil {
		t.Fatalf("request mapping failed: %v", err)
	}

	// Delete it (sends request with lifetime=0)
	err = client.DeleteMapping(ctx, mapping)
	if err != nil {
		t.Fatalf("delete mapping failed: %v", err)
	}
}

func TestPCPClient_MappingFailure(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PCP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	igd.SetFailMapping(true)

	client := NewPCPClient(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = client.RequestMapping(ctx, UDP, 51820, 2*time.Hour)
	if err == nil {
		t.Error("expected mapping to fail")
	}
}

func TestPCPClient_CustomExternalPort(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PCP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	// Configure IGD to assign a specific external port
	igd.SetMappedPort(12345)

	client := NewPCPClient(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	mapping, err := client.RequestMapping(ctx, UDP, 51820, 2*time.Hour)
	if err != nil {
		t.Fatalf("request mapping failed: %v", err)
	}

	if mapping.ExternalPort != 12345 {
		t.Errorf("expected external port 12345, got %d", mapping.ExternalPort)
	}
}

func TestPCPClient_Name(t *testing.T) {
	client := NewPCPClient(net.ParseIP("192.168.1.1"), net.ParseIP("192.168.1.100"), 0)
	if client.Name() != "pcp" {
		t.Errorf("expected name 'pcp', got '%s'", client.Name())
	}
}
