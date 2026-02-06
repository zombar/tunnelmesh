package dns

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/testutil"
)

func TestResolver_AddRecord(t *testing.T) {
	r := NewResolver(".tunnelmesh", 60)

	r.AddRecord("mynode", "172.30.0.1")
	r.AddRecord("other", "172.30.0.2")

	ip, ok := r.Resolve("mynode.tunnelmesh")
	assert.True(t, ok)
	assert.Equal(t, "172.30.0.1", ip)

	ip, ok = r.Resolve("other.tunnelmesh")
	assert.True(t, ok)
	assert.Equal(t, "172.30.0.2", ip)
}

func TestResolver_RemoveRecord(t *testing.T) {
	r := NewResolver(".tunnelmesh", 60)

	r.AddRecord("mynode", "172.30.0.1")

	ip, ok := r.Resolve("mynode.tunnelmesh")
	assert.True(t, ok)
	assert.Equal(t, "172.30.0.1", ip)

	r.RemoveRecord("mynode")

	_, ok = r.Resolve("mynode.tunnelmesh")
	assert.False(t, ok)
}

func TestResolver_UpdateRecords(t *testing.T) {
	r := NewResolver(".tunnelmesh", 60)

	// Initial records
	r.AddRecord("node1", "172.30.0.1")
	r.AddRecord("node2", "172.30.0.2")

	// Bulk update - should replace all
	r.UpdateRecords(map[string]string{
		"node3": "172.30.0.3",
		"node4": "172.30.0.4",
	})

	// Old records should be gone
	_, ok := r.Resolve("node1.tunnelmesh")
	assert.False(t, ok)
	_, ok = r.Resolve("node2.tunnelmesh")
	assert.False(t, ok)

	// New records should exist
	ip, ok := r.Resolve("node3.tunnelmesh")
	assert.True(t, ok)
	assert.Equal(t, "172.30.0.3", ip)
}

func TestResolver_ResolveWithoutSuffix(t *testing.T) {
	r := NewResolver(".tunnelmesh", 60)
	r.AddRecord("mynode", "172.30.0.1")

	// Should work with or without suffix
	ip, ok := r.Resolve("mynode.tunnelmesh")
	assert.True(t, ok)
	assert.Equal(t, "172.30.0.1", ip)

	ip, ok = r.Resolve("mynode")
	assert.True(t, ok)
	assert.Equal(t, "172.30.0.1", ip)
}

func TestResolver_ResolveNonexistent(t *testing.T) {
	r := NewResolver(".tunnelmesh", 60)

	_, ok := r.Resolve("unknown.tunnelmesh")
	assert.False(t, ok)
}

func TestResolver_DNSServer(t *testing.T) {
	port := testutil.FreePort(t)
	addr := "127.0.0.1:" + strconv.Itoa(port)

	r := NewResolver(".tunnelmesh", 60)
	r.AddRecord("testhost", "172.30.0.42")

	// Start server
	go func() {
		err := r.ListenAndServe(addr)
		if err != nil {
			t.Logf("DNS server error: %v", err)
		}
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)
	defer func() { _ = r.Shutdown() }()

	// Query the DNS server
	c := new(dns.Client)
	m := new(dns.Msg)
	m.SetQuestion("testhost.tunnelmesh.", dns.TypeA)

	resp, _, err := c.Exchange(m, addr)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Answer, 1)

	a, ok := resp.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, net.ParseIP("172.30.0.42").To4(), a.A.To4())
}

func TestResolver_DNSServer_NXDOMAIN(t *testing.T) {
	port := testutil.FreePort(t)
	addr := "127.0.0.1:" + strconv.Itoa(port)

	r := NewResolver(".tunnelmesh", 60)

	go func() {
		_ = r.ListenAndServe(addr)
	}()

	time.Sleep(100 * time.Millisecond)
	defer func() { _ = r.Shutdown() }()

	c := new(dns.Client)
	m := new(dns.Msg)
	m.SetQuestion("unknown.tunnelmesh.", dns.TypeA)

	resp, _, err := c.Exchange(m, addr)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, dns.RcodeNameError, resp.Rcode)
}

func TestResolver_ListRecords(t *testing.T) {
	r := NewResolver(".tunnelmesh", 60)

	r.AddRecord("node1", "172.30.0.1")
	r.AddRecord("node2", "172.30.0.2")

	records := r.ListRecords()
	assert.Len(t, records, 2)
	assert.Equal(t, "172.30.0.1", records["node1"])
	assert.Equal(t, "172.30.0.2", records["node2"])
}
