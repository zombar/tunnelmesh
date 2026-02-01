//go:build !linux && !darwin && !windows

package netmon

import (
	"context"
	"net"
	"path/filepath"
	"time"
)

type pollingMonitor struct {
	cfg     Config
	events  chan Event
	lastIPs map[string]bool
}

func newPlatformMonitor(cfg Config) (Monitor, error) {
	return &pollingMonitor{
		cfg:     cfg,
		events:  make(chan Event, 16),
		lastIPs: make(map[string]bool),
	}, nil
}

func (m *pollingMonitor) Start(ctx context.Context) (<-chan Event, error) {
	go m.pollLoop(ctx)
	return m.events, nil // No debouncing needed for polling
}

func (m *pollingMonitor) pollLoop(ctx context.Context) {
	defer close(m.events)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Initialize with current IPs
	m.lastIPs = m.getCurrentIPs()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			currentIPs := m.getCurrentIPs()
			if m.hasChanged(currentIPs) {
				m.lastIPs = currentIPs
				event := Event{
					Type:      ChangeAddressAdded,
					Timestamp: time.Now(),
				}
				if !m.shouldIgnore(&event) {
					select {
					case m.events <- event:
					default:
					}
				}
			}
		}
	}
}

func (m *pollingMonitor) getCurrentIPs() map[string]bool {
	result := make(map[string]bool)

	ifaces, err := net.Interfaces()
	if err != nil {
		return result
	}

	for _, iface := range ifaces {
		// Skip ignored interfaces
		ignore := false
		for _, pattern := range m.cfg.IgnoreInterfaces {
			matched, _ := filepath.Match(pattern, iface.Name)
			if matched {
				ignore = true
				break
			}
		}
		if ignore {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			result[addr.String()] = true
		}
	}

	return result
}

func (m *pollingMonitor) hasChanged(current map[string]bool) bool {
	if len(current) != len(m.lastIPs) {
		return true
	}

	for ip := range current {
		if !m.lastIPs[ip] {
			return true
		}
	}

	return false
}

func (m *pollingMonitor) shouldIgnore(event *Event) bool {
	// Polling already filters interfaces
	return false
}

func (m *pollingMonitor) Close() error {
	return nil
}
