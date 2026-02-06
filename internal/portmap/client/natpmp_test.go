package client

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/portmap/testutil"
)

func TestNATMPClient_Probe(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PMP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewNATMPClient(net.ParseIP("127.0.0.1"), igd.PxPPort())

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
	if counters.PMPPublicAddrRecv != 1 {
		t.Errorf("expected 1 PMP public addr request, got %d", counters.PMPPublicAddrRecv)
	}
}

func TestNATMPClient_ProbeTimeout(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PMP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	igd.SetFailProbe(true)

	client := NewNATMPClient(net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = client.Probe(ctx)
	if err == nil {
		t.Error("expected probe to fail")
	}
}

func TestNATMPClient_GetExternalAddress(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PMP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewNATMPClient(net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	extIP, err := client.GetExternalAddress(ctx)
	if err != nil {
		t.Fatalf("get external address failed: %v", err)
	}

	if !extIP.Equal(igd.ExternalIP()) {
		t.Errorf("expected external IP %v, got %v", igd.ExternalIP(), extIP)
	}
}

func TestNATMPClient_RequestMapping(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PMP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewNATMPClient(net.ParseIP("127.0.0.1"), igd.PxPPort())

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
	if mapping.Lifetime == 0 {
		t.Error("expected non-zero lifetime")
	}

	counters := igd.Counters.Snapshot()
	if counters.PMPMapUDPRecv != 1 {
		t.Errorf("expected 1 PMP UDP map request, got %d", counters.PMPMapUDPRecv)
	}
}

func TestNATMPClient_RequestMappingTCP(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PMP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewNATMPClient(net.ParseIP("127.0.0.1"), igd.PxPPort())

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

	counters := igd.Counters.Snapshot()
	if counters.PMPMapTCPRecv != 1 {
		t.Errorf("expected 1 PMP TCP map request, got %d", counters.PMPMapTCPRecv)
	}
}

func TestNATMPClient_RefreshMapping(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PMP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewNATMPClient(net.ParseIP("127.0.0.1"), igd.PxPPort())

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
	if counters.PMPMapUDPRecv != 2 {
		t.Errorf("expected 2 PMP UDP map requests, got %d", counters.PMPMapUDPRecv)
	}
}

func TestNATMPClient_DeleteMapping(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PMP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	client := NewNATMPClient(net.ParseIP("127.0.0.1"), igd.PxPPort())

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

func TestNATMPClient_MappingFailure(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PMP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	igd.SetFailMapping(true)

	client := NewNATMPClient(net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = client.RequestMapping(ctx, UDP, 51820, 2*time.Hour)
	if err == nil {
		t.Error("expected mapping to fail")
	}
}

func TestNATMPClient_CustomExternalPort(t *testing.T) {
	igd, err := testutil.NewTestIGD(testutil.TestIGDOptions{PMP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	// Configure IGD to assign a specific external port
	igd.SetMappedPort(54321)

	client := NewNATMPClient(net.ParseIP("127.0.0.1"), igd.PxPPort())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	mapping, err := client.RequestMapping(ctx, UDP, 51820, 2*time.Hour)
	if err != nil {
		t.Fatalf("request mapping failed: %v", err)
	}

	if mapping.ExternalPort != 54321 {
		t.Errorf("expected external port 54321, got %d", mapping.ExternalPort)
	}
}

func TestNATMPClient_Name(t *testing.T) {
	client := NewNATMPClient(net.ParseIP("192.168.1.1"), 0)
	if client.Name() != "natpmp" {
		t.Errorf("expected name 'natpmp', got '%s'", client.Name())
	}
}

func TestNATMPClient_Close(t *testing.T) {
	client := NewNATMPClient(net.ParseIP("192.168.1.1"), 0)
	err := client.Close()
	if err != nil {
		t.Errorf("close returned error: %v", err)
	}
}
