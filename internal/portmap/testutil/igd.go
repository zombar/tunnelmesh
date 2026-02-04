// Package testutil provides test infrastructure for port mapping.
package testutil

import (
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
)

// Protocol constants for NAT-PMP and PCP.
const (
	pmpVersion         = 0
	pmpOpMapPublicAddr = 0
	pmpOpMapUDP        = 1
	pmpOpMapTCP        = 2
	pmpOpReply         = 0x80

	pcpVersion    = 2
	pcpOpAnnounce = 0
	pcpOpMap      = 1
	pcpOpReply    = 0x80

	pcpUDPMapping = 17
	pcpTCPMapping = 6
)

// TestIGD is a mock Internet Gateway Device for testing port mapping protocols.
// It supports NAT-PMP, PCP, and UPnP discovery.
type TestIGD struct {
	mu sync.RWMutex

	// Connections
	pxpConn  net.PacketConn // for NAT-PMP and PCP (port 5351)
	upnpConn net.PacketConn // for UPnP discovery (port 1900)
	httpSrv  *httptest.Server

	// Protocol enable flags
	enablePMP  bool
	enablePCP  bool
	enableUPnP bool

	// Configurable responses (protected by mu)
	externalIP  net.IP // External IP to return
	gateway     net.IP // Gateway IP (usually 192.168.1.1)
	mappedPort  uint16 // External port to assign (0 = same as internal)
	mapLifetime uint32 // Lifetime in seconds (default 7200)
	pcpEpoch    uint32 // PCP epoch value
	resultCode  uint8  // Result code to return (0 = success)
	failProbe   bool   // If true, don't respond to probes
	failMapping bool   // If true, don't respond to mapping requests

	// Counters
	Counters IGDCounters

	closed atomic.Bool
}

// SetFailProbe sets whether the IGD should fail to respond to probes.
func (igd *TestIGD) SetFailProbe(fail bool) {
	igd.mu.Lock()
	defer igd.mu.Unlock()
	igd.failProbe = fail
}

// SetFailMapping sets whether the IGD should fail to respond to mapping requests.
func (igd *TestIGD) SetFailMapping(fail bool) {
	igd.mu.Lock()
	defer igd.mu.Unlock()
	igd.failMapping = fail
}

// SetMappedPort sets the external port to assign for mappings.
func (igd *TestIGD) SetMappedPort(port uint16) {
	igd.mu.Lock()
	defer igd.mu.Unlock()
	igd.mappedPort = port
}

// SetExternalIP sets the external IP to return.
func (igd *TestIGD) SetExternalIP(ip net.IP) {
	igd.mu.Lock()
	defer igd.mu.Unlock()
	igd.externalIP = ip
}

// SetResultCode sets the result code to return.
func (igd *TestIGD) SetResultCode(code uint8) {
	igd.mu.Lock()
	defer igd.mu.Unlock()
	igd.resultCode = code
}

// getConfig returns a snapshot of the config for use in handlers.
type igdConfig struct {
	externalIP  net.IP
	gateway     net.IP
	mappedPort  uint16
	mapLifetime uint32
	pcpEpoch    uint32
	resultCode  uint8
	failProbe   bool
	failMapping bool
	enablePMP   bool
	enablePCP   bool
	enableUPnP  bool
}

func (igd *TestIGD) getConfig() igdConfig {
	igd.mu.RLock()
	defer igd.mu.RUnlock()
	return igdConfig{
		externalIP:  igd.externalIP,
		gateway:     igd.gateway,
		mappedPort:  igd.mappedPort,
		mapLifetime: igd.mapLifetime,
		pcpEpoch:    igd.pcpEpoch,
		resultCode:  igd.resultCode,
		failProbe:   igd.failProbe,
		failMapping: igd.failMapping,
		enablePMP:   igd.enablePMP,
		enablePCP:   igd.enablePCP,
		enableUPnP:  igd.enableUPnP,
	}
}

// ExternalIP returns the configured external IP.
func (igd *TestIGD) ExternalIP() net.IP {
	igd.mu.RLock()
	defer igd.mu.RUnlock()
	return igd.externalIP
}

// IGDCounters tracks protocol events for testing.
type IGDCounters struct {
	mu sync.Mutex

	PMPPublicAddrRecv int
	PMPMapUDPRecv     int
	PMPMapTCPRecv     int
	PCPAnnounceRecv   int
	PCPMapRecv        int
	UPnPDiscoRecv     int
	FailedWrites      int
}

// Reset clears all counters.
func (c *IGDCounters) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.PMPPublicAddrRecv = 0
	c.PMPMapUDPRecv = 0
	c.PMPMapTCPRecv = 0
	c.PCPAnnounceRecv = 0
	c.PCPMapRecv = 0
	c.UPnPDiscoRecv = 0
	c.FailedWrites = 0
}

func (c *IGDCounters) inc(p *int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	(*p)++
}

// Snapshot returns a copy of the counters.
func (c *IGDCounters) Snapshot() IGDCounters {
	c.mu.Lock()
	defer c.mu.Unlock()
	return IGDCounters{
		PMPPublicAddrRecv: c.PMPPublicAddrRecv,
		PMPMapUDPRecv:     c.PMPMapUDPRecv,
		PMPMapTCPRecv:     c.PMPMapTCPRecv,
		PCPAnnounceRecv:   c.PCPAnnounceRecv,
		PCPMapRecv:        c.PCPMapRecv,
		UPnPDiscoRecv:     c.UPnPDiscoRecv,
		FailedWrites:      c.FailedWrites,
	}
}

// TestIGDOptions configures which protocols the mock IGD supports.
type TestIGDOptions struct {
	PMP  bool
	PCP  bool
	UPnP bool
}

// NewTestIGD creates a new mock IGD with the specified protocol support.
func NewTestIGD(opts TestIGDOptions) (*TestIGD, error) {
	igd := &TestIGD{
		enablePMP:   opts.PMP,
		enablePCP:   opts.PCP,
		enableUPnP:  opts.UPnP,
		externalIP:  net.ParseIP("203.0.113.5"),
		gateway:     net.ParseIP("192.168.1.1"),
		mapLifetime: 7200,
		pcpEpoch:    1000,
	}

	var err error

	// Create PxP listener (NAT-PMP/PCP)
	igd.pxpConn, err = net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	// Create UPnP discovery listener
	igd.upnpConn, err = net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		igd.pxpConn.Close()
		return nil, err
	}

	// Create HTTP server for UPnP
	igd.httpSrv = httptest.NewServer(http.HandlerFunc(igd.serveUPnPHTTP))

	// Start protocol handlers
	go igd.servePxP()
	go igd.serveUPnPDiscovery()

	return igd, nil
}

// PxPPort returns the port number for NAT-PMP/PCP communication.
func (igd *TestIGD) PxPPort() uint16 {
	return uint16(igd.pxpConn.LocalAddr().(*net.UDPAddr).Port)
}

// UPnPPort returns the port number for UPnP discovery.
func (igd *TestIGD) UPnPPort() uint16 {
	return uint16(igd.upnpConn.LocalAddr().(*net.UDPAddr).Port)
}

// HTTPAddr returns the HTTP server address for UPnP.
func (igd *TestIGD) HTTPAddr() string {
	return igd.httpSrv.URL
}

// Close shuts down the mock IGD.
func (igd *TestIGD) Close() error {
	igd.closed.Store(true)
	igd.pxpConn.Close()
	igd.upnpConn.Close()
	igd.httpSrv.Close()
	return nil
}

// servePxP handles NAT-PMP and PCP requests.
func (igd *TestIGD) servePxP() {
	buf := make([]byte, 1500)
	for {
		n, addr, err := igd.pxpConn.ReadFrom(buf)
		if err != nil {
			if !igd.closed.Load() {
				// Log error in production
			}
			return
		}

		pkt := buf[:n]
		if len(pkt) < 2 {
			continue
		}

		ver := pkt[0]
		switch ver {
		case pmpVersion:
			igd.handlePMP(pkt, addr)
		case pcpVersion:
			igd.handlePCP(pkt, addr)
		}
	}
}

// handlePMP processes NAT-PMP requests.
func (igd *TestIGD) handlePMP(pkt []byte, addr net.Addr) {
	if len(pkt) < 2 {
		return
	}

	cfg := igd.getConfig()

	op := pkt[1]
	switch op {
	case pmpOpMapPublicAddr:
		igd.Counters.inc(&igd.Counters.PMPPublicAddrRecv)
		if !cfg.enablePMP || cfg.failProbe {
			return
		}
		resp := buildPMPPublicAddrResponse(cfg)
		if _, err := igd.pxpConn.WriteTo(resp, addr); err != nil {
			igd.Counters.inc(&igd.Counters.FailedWrites)
		}

	case pmpOpMapUDP:
		igd.Counters.inc(&igd.Counters.PMPMapUDPRecv)
		if !cfg.enablePMP || cfg.failMapping {
			return
		}
		if len(pkt) < 12 {
			return
		}
		internalPort := binary.BigEndian.Uint16(pkt[4:6])
		suggestedPort := binary.BigEndian.Uint16(pkt[6:8])
		lifetime := binary.BigEndian.Uint32(pkt[8:12])
		resp := buildPMPMappingResponse(cfg, pmpOpMapUDP, internalPort, suggestedPort, lifetime)
		if _, err := igd.pxpConn.WriteTo(resp, addr); err != nil {
			igd.Counters.inc(&igd.Counters.FailedWrites)
		}

	case pmpOpMapTCP:
		igd.Counters.inc(&igd.Counters.PMPMapTCPRecv)
		if !cfg.enablePMP || cfg.failMapping {
			return
		}
		if len(pkt) < 12 {
			return
		}
		internalPort := binary.BigEndian.Uint16(pkt[4:6])
		suggestedPort := binary.BigEndian.Uint16(pkt[6:8])
		lifetime := binary.BigEndian.Uint32(pkt[8:12])
		resp := buildPMPMappingResponse(cfg, pmpOpMapTCP, internalPort, suggestedPort, lifetime)
		if _, err := igd.pxpConn.WriteTo(resp, addr); err != nil {
			igd.Counters.inc(&igd.Counters.FailedWrites)
		}
	}
}

// buildPMPPublicAddrResponse creates a NAT-PMP public address response.
func buildPMPPublicAddrResponse(cfg igdConfig) []byte {
	resp := make([]byte, 12)
	resp[0] = pmpVersion
	resp[1] = pmpOpMapPublicAddr | pmpOpReply
	// resp[2:4] = result code (0 = success)
	binary.BigEndian.PutUint16(resp[2:4], uint16(cfg.resultCode))
	// resp[4:8] = epoch time
	binary.BigEndian.PutUint32(resp[4:8], cfg.pcpEpoch)
	// resp[8:12] = external IP (IPv4)
	copy(resp[8:12], cfg.externalIP.To4())
	return resp
}

// buildPMPMappingResponse creates a NAT-PMP mapping response.
func buildPMPMappingResponse(cfg igdConfig, op uint8, internalPort, suggestedPort uint16, lifetime uint32) []byte {
	resp := make([]byte, 16)
	resp[0] = pmpVersion
	resp[1] = op | pmpOpReply
	binary.BigEndian.PutUint16(resp[2:4], uint16(cfg.resultCode))
	binary.BigEndian.PutUint32(resp[4:8], cfg.pcpEpoch)
	binary.BigEndian.PutUint16(resp[8:10], internalPort)

	// External port - use MappedPort if set, otherwise use suggested or internal
	externalPort := suggestedPort
	if externalPort == 0 {
		externalPort = internalPort
	}
	if cfg.mappedPort != 0 {
		externalPort = cfg.mappedPort
	}
	binary.BigEndian.PutUint16(resp[10:12], externalPort)

	// Lifetime - use configured or requested
	lt := lifetime
	if cfg.mapLifetime != 0 && lifetime != 0 {
		lt = cfg.mapLifetime
	}
	binary.BigEndian.PutUint32(resp[12:16], lt)

	return resp
}

// handlePCP processes PCP requests.
func (igd *TestIGD) handlePCP(pkt []byte, addr net.Addr) {
	if len(pkt) < 24 {
		return
	}

	cfg := igd.getConfig()

	op := pkt[1]
	switch op {
	case pcpOpAnnounce:
		igd.Counters.inc(&igd.Counters.PCPAnnounceRecv)
		if !cfg.enablePCP || cfg.failProbe {
			return
		}
		resp := buildPCPAnnounceResponse(cfg, pkt)
		if _, err := igd.pxpConn.WriteTo(resp, addr); err != nil {
			igd.Counters.inc(&igd.Counters.FailedWrites)
		}

	case pcpOpMap:
		igd.Counters.inc(&igd.Counters.PCPMapRecv)
		if !cfg.enablePCP || cfg.failMapping {
			return
		}
		if len(pkt) < 60 {
			return
		}
		resp := buildPCPMapResponse(cfg, pkt)
		if _, err := igd.pxpConn.WriteTo(resp, addr); err != nil {
			igd.Counters.inc(&igd.Counters.FailedWrites)
		}
	}
}

// buildPCPAnnounceResponse creates a PCP announce response.
func buildPCPAnnounceResponse(cfg igdConfig, req []byte) []byte {
	resp := make([]byte, 24)
	resp[0] = pcpVersion
	resp[1] = pcpOpAnnounce | pcpOpReply
	// resp[2] = reserved
	resp[3] = cfg.resultCode // result code
	// resp[4:8] = lifetime (0 for announce)
	binary.BigEndian.PutUint32(resp[8:12], cfg.pcpEpoch)
	// resp[12:24] = reserved
	return resp
}

// buildPCPMapResponse creates a PCP map response.
func buildPCPMapResponse(cfg igdConfig, req []byte) []byte {
	resp := make([]byte, 60)
	resp[0] = pcpVersion
	resp[1] = pcpOpMap | pcpOpReply
	resp[3] = cfg.resultCode

	// Lifetime
	reqLifetime := binary.BigEndian.Uint32(req[4:8])
	lifetime := reqLifetime
	if cfg.mapLifetime != 0 && reqLifetime != 0 {
		lifetime = cfg.mapLifetime
	}
	binary.BigEndian.PutUint32(resp[4:8], lifetime)

	// Epoch
	binary.BigEndian.PutUint32(resp[8:12], cfg.pcpEpoch)

	// Copy nonce from request (bytes 24-36)
	copy(resp[24:36], req[24:36])

	// Protocol (byte 36)
	resp[36] = req[36]

	// Internal port (bytes 40-42 in request)
	internalPort := binary.BigEndian.Uint16(req[40:42])
	binary.BigEndian.PutUint16(resp[40:42], internalPort)

	// External port - use MappedPort if set
	suggestedPort := binary.BigEndian.Uint16(req[42:44])
	externalPort := suggestedPort
	if externalPort == 0 {
		externalPort = internalPort
	}
	if cfg.mappedPort != 0 {
		externalPort = cfg.mappedPort
	}
	binary.BigEndian.PutUint16(resp[42:44], externalPort)

	// External IP (IPv4-mapped IPv6)
	extIP := cfg.externalIP.To4()
	if extIP == nil {
		extIP = net.IPv4(0, 0, 0, 0)
	}
	// Write as IPv4-mapped IPv6 (::ffff:a.b.c.d)
	resp[44] = 0
	resp[45] = 0
	resp[46] = 0
	resp[47] = 0
	resp[48] = 0
	resp[49] = 0
	resp[50] = 0
	resp[51] = 0
	resp[52] = 0
	resp[53] = 0
	resp[54] = 0xff
	resp[55] = 0xff
	copy(resp[56:60], extIP)

	return resp
}

// UPnP discovery packet (simplified)
var upnpDiscoveryPacket = []byte("M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 1\r\nST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\n\r\n")

// serveUPnPDiscovery handles UPnP SSDP discovery.
func (igd *TestIGD) serveUPnPDiscovery() {
	buf := make([]byte, 1500)
	for {
		n, addr, err := igd.upnpConn.ReadFrom(buf)
		if err != nil {
			if !igd.closed.Load() {
				// Log error in production
			}
			return
		}

		pkt := buf[:n]

		// Simple check for M-SEARCH
		if len(pkt) > 10 && string(pkt[:8]) == "M-SEARCH" {
			igd.Counters.inc(&igd.Counters.UPnPDiscoRecv)
			cfg := igd.getConfig()
			if !cfg.enableUPnP || cfg.failProbe {
				continue
			}

			resp := igd.buildUPnPDiscoveryResponse()
			if _, err := igd.upnpConn.WriteTo(resp, addr); err != nil {
				igd.Counters.inc(&igd.Counters.FailedWrites)
			}
		}
	}
}

// buildUPnPDiscoveryResponse creates a UPnP SSDP response.
func (igd *TestIGD) buildUPnPDiscoveryResponse() []byte {
	return []byte("HTTP/1.1 200 OK\r\n" +
		"CACHE-CONTROL: max-age=120\r\n" +
		"ST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\n" +
		"USN: uuid:test-igd-1234::urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\n" +
		"EXT:\r\n" +
		"SERVER: TestIGD/1.0 UPnP/1.1\r\n" +
		"LOCATION: " + igd.httpSrv.URL + "/rootDesc.xml\r\n" +
		"\r\n")
}

// serveUPnPHTTP handles UPnP HTTP requests.
func (igd *TestIGD) serveUPnPHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/rootDesc.xml":
		w.Header().Set("Content-Type", "text/xml")
		w.Write([]byte(igd.rootDescXML()))
	case "/WANIPConnection":
		igd.handleUPnPAction(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (igd *TestIGD) rootDescXML() string {
	return `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:1</deviceType>
    <friendlyName>TestIGD</friendlyName>
    <deviceList>
      <device>
        <deviceType>urn:schemas-upnp-org:device:WANDevice:1</deviceType>
        <deviceList>
          <device>
            <deviceType>urn:schemas-upnp-org:device:WANConnectionDevice:1</deviceType>
            <serviceList>
              <service>
                <serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
                <controlURL>` + igd.httpSrv.URL + `/WANIPConnection</controlURL>
              </service>
            </serviceList>
          </device>
        </deviceList>
      </device>
    </deviceList>
  </device>
</root>`
}

func (igd *TestIGD) handleUPnPAction(w http.ResponseWriter, r *http.Request) {
	action := r.Header.Get("SOAPAction")
	cfg := igd.getConfig()

	switch {
	case contains(action, "GetExternalIPAddress"):
		w.Header().Set("Content-Type", "text/xml")
		w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
      <NewExternalIPAddress>` + cfg.externalIP.String() + `</NewExternalIPAddress>
    </u:GetExternalIPAddressResponse>
  </s:Body>
</s:Envelope>`))

	case contains(action, "AddPortMapping"):
		if cfg.failMapping {
			http.Error(w, "Mapping failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:AddPortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
    </u:AddPortMappingResponse>
  </s:Body>
</s:Envelope>`))

	case contains(action, "DeletePortMapping"):
		w.Header().Set("Content-Type", "text/xml")
		w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:DeletePortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
    </u:DeletePortMappingResponse>
  </s:Body>
</s:Envelope>`))

	default:
		http.Error(w, "Unknown action", http.StatusBadRequest)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
