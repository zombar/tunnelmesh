package coord

import (
	"context"
	"fmt"
	"time"
)

// waitFor polls condition() every interval until it returns true or ctx is done.
// This is a helper for tests to wait for async operations to complete.
//
// Example usage:
//
//	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
//	defer cancel()
//	err := waitFor(ctx, 10*time.Millisecond, func() bool {
//	    return connection != nil
//	})
//	require.NoError(t, err, "connection not established")
func waitFor(ctx context.Context, interval time.Duration, condition func() bool) error {
	// Check immediately first
	if condition() {
		return nil
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waitFor: %w", ctx.Err())
		case <-ticker.C:
			if condition() {
				return nil
			}
		}
	}
}
