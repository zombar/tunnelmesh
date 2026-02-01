//go:build windows

package netmon

import (
	"context"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	iphlpapi                    = syscall.NewLazyDLL("iphlpapi.dll")
	procNotifyIpInterfaceChange = iphlpapi.NewProc("NotifyIpInterfaceChange")
	procCancelMibChangeNotify2  = iphlpapi.NewProc("CancelMibChangeNotify2")
)

const (
	afUnspec = 0 // AF_UNSPEC - monitor both IPv4 and IPv6
)

type windowsMonitor struct {
	cfg          Config
	events       chan Event
	notifyHandle uintptr
	mu           sync.Mutex
	closed       bool
}

func newPlatformMonitor(cfg Config) (Monitor, error) {
	return &windowsMonitor{
		cfg:    cfg,
		events: make(chan Event, 16),
	}, nil
}

func (m *windowsMonitor) Start(ctx context.Context) (<-chan Event, error) {
	// Create callback that will be invoked on network changes
	callback := syscall.NewCallback(m.notifyCallback)

	// Register for interface change notifications
	// Parameters: Family, Callback, CallerContext, InitialNotification, NotificationHandle
	r1, _, err := procNotifyIpInterfaceChange.Call(
		uintptr(afUnspec),                    // AF_UNSPEC = 0 - monitor all address families
		callback,                             // Callback function
		uintptr(unsafe.Pointer(m)),           // Context pointer
		uintptr(0),                           // InitialNotification = FALSE
		uintptr(unsafe.Pointer(&m.notifyHandle)),
	)
	if r1 != 0 {
		return nil, err
	}

	// Handle context cancellation
	go func() {
		<-ctx.Done()
		m.Close()
	}()

	debouncer := NewDebouncer(m.events, m.cfg.DebounceInterval)
	return debouncer.Run(ctx), nil
}

func (m *windowsMonitor) notifyCallback(callerContext, row, notificationType uintptr) uintptr {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return 0
	}

	event := Event{
		Type:      ChangeAddressAdded, // Simplified - Windows API provides more detail
		Timestamp: time.Now(),
	}

	select {
	case m.events <- event:
	default: // Drop if channel full
	}

	return 0
}

func (m *windowsMonitor) shouldIgnore(event *Event) bool {
	if event.Interface == "" {
		return false
	}

	for _, pattern := range m.cfg.IgnoreInterfaces {
		matched, _ := filepath.Match(pattern, event.Interface)
		if matched {
			return true
		}
	}

	return false
}

func (m *windowsMonitor) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil
	}
	m.closed = true

	if m.notifyHandle != 0 {
		procCancelMibChangeNotify2.Call(m.notifyHandle)
		m.notifyHandle = 0
	}

	close(m.events)
	return nil
}
