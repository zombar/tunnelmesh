package client

import (
	"context"
	"net"
	"sync"
	"time"
)

// MockClient is a mock implementation of Client for testing.
type MockClient struct {
	mu sync.Mutex

	// ProbeFunc is called by Probe. If nil, returns ProbeGateway and ProbeErr.
	ProbeFunc func(ctx context.Context) (net.IP, error)
	ProbeGateway net.IP
	ProbeErr     error

	// GetExternalAddressFunc is called by GetExternalAddress.
	GetExternalAddressFunc func(ctx context.Context) (net.IP, error)
	ExternalIP             net.IP
	GetExternalErr         error

	// RequestMappingFunc is called by RequestMapping.
	RequestMappingFunc func(ctx context.Context, protocol Protocol, internalPort int, lifetime time.Duration) (*Mapping, error)
	RequestMapping_    *Mapping
	RequestMappingErr  error

	// RefreshMappingFunc is called by RefreshMapping.
	RefreshMappingFunc func(ctx context.Context, mapping *Mapping) (*Mapping, error)
	RefreshMapping_    *Mapping
	RefreshMappingErr  error

	// DeleteMappingFunc is called by DeleteMapping.
	DeleteMappingFunc func(ctx context.Context, mapping *Mapping) error
	DeleteMappingErr  error

	// Tracking for assertions
	ProbeCalls           int
	RequestMappingCalls  int
	RefreshMappingCalls  int
	DeleteMappingCalls   int
	CloseCalls           int

	closed bool
}

// NewMockClient creates a new MockClient with default success responses.
func NewMockClient() *MockClient {
	return &MockClient{
		ProbeGateway: net.ParseIP("192.168.1.1"),
		ExternalIP:   net.ParseIP("203.0.113.5"),
		RequestMapping_: &Mapping{
			Protocol:     UDP,
			InternalPort: 51820,
			ExternalPort: 51820,
			ExternalIP:   net.ParseIP("203.0.113.5"),
			Gateway:      net.ParseIP("192.168.1.1"),
			Lifetime:     2 * time.Hour,
		},
		RefreshMapping_: &Mapping{
			Protocol:     UDP,
			InternalPort: 51820,
			ExternalPort: 51820,
			ExternalIP:   net.ParseIP("203.0.113.5"),
			Gateway:      net.ParseIP("192.168.1.1"),
			Lifetime:     2 * time.Hour,
		},
	}
}

func (m *MockClient) Name() string {
	return "mock"
}

func (m *MockClient) Probe(ctx context.Context) (net.IP, error) {
	m.mu.Lock()
	m.ProbeCalls++
	m.mu.Unlock()

	if m.ProbeFunc != nil {
		return m.ProbeFunc(ctx)
	}
	return m.ProbeGateway, m.ProbeErr
}

func (m *MockClient) GetExternalAddress(ctx context.Context) (net.IP, error) {
	m.mu.Lock()
	externalIP := m.ExternalIP
	getExternalErr := m.GetExternalErr
	getExternalAddressFunc := m.GetExternalAddressFunc
	m.mu.Unlock()

	if getExternalAddressFunc != nil {
		return getExternalAddressFunc(ctx)
	}
	return externalIP, getExternalErr
}

func (m *MockClient) RequestMapping(ctx context.Context, protocol Protocol, internalPort int, lifetime time.Duration) (*Mapping, error) {
	m.mu.Lock()
	m.RequestMappingCalls++
	m.mu.Unlock()

	if m.RequestMappingFunc != nil {
		return m.RequestMappingFunc(ctx, protocol, internalPort, lifetime)
	}
	if m.RequestMappingErr != nil {
		return nil, m.RequestMappingErr
	}
	// Return a copy with updated internal port
	mapping := *m.RequestMapping_
	mapping.InternalPort = internalPort
	mapping.Protocol = protocol
	return &mapping, nil
}

func (m *MockClient) RefreshMapping(ctx context.Context, mapping *Mapping) (*Mapping, error) {
	m.mu.Lock()
	m.RefreshMappingCalls++
	m.mu.Unlock()

	if m.RefreshMappingFunc != nil {
		return m.RefreshMappingFunc(ctx, mapping)
	}
	if m.RefreshMappingErr != nil {
		return nil, m.RefreshMappingErr
	}
	// Return a copy
	refreshed := *m.RefreshMapping_
	refreshed.InternalPort = mapping.InternalPort
	refreshed.Protocol = mapping.Protocol
	return &refreshed, nil
}

func (m *MockClient) DeleteMapping(ctx context.Context, mapping *Mapping) error {
	m.mu.Lock()
	m.DeleteMappingCalls++
	m.mu.Unlock()

	if m.DeleteMappingFunc != nil {
		return m.DeleteMappingFunc(ctx, mapping)
	}
	return m.DeleteMappingErr
}

func (m *MockClient) Close() error {
	m.mu.Lock()
	m.CloseCalls++
	m.closed = true
	m.mu.Unlock()
	return nil
}

// Reset clears all call counts.
func (m *MockClient) Reset() {
	m.mu.Lock()
	m.ProbeCalls = 0
	m.RequestMappingCalls = 0
	m.RefreshMappingCalls = 0
	m.DeleteMappingCalls = 0
	m.CloseCalls = 0
	m.mu.Unlock()
}

// SetProbeError sets the error to return from Probe.
func (m *MockClient) SetProbeError(err error) {
	m.mu.Lock()
	m.ProbeErr = err
	m.mu.Unlock()
}

// SetRequestMappingError sets the error to return from RequestMapping.
func (m *MockClient) SetRequestMappingError(err error) {
	m.mu.Lock()
	m.RequestMappingErr = err
	m.mu.Unlock()
}

// SetRefreshMappingError sets the error to return from RefreshMapping.
func (m *MockClient) SetRefreshMappingError(err error) {
	m.mu.Lock()
	m.RefreshMappingErr = err
	m.mu.Unlock()
}

// GetProbeCalls returns the number of probe calls.
func (m *MockClient) GetProbeCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ProbeCalls
}

// GetRequestMappingCalls returns the number of request mapping calls.
func (m *MockClient) GetRequestMappingCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.RequestMappingCalls
}

// GetRefreshMappingCalls returns the number of refresh mapping calls.
func (m *MockClient) GetRefreshMappingCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.RefreshMappingCalls
}

// GetDeleteMappingCalls returns the number of delete mapping calls.
func (m *MockClient) GetDeleteMappingCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.DeleteMappingCalls
}
