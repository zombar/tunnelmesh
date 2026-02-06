package testutil

import (
	"net"
	"testing"
	"time"
)

func TestTestIGD_Create(t *testing.T) {
	igd, err := NewTestIGD(TestIGDOptions{PMP: true, PCP: true, UPnP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	if igd.PxPPort() == 0 {
		t.Error("PxPPort should not be 0")
	}
	if igd.UPnPPort() == 0 {
		t.Error("UPnPPort should not be 0")
	}
	if igd.HTTPAddr() == "" {
		t.Error("HTTPAddr should not be empty")
	}
}

func TestTestIGD_PMPPublicAddr(t *testing.T) {
	igd, err := NewTestIGD(TestIGDOptions{PMP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	// Send PMP public addr request
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		t.Fatalf("failed to create UDP socket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// PMP public address request: version=0, opcode=0
	req := []byte{0, 0}
	gwAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(igd.PxPPort())}

	_, err = conn.WriteTo(req, gwAddr)
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}

	buf := make([]byte, 256)
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("failed to set deadline: %v", err)
	}
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	resp := buf[:n]
	if len(resp) < 12 {
		t.Fatalf("response too short: %d bytes", len(resp))
	}

	// Check version and opcode
	if resp[0] != 0 {
		t.Errorf("expected version 0, got %d", resp[0])
	}
	if resp[1] != 0x80 { // opcode | reply
		t.Errorf("expected opcode 0x80, got 0x%x", resp[1])
	}

	// Check external IP is returned
	extIP := net.IPv4(resp[8], resp[9], resp[10], resp[11])
	if !extIP.Equal(igd.ExternalIP()) {
		t.Errorf("expected external IP %v, got %v", igd.ExternalIP(), extIP)
	}

	counters := igd.Counters.Snapshot()
	if counters.PMPPublicAddrRecv != 1 {
		t.Errorf("expected 1 PMP public addr request, got %d", counters.PMPPublicAddrRecv)
	}
}

func TestTestIGD_PMPMapping(t *testing.T) {
	igd, err := NewTestIGD(TestIGDOptions{PMP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		t.Fatalf("failed to create UDP socket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// PMP UDP mapping request
	req := make([]byte, 12)
	req[0] = 0 // version
	req[1] = 1 // opcode = map UDP
	// req[2:4] = reserved
	req[4] = 0xC8 // internal port 51200 (0xC800)
	req[5] = 0x00
	req[6] = 0xC8 // suggested external port
	req[7] = 0x00
	req[8] = 0x00 // lifetime 7200 (0x1C20)
	req[9] = 0x00
	req[10] = 0x1C
	req[11] = 0x20

	gwAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(igd.PxPPort())}
	_, err = conn.WriteTo(req, gwAddr)
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}

	buf := make([]byte, 256)
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("failed to set deadline: %v", err)
	}
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	resp := buf[:n]
	if len(resp) < 16 {
		t.Fatalf("response too short: %d bytes", len(resp))
	}

	if resp[0] != 0 {
		t.Errorf("expected version 0, got %d", resp[0])
	}
	if resp[1] != 0x81 { // opcode 1 | reply
		t.Errorf("expected opcode 0x81, got 0x%x", resp[1])
	}

	counters := igd.Counters.Snapshot()
	if counters.PMPMapUDPRecv != 1 {
		t.Errorf("expected 1 PMP UDP map request, got %d", counters.PMPMapUDPRecv)
	}
}

func TestTestIGD_PCPAnnounce(t *testing.T) {
	igd, err := NewTestIGD(TestIGDOptions{PCP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		t.Fatalf("failed to create UDP socket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// PCP announce request
	req := make([]byte, 24)
	req[0] = 2 // version
	req[1] = 0 // opcode = announce
	// Fill client IP (localhost as IPv4-mapped IPv6)
	req[8+10] = 0xff
	req[8+11] = 0xff
	req[8+12] = 127
	req[8+13] = 0
	req[8+14] = 0
	req[8+15] = 1

	gwAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(igd.PxPPort())}
	_, err = conn.WriteTo(req, gwAddr)
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}

	buf := make([]byte, 256)
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("failed to set deadline: %v", err)
	}
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	resp := buf[:n]
	if len(resp) < 24 {
		t.Fatalf("response too short: %d bytes", len(resp))
	}

	if resp[0] != 2 {
		t.Errorf("expected version 2, got %d", resp[0])
	}
	if resp[1] != 0x80 { // announce | reply
		t.Errorf("expected opcode 0x80, got 0x%x", resp[1])
	}
	if resp[3] != 0 { // result code OK
		t.Errorf("expected result code 0, got %d", resp[3])
	}

	counters := igd.Counters.Snapshot()
	if counters.PCPAnnounceRecv != 1 {
		t.Errorf("expected 1 PCP announce request, got %d", counters.PCPAnnounceRecv)
	}
}

func TestTestIGD_PCPMap(t *testing.T) {
	igd, err := NewTestIGD(TestIGDOptions{PCP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		t.Fatalf("failed to create UDP socket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// PCP MAP request
	req := make([]byte, 60)
	req[0] = 2 // version
	req[1] = 1 // opcode = map
	req[4] = 0 // lifetime 7200 (0x1C20)
	req[5] = 0
	req[6] = 0x1C
	req[7] = 0x20

	// Client IP (IPv4-mapped IPv6)
	req[8+10] = 0xff
	req[8+11] = 0xff
	req[8+12] = 127
	req[8+13] = 0
	req[8+14] = 0
	req[8+15] = 1

	// MAP opcode data
	// req[24:36] = nonce (can be zeros for test)
	req[36] = 17 // protocol = UDP
	// req[37:40] = reserved
	req[40] = 0xC8 // internal port 51200
	req[41] = 0x00
	req[42] = 0xC8 // suggested external port
	req[43] = 0x00
	// req[44:60] = suggested external IP (zeros = any)

	gwAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(igd.PxPPort())}
	_, err = conn.WriteTo(req, gwAddr)
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}

	buf := make([]byte, 256)
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("failed to set deadline: %v", err)
	}
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	resp := buf[:n]
	if len(resp) < 60 {
		t.Fatalf("response too short: %d bytes", len(resp))
	}

	if resp[0] != 2 {
		t.Errorf("expected version 2, got %d", resp[0])
	}
	if resp[1] != 0x81 { // map | reply
		t.Errorf("expected opcode 0x81, got 0x%x", resp[1])
	}
	if resp[3] != 0 { // result code OK
		t.Errorf("expected result code 0, got %d", resp[3])
	}

	counters := igd.Counters.Snapshot()
	if counters.PCPMapRecv != 1 {
		t.Errorf("expected 1 PCP map request, got %d", counters.PCPMapRecv)
	}
}

func TestTestIGD_FailProbe(t *testing.T) {
	igd, err := NewTestIGD(TestIGDOptions{PMP: true})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	igd.SetFailProbe(true)

	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		t.Fatalf("failed to create UDP socket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// PMP public address request
	req := []byte{0, 0}
	gwAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(igd.PxPPort())}
	_, err = conn.WriteTo(req, gwAddr)
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}

	buf := make([]byte, 256)
	if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("failed to set deadline: %v", err)
	}
	_, _, err = conn.ReadFrom(buf)
	if err == nil {
		t.Error("expected timeout, got response")
	}

	// Counter should still be incremented
	counters := igd.Counters.Snapshot()
	if counters.PMPPublicAddrRecv != 1 {
		t.Errorf("expected 1 PMP public addr request, got %d", counters.PMPPublicAddrRecv)
	}
}

func TestTestIGD_Disabled(t *testing.T) {
	// Create IGD with all protocols disabled
	igd, err := NewTestIGD(TestIGDOptions{PMP: false, PCP: false, UPnP: false})
	if err != nil {
		t.Fatalf("failed to create TestIGD: %v", err)
	}
	defer func() { _ = igd.Close() }()

	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		t.Fatalf("failed to create UDP socket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// PMP public address request - should not get response
	req := []byte{0, 0}
	gwAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(igd.PxPPort())}
	_, err = conn.WriteTo(req, gwAddr)
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}

	buf := make([]byte, 256)
	if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("failed to set deadline: %v", err)
	}
	_, _, err = conn.ReadFrom(buf)
	if err == nil {
		t.Error("expected timeout when protocol disabled")
	}
}
