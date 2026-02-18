package s3

import (
	"os"
	"testing"
)

func TestEffectiveAvailableBytes(t *testing.T) {
	tests := []struct {
		name     string
		snap     CapacitySnapshot
		expected int64
	}{
		{
			name: "unlimited quota returns volume available",
			snap: CapacitySnapshot{
				VolumeAvailableBytes: 100_000_000,
				QuotaAvailBytes:      -1,
			},
			expected: 100_000_000,
		},
		{
			name: "quota lower than volume",
			snap: CapacitySnapshot{
				VolumeAvailableBytes: 100_000_000,
				QuotaAvailBytes:      50_000_000,
			},
			expected: 50_000_000,
		},
		{
			name: "volume lower than quota",
			snap: CapacitySnapshot{
				VolumeAvailableBytes: 30_000_000,
				QuotaAvailBytes:      50_000_000,
			},
			expected: 30_000_000,
		},
		{
			name: "both zero",
			snap: CapacitySnapshot{
				VolumeAvailableBytes: 0,
				QuotaAvailBytes:      0,
			},
			expected: 0,
		},
		{
			name: "quota zero volume nonzero",
			snap: CapacitySnapshot{
				VolumeAvailableBytes: 100_000_000,
				QuotaAvailBytes:      0,
			},
			expected: 0,
		},
		{
			name: "equal values",
			snap: CapacitySnapshot{
				VolumeAvailableBytes: 50_000_000,
				QuotaAvailBytes:      50_000_000,
			},
			expected: 50_000_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.snap.EffectiveAvailableBytes()
			if got != tt.expected {
				t.Errorf("EffectiveAvailableBytes() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestCapacityRegistryUpdate(t *testing.T) {
	reg := NewCapacityRegistry()

	snap := &CapacitySnapshot{
		CoordinatorName:      "coord-1",
		VolumeAvailableBytes: 100,
	}
	reg.Update(snap)

	got := reg.Get("coord-1")
	if got == nil {
		t.Fatal("expected snapshot, got nil")
	}
	if got.VolumeAvailableBytes != 100 {
		t.Errorf("VolumeAvailableBytes = %d, want 100", got.VolumeAvailableBytes)
	}

	// Update replaces
	snap2 := &CapacitySnapshot{
		CoordinatorName:      "coord-1",
		VolumeAvailableBytes: 200,
	}
	reg.Update(snap2)
	got = reg.Get("coord-1")
	if got.VolumeAvailableBytes != 200 {
		t.Errorf("VolumeAvailableBytes = %d, want 200", got.VolumeAvailableBytes)
	}
}

func TestCapacityRegistryUpdateNil(t *testing.T) {
	reg := NewCapacityRegistry()
	reg.Update(nil) // should not panic
	if len(reg.GetAll()) != 0 {
		t.Errorf("expected empty registry after nil update")
	}
}

func TestCapacityRegistryGetUnknown(t *testing.T) {
	reg := NewCapacityRegistry()
	if got := reg.Get("unknown"); got != nil {
		t.Errorf("expected nil for unknown coordinator, got %v", got)
	}
}

func TestCapacityRegistryGetAll(t *testing.T) {
	reg := NewCapacityRegistry()
	reg.Update(&CapacitySnapshot{CoordinatorName: "a", VolumeAvailableBytes: 10})
	reg.Update(&CapacitySnapshot{CoordinatorName: "b", VolumeAvailableBytes: 20})

	all := reg.GetAll()
	if len(all) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(all))
	}
	if all["a"].VolumeAvailableBytes != 10 {
		t.Errorf("a.VolumeAvailableBytes = %d, want 10", all["a"].VolumeAvailableBytes)
	}
	if all["b"].VolumeAvailableBytes != 20 {
		t.Errorf("b.VolumeAvailableBytes = %d, want 20", all["b"].VolumeAvailableBytes)
	}
}

func TestHasCapacityForFailOpen(t *testing.T) {
	reg := NewCapacityRegistry()

	// Unknown coordinator — fail-open
	if !reg.HasCapacityFor("unknown", 1000) {
		t.Error("expected fail-open for unknown coordinator")
	}
}

func TestHasCapacityForWithSnapshot(t *testing.T) {
	reg := NewCapacityRegistry()
	reg.Update(&CapacitySnapshot{
		CoordinatorName:      "coord-1",
		VolumeAvailableBytes: 1000,
		QuotaAvailBytes:      -1, // unlimited quota
	})

	if !reg.HasCapacityFor("coord-1", 500) {
		t.Error("expected capacity for 500 bytes with 1000 available")
	}
	if !reg.HasCapacityFor("coord-1", 1000) {
		t.Error("expected capacity for exactly 1000 bytes")
	}
	if reg.HasCapacityFor("coord-1", 1001) {
		t.Error("expected no capacity for 1001 bytes with 1000 available")
	}
}

func TestHasCapacityForQuotaConstrained(t *testing.T) {
	reg := NewCapacityRegistry()
	reg.Update(&CapacitySnapshot{
		CoordinatorName:      "coord-1",
		VolumeAvailableBytes: 10000,
		QuotaAvailBytes:      500,
	})

	if !reg.HasCapacityFor("coord-1", 500) {
		t.Error("expected capacity for 500 bytes (quota limit)")
	}
	if reg.HasCapacityFor("coord-1", 501) {
		t.Error("expected no capacity for 501 bytes (quota limit is 500)")
	}
}

func TestSortByAvailableCapacity(t *testing.T) {
	reg := NewCapacityRegistry()
	reg.Update(&CapacitySnapshot{
		CoordinatorName:      "small",
		VolumeAvailableBytes: 100,
		QuotaAvailBytes:      -1,
	})
	reg.Update(&CapacitySnapshot{
		CoordinatorName:      "large",
		VolumeAvailableBytes: 1000,
		QuotaAvailBytes:      -1,
	})
	reg.Update(&CapacitySnapshot{
		CoordinatorName:      "medium",
		VolumeAvailableBytes: 500,
		QuotaAvailBytes:      -1,
	})

	sorted := reg.SortByAvailableCapacity([]string{"small", "large", "medium"})
	if sorted[0] != "large" || sorted[1] != "medium" || sorted[2] != "small" {
		t.Errorf("expected [large, medium, small], got %v", sorted)
	}
}

func TestSortByAvailableCapacityUnknownLast(t *testing.T) {
	reg := NewCapacityRegistry()
	reg.Update(&CapacitySnapshot{
		CoordinatorName:      "known",
		VolumeAvailableBytes: 100,
		QuotaAvailBytes:      -1,
	})

	sorted := reg.SortByAvailableCapacity([]string{"unknown", "known"})
	if sorted[0] != "known" || sorted[1] != "unknown" {
		t.Errorf("expected [known, unknown], got %v", sorted)
	}
}

func TestSortByAvailableCapacityEmpty(t *testing.T) {
	reg := NewCapacityRegistry()
	sorted := reg.SortByAvailableCapacity([]string{})
	if len(sorted) != 0 {
		t.Errorf("expected empty slice, got %v", sorted)
	}
}

func TestGetVolumeStats(t *testing.T) {
	dir, err := os.MkdirTemp("", "capacity-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	total, used, available, err := GetVolumeStats(dir)
	if err != nil {
		t.Fatalf("GetVolumeStats: %v", err)
	}

	if total <= 0 {
		t.Errorf("expected positive total, got %d", total)
	}
	if used < 0 {
		t.Errorf("expected non-negative used, got %d", used)
	}
	if available <= 0 {
		t.Errorf("expected positive available, got %d", available)
	}
	if used+available > total {
		// available is Bavail (non-root), used is total - Bfree
		// so used + available can be <= total (reserved blocks)
		// but it should not wildly exceed total
		if used+available > total*2 {
			t.Errorf("used(%d) + available(%d) greatly exceeds total(%d)", used, available, total)
		}
	}
}

func TestGetVolumeStatsInvalidPath(t *testing.T) {
	_, _, _, err := GetVolumeStats("/nonexistent/path/that/should/not/exist")
	if err == nil {
		t.Error("expected error for nonexistent path")
	}
}
