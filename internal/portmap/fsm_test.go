package portmap

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/portmap/client"
)

// testObserver records all observer calls for verification.
type testObserver struct {
	mu              sync.Mutex
	stateChanges    []stateChange
	mappingsAcquired []*Mapping
	mappingsLost    []error
}

type stateChange struct {
	oldState State
	newState State
}

func (o *testObserver) OnPortMapStateChanged(pm *PortMapper, oldState, newState State) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stateChanges = append(o.stateChanges, stateChange{oldState, newState})
}

func (o *testObserver) OnMappingAcquired(pm *PortMapper, mapping *Mapping) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.mappingsAcquired = append(o.mappingsAcquired, mapping)
}

func (o *testObserver) OnMappingLost(pm *PortMapper, reason error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.mappingsLost = append(o.mappingsLost, reason)
}

func (o *testObserver) getStateChanges() []stateChange {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]stateChange{}, o.stateChanges...)
}

func (o *testObserver) getMappingsAcquired() []*Mapping {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]*Mapping{}, o.mappingsAcquired...)
}

func (o *testObserver) getMappingsLost() []error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]error{}, o.mappingsLost...)
}

func TestFSM_InitialState(t *testing.T) {
	mock := client.NewMockClient()
	pm := New(Config{
		Protocol:  ProtocolUDP,
		LocalPort: 51820,
		Client:    mock,
	})
	defer func() { _ = pm.Stop() }()

	assert.Equal(t, StateIdle, pm.State())
	assert.Nil(t, pm.Mapping())
	assert.False(t, pm.IsActive())
}

func TestFSM_StartDiscovery(t *testing.T) {
	mock := client.NewMockClient()
	obs := &testObserver{}

	pm := New(Config{
		Protocol:  ProtocolUDP,
		LocalPort: 51820,
		Client:    mock,
		Observers: []Observer{obs},
	})
	defer func() { _ = pm.Stop() }()

	err := pm.Start()
	require.NoError(t, err)

	// Wait for discovery to complete
	waitForState(t, pm, StateActive, 2*time.Second)

	changes := obs.getStateChanges()
	require.GreaterOrEqual(t, len(changes), 2)

	// Should go Idle -> Discovering -> Requesting -> Active
	assert.Equal(t, StateIdle, changes[0].oldState)
	assert.Equal(t, StateDiscovering, changes[0].newState)
}

func TestFSM_DiscoverySuccess(t *testing.T) {
	mock := client.NewMockClient()
	obs := &testObserver{}

	pm := New(Config{
		Protocol:  ProtocolUDP,
		LocalPort: 51820,
		Client:    mock,
		Observers: []Observer{obs},
	})
	defer func() { _ = pm.Stop() }()

	err := pm.Start()
	require.NoError(t, err)

	// Wait for active state
	waitForState(t, pm, StateActive, 2*time.Second)

	assert.Equal(t, StateActive, pm.State())
	assert.True(t, pm.IsActive())
	assert.NotNil(t, pm.Mapping())
	assert.Equal(t, 51820, pm.Mapping().InternalPort)

	// Observer should have received mapping acquired
	mappings := obs.getMappingsAcquired()
	require.Len(t, mappings, 1)
	assert.Equal(t, 51820, mappings[0].InternalPort)
}

func TestFSM_DiscoveryFailure(t *testing.T) {
	mock := client.NewMockClient()
	mock.SetProbeError(client.ErrNoGatewayFound)
	obs := &testObserver{}

	pm := New(Config{
		Protocol:  ProtocolUDP,
		LocalPort: 51820,
		Client:    mock,
		Observers: []Observer{obs},
	})
	defer func() { _ = pm.Stop() }()

	err := pm.Start()
	require.NoError(t, err)

	// Wait for failed state
	waitForState(t, pm, StateFailed, 2*time.Second)

	assert.Equal(t, StateFailed, pm.State())
	assert.False(t, pm.IsActive())
	assert.Nil(t, pm.Mapping())
	assert.NotNil(t, pm.LastError())
}

func TestFSM_MappingFailure(t *testing.T) {
	mock := client.NewMockClient()
	mock.SetRequestMappingError(client.ErrMappingFailed)
	obs := &testObserver{}

	pm := New(Config{
		Protocol:  ProtocolUDP,
		LocalPort: 51820,
		Client:    mock,
		Observers: []Observer{obs},
	})
	defer func() { _ = pm.Stop() }()

	err := pm.Start()
	require.NoError(t, err)

	// Wait for failed state
	waitForState(t, pm, StateFailed, 2*time.Second)

	assert.Equal(t, StateFailed, pm.State())
	assert.False(t, pm.IsActive())
}

func TestFSM_RefreshSuccess(t *testing.T) {
	mock := client.NewMockClient()
	obs := &testObserver{}

	// Use very short lifetime to trigger refresh quickly
	mock.RequestMapping_.Lifetime = 100 * time.Millisecond

	pm := New(Config{
		Protocol:      ProtocolUDP,
		LocalPort:     51820,
		Client:        mock,
		Observers:     []Observer{obs},
		RefreshMargin: 50 * time.Millisecond,
	})
	defer func() { _ = pm.Stop() }()

	err := pm.Start()
	require.NoError(t, err)

	// Wait for active state
	waitForState(t, pm, StateActive, 2*time.Second)

	// Wait for refresh to occur
	time.Sleep(200 * time.Millisecond)

	// Should have called refresh
	assert.GreaterOrEqual(t, mock.GetRefreshMappingCalls(), 1)
}

func TestFSM_RefreshFailure(t *testing.T) {
	mock := client.NewMockClient()
	obs := &testObserver{}

	// Use very short lifetime to trigger refresh quickly
	mock.RequestMapping_.Lifetime = 100 * time.Millisecond

	pm := New(Config{
		Protocol:      ProtocolUDP,
		LocalPort:     51820,
		Client:        mock,
		Observers:     []Observer{obs},
		RefreshMargin: 50 * time.Millisecond,
	})
	defer func() { _ = pm.Stop() }()

	err := pm.Start()
	require.NoError(t, err)

	// Wait for active state
	waitForState(t, pm, StateActive, 2*time.Second)

	// Set refresh to fail
	mock.SetRefreshMappingError(errors.New("refresh failed"))

	// Wait for refresh to fail and mapping to be lost
	time.Sleep(300 * time.Millisecond)

	// Observer should have received mapping lost
	lost := obs.getMappingsLost()
	assert.GreaterOrEqual(t, len(lost), 1)
}

func TestFSM_NetworkChange_FromActive(t *testing.T) {
	mock := client.NewMockClient()
	obs := &testObserver{}

	pm := New(Config{
		Protocol:  ProtocolUDP,
		LocalPort: 51820,
		Client:    mock,
		Observers: []Observer{obs},
	})
	defer func() { _ = pm.Stop() }()

	err := pm.Start()
	require.NoError(t, err)

	// Wait for active state
	waitForState(t, pm, StateActive, 2*time.Second)
	assert.Equal(t, StateActive, pm.State())

	initialAcquired := len(obs.getMappingsAcquired())
	initialLost := len(obs.getMappingsLost())

	// Simulate network change
	pm.NetworkChanged()

	// Wait for state to leave Active first (network change processing)
	time.Sleep(50 * time.Millisecond) // Give FSM time to process

	// Wait for it to become active again
	waitForState(t, pm, StateActive, 2*time.Second)
	assert.True(t, pm.IsActive())

	// Observer should have received mapping lost
	lost := obs.getMappingsLost()
	assert.Greater(t, len(lost), initialLost, "should have received mapping lost notification")

	// Should have re-acquired mapping
	acquired := obs.getMappingsAcquired()
	assert.Greater(t, len(acquired), initialAcquired, "should have re-acquired mapping")

	// State changes should include a transition to discovering
	changes := obs.getStateChanges()
	hasDiscovering := false
	for _, c := range changes {
		if c.newState == StateDiscovering {
			hasDiscovering = true
			break
		}
	}
	assert.True(t, hasDiscovering, "should have transitioned to discovering")
}

func TestFSM_NetworkChange_FromDiscovering(t *testing.T) {
	mock := client.NewMockClient()

	// Make probe slow so we can test network change during discovery
	mock.ProbeFunc = func(ctx context.Context) (net.IP, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
			return net.ParseIP("192.168.1.1"), nil
		}
	}

	pm := New(Config{
		Protocol:  ProtocolUDP,
		LocalPort: 51820,
		Client:    mock,
	})
	defer func() { _ = pm.Stop() }()

	err := pm.Start()
	require.NoError(t, err)

	// Wait for discovering state
	waitForState(t, pm, StateDiscovering, 1*time.Second)

	// Simulate network change during discovery
	pm.NetworkChanged()

	// Wait for the first probe to complete (500ms) plus time for network change
	// to be processed and second probe to start
	time.Sleep(700 * time.Millisecond)

	// Should have called probe at least twice (initial + after network change)
	assert.GreaterOrEqual(t, mock.GetProbeCalls(), 2, "probe should be called again after network change")
}

func TestFSM_Stop_FromIdle(t *testing.T) {
	mock := client.NewMockClient()

	pm := New(Config{
		Protocol:  ProtocolUDP,
		LocalPort: 51820,
		Client:    mock,
	})

	err := pm.Stop()
	require.NoError(t, err)

	assert.Equal(t, StateStopped, pm.State())
}

func TestFSM_Stop_FromActive(t *testing.T) {
	mock := client.NewMockClient()
	obs := &testObserver{}

	pm := New(Config{
		Protocol:  ProtocolUDP,
		LocalPort: 51820,
		Client:    mock,
		Observers: []Observer{obs},
	})

	err := pm.Start()
	require.NoError(t, err)

	// Wait for active state
	waitForState(t, pm, StateActive, 2*time.Second)

	err = pm.Stop()
	require.NoError(t, err)

	assert.Equal(t, StateStopped, pm.State())
	assert.False(t, pm.IsActive())
	assert.Nil(t, pm.Mapping())

	// Should have called DeleteMapping
	assert.Equal(t, 1, mock.GetDeleteMappingCalls())

	// Observer should have received mapping lost
	lost := obs.getMappingsLost()
	assert.GreaterOrEqual(t, len(lost), 1)
}

func TestFSM_Stop_FromDiscovering(t *testing.T) {
	mock := client.NewMockClient()

	// Make probe slow so we can test stop during discovery
	mock.ProbeFunc = func(ctx context.Context) (net.IP, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return net.ParseIP("192.168.1.1"), nil
		}
	}

	pm := New(Config{
		Protocol:  ProtocolUDP,
		LocalPort: 51820,
		Client:    mock,
	})

	err := pm.Start()
	require.NoError(t, err)

	// Wait for discovering state
	waitForState(t, pm, StateDiscovering, 1*time.Second)

	err = pm.Stop()
	require.NoError(t, err)

	assert.Equal(t, StateStopped, pm.State())
}

func TestFSM_ObserverNotifications(t *testing.T) {
	mock := client.NewMockClient()
	obs := &testObserver{}

	pm := New(Config{
		Protocol:  ProtocolUDP,
		LocalPort: 51820,
		Client:    mock,
		Observers: []Observer{obs},
	})

	err := pm.Start()
	require.NoError(t, err)

	// Wait for active state
	waitForState(t, pm, StateActive, 2*time.Second)

	changes := obs.getStateChanges()

	// Should have at least these transitions:
	// Idle -> Discovering
	// Discovering -> Requesting
	// Requesting -> Active
	assert.GreaterOrEqual(t, len(changes), 3)

	// Verify first transition
	assert.Equal(t, StateIdle, changes[0].oldState)
	assert.Equal(t, StateDiscovering, changes[0].newState)

	// Verify mapping acquired was called
	mappings := obs.getMappingsAcquired()
	assert.Len(t, mappings, 1)

	_ = pm.Stop()

	// Verify mapping lost was called
	lost := obs.getMappingsLost()
	assert.GreaterOrEqual(t, len(lost), 1)
}

func TestFSM_MultipleObservers(t *testing.T) {
	mock := client.NewMockClient()
	obs1 := &testObserver{}
	obs2 := &testObserver{}

	pm := New(Config{
		Protocol:  ProtocolUDP,
		LocalPort: 51820,
		Client:    mock,
		Observers: []Observer{obs1, obs2},
	})

	err := pm.Start()
	require.NoError(t, err)

	// Wait for active state
	waitForState(t, pm, StateActive, 2*time.Second)

	_ = pm.Stop()

	// Both observers should have received notifications
	assert.GreaterOrEqual(t, len(obs1.getStateChanges()), 1)
	assert.GreaterOrEqual(t, len(obs2.getStateChanges()), 1)
}

func TestFSM_ConcurrentAccess(t *testing.T) {
	mock := client.NewMockClient()

	pm := New(Config{
		Protocol:  ProtocolUDP,
		LocalPort: 51820,
		Client:    mock,
	})

	err := pm.Start()
	require.NoError(t, err)

	// Wait for active state
	waitForState(t, pm, StateActive, 2*time.Second)

	// Concurrent reads and network changes
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(3)

		// Concurrent State() calls
		go func() {
			defer wg.Done()
			_ = pm.State()
		}()

		// Concurrent Mapping() calls
		go func() {
			defer wg.Done()
			_ = pm.Mapping()
		}()

		// Concurrent IsActive() calls
		go func() {
			defer wg.Done()
			_ = pm.IsActive()
		}()
	}

	wg.Wait()
	_ = pm.Stop()
}

func TestFSM_DoubleStart(t *testing.T) {
	mock := client.NewMockClient()

	pm := New(Config{
		Protocol:  ProtocolUDP,
		LocalPort: 51820,
		Client:    mock,
	})
	defer func() { _ = pm.Stop() }()

	err := pm.Start()
	require.NoError(t, err)

	// Second start should return error or be no-op
	_ = pm.Start()
	// Either returns error or succeeds (idempotent)
	// The important thing is it doesn't panic or cause issues
}

func TestFSM_DoubleStop(t *testing.T) {
	mock := client.NewMockClient()

	pm := New(Config{
		Protocol:  ProtocolUDP,
		LocalPort: 51820,
		Client:    mock,
	})

	err := pm.Start()
	require.NoError(t, err)

	// Wait for active state
	waitForState(t, pm, StateActive, 2*time.Second)

	err = pm.Stop()
	require.NoError(t, err)

	// Second stop should be safe
	err = pm.Stop()
	require.NoError(t, err)

	assert.Equal(t, StateStopped, pm.State())
}

func TestFSM_MappingContents(t *testing.T) {
	mock := client.NewMockClient()
	mock.RequestMapping_.ExternalPort = 12345
	mock.RequestMapping_.ExternalIP = net.ParseIP("198.51.100.1")
	mock.RequestMapping_.Gateway = net.ParseIP("192.168.1.254")
	mock.RequestMapping_.Lifetime = 1 * time.Hour

	pm := New(Config{
		Protocol:  ProtocolUDP,
		LocalPort: 51820,
		Client:    mock,
	})
	defer func() { _ = pm.Stop() }()

	err := pm.Start()
	require.NoError(t, err)

	// Wait for active state
	waitForState(t, pm, StateActive, 2*time.Second)

	mapping := pm.Mapping()
	require.NotNil(t, mapping)

	assert.Equal(t, ProtocolUDP, mapping.Protocol)
	assert.Equal(t, 51820, mapping.InternalPort)
	assert.Equal(t, 12345, mapping.ExternalPort)
	assert.True(t, mapping.ExternalIP.Equal(net.ParseIP("198.51.100.1")))
	assert.True(t, mapping.Gateway.Equal(net.ParseIP("192.168.1.254")))
	assert.Equal(t, 1*time.Hour, mapping.Lifetime)
	assert.Equal(t, "mock", mapping.ClientType)
	assert.False(t, mapping.CreatedAt.IsZero())
	assert.False(t, mapping.ExpiresAt.IsZero())
}

func TestFSM_TCPProtocol(t *testing.T) {
	mock := client.NewMockClient()
	mock.RequestMapping_.Protocol = client.TCP

	pm := New(Config{
		Protocol:  ProtocolTCP,
		LocalPort: 22,
		Client:    mock,
	})
	defer func() { _ = pm.Stop() }()

	err := pm.Start()
	require.NoError(t, err)

	// Wait for active state
	waitForState(t, pm, StateActive, 2*time.Second)

	mapping := pm.Mapping()
	require.NotNil(t, mapping)
	assert.Equal(t, ProtocolTCP, mapping.Protocol)
	assert.Equal(t, 22, mapping.InternalPort)
}

// waitForState waits for the port mapper to reach the expected state.
func waitForState(t *testing.T, pm *PortMapper, expected State, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pm.State() == expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for state %s, current state: %s", expected, pm.State())
}
