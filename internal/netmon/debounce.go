package netmon

import (
	"context"
	"time"
)

// Debouncer coalesces rapid network change events.
type Debouncer struct {
	interval time.Duration
	input    <-chan Event
	output   chan Event
}

// NewDebouncer creates a debouncer that coalesces events within the given interval.
func NewDebouncer(input <-chan Event, interval time.Duration) *Debouncer {
	return &Debouncer{
		interval: interval,
		input:    input,
		output:   make(chan Event),
	}
}

// Run starts the debouncer and returns the output channel.
// Events that occur within the debounce interval are coalesced into a single event.
func (d *Debouncer) Run(ctx context.Context) <-chan Event {
	go d.loop(ctx)
	return d.output
}

func (d *Debouncer) loop(ctx context.Context) {
	defer close(d.output)

	var timer *time.Timer
	var timerChan <-chan time.Time
	var pending *Event

	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return

		case event, ok := <-d.input:
			if !ok {
				// Input channel closed, flush any pending event
				if pending != nil {
					select {
					case d.output <- *pending:
					case <-ctx.Done():
					}
				}
				return
			}

			// Store the event (overwriting any pending event)
			pending = &event

			// Reset or create the timer
			if timer == nil {
				timer = time.NewTimer(d.interval)
				timerChan = timer.C
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(d.interval)
			}

		case <-timerChan:
			// Timer fired, emit the pending event
			if pending != nil {
				select {
				case d.output <- *pending:
				case <-ctx.Done():
					return
				}
				pending = nil
			}
			timerChan = nil
		}
	}
}
