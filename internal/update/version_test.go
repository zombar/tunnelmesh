package update

import (
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantMajor  int
		wantMinor  int
		wantPatch  int
		wantPre    string
		wantErr    bool
		wantDevBld bool
	}{
		{
			name:      "simple version with v prefix",
			input:     "v1.2.3",
			wantMajor: 1,
			wantMinor: 2,
			wantPatch: 3,
		},
		{
			name:      "simple version without v prefix",
			input:     "1.2.3",
			wantMajor: 1,
			wantMinor: 2,
			wantPatch: 3,
		},
		{
			name:      "version with prerelease",
			input:     "v1.0.0-beta.1",
			wantMajor: 1,
			wantMinor: 0,
			wantPatch: 0,
			wantPre:   "beta.1",
		},
		{
			name:      "version with alpha prerelease",
			input:     "v2.1.0-alpha",
			wantMajor: 2,
			wantMinor: 1,
			wantPatch: 0,
			wantPre:   "alpha",
		},
		{
			name:       "dev build",
			input:      "dev",
			wantDevBld: true,
		},
		{
			name:       "unknown build",
			input:      "unknown",
			wantDevBld: true,
		},
		{
			name:       "dev with commit hash",
			input:      "dev-abc1234",
			wantDevBld: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:  "commit hash is valid",
			input: "77bf625",
			// Not semver, not dev - it's a commit hash release
		},
		{
			name:  "partial version is valid",
			input: "v1.2",
			// Non-semver but valid
		},
		{
			name:      "large version numbers",
			input:     "v100.200.300",
			wantMajor: 100,
			wantMinor: 200,
			wantPatch: 300,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := ParseVersion(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseVersion(%q) expected error, got nil", tt.input)
				}
				return
			}

			if err != nil {
				t.Errorf("ParseVersion(%q) unexpected error: %v", tt.input, err)
				return
			}

			if tt.wantDevBld {
				if !v.IsDevBuild() {
					t.Errorf("ParseVersion(%q).IsDevBuild() = false, want true", tt.input)
				}
				return
			}

			if v.Major != tt.wantMajor {
				t.Errorf("ParseVersion(%q).Major = %d, want %d", tt.input, v.Major, tt.wantMajor)
			}
			if v.Minor != tt.wantMinor {
				t.Errorf("ParseVersion(%q).Minor = %d, want %d", tt.input, v.Minor, tt.wantMinor)
			}
			if v.Patch != tt.wantPatch {
				t.Errorf("ParseVersion(%q).Patch = %d, want %d", tt.input, v.Patch, tt.wantPatch)
			}
			if v.Prerelease != tt.wantPre {
				t.Errorf("ParseVersion(%q).Prerelease = %q, want %q", tt.input, v.Prerelease, tt.wantPre)
			}
		})
	}
}

func TestVersionCompare(t *testing.T) {
	tests := []struct {
		name string
		v1   string
		v2   string
		want int // -1 (v1 < v2), 0 (equal), 1 (v1 > v2)
	}{
		// Basic comparisons
		{"equal versions", "v1.0.0", "v1.0.0", 0},
		{"major difference", "v1.0.0", "v2.0.0", -1},
		{"minor difference", "v1.1.0", "v1.2.0", -1},
		{"patch difference", "v1.0.1", "v1.0.2", -1},

		// Reverse comparisons
		{"major greater", "v2.0.0", "v1.0.0", 1},
		{"minor greater", "v1.2.0", "v1.1.0", 1},
		{"patch greater", "v1.0.2", "v1.0.1", 1},

		// Prerelease comparisons
		{"release > prerelease", "v1.0.0", "v1.0.0-beta", 1},
		{"prerelease < release", "v1.0.0-beta", "v1.0.0", -1},
		{"alpha < beta", "v1.0.0-alpha", "v1.0.0-beta", -1},
		{"beta.1 < beta.2", "v1.0.0-beta.1", "v1.0.0-beta.2", -1},

		// Dev builds (dirty)
		{"dirty < release", "abc123-dirty", "v1.0.0", -1},
		{"release > dirty", "v1.0.0", "abc123-dirty", 1},
		{"dev < release", "dev", "v1.0.0", -1},
		{"release > dev", "v1.0.0", "dev", 1},
		{"dev == dev", "dev", "dev", 0},
		{"unknown < release", "unknown", "v0.0.1", -1},

		// Commit hash versions (non-semver production)
		{"same commit hash", "77bf625", "77bf625", 0},
		{"different commit hashes", "77bf625", "abc1234", -1}, // can't compare, assume need update
		{"commit hash < semver", "77bf625", "v1.0.0", -1},     // semver is "newer"
		{"semver > commit hash", "v1.0.0", "77bf625", 1},

		// Without v prefix
		{"no prefix equal", "1.0.0", "v1.0.0", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v1, err := ParseVersion(tt.v1)
			if err != nil {
				t.Fatalf("ParseVersion(%q) error: %v", tt.v1, err)
			}

			v2, err := ParseVersion(tt.v2)
			if err != nil {
				t.Fatalf("ParseVersion(%q) error: %v", tt.v2, err)
			}

			got := v1.Compare(v2)
			if got != tt.want {
				t.Errorf("(%q).Compare(%q) = %d, want %d", tt.v1, tt.v2, got, tt.want)
			}
		})
	}
}

func TestVersionNeedsUpdate(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{"older needs update", "v1.0.0", "v1.1.0", true},
		{"same no update", "v1.0.0", "v1.0.0", false},
		{"newer no update", "v1.1.0", "v1.0.0", false},
		{"dev needs update", "dev", "v0.0.1", true},
		{"prerelease needs update to release", "v1.0.0-beta", "v1.0.0", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current, _ := ParseVersion(tt.current)
			latest, _ := ParseVersion(tt.latest)

			got := current.NeedsUpdate(latest)
			if got != tt.want {
				t.Errorf("(%q).NeedsUpdate(%q) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestVersionString(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"v1.2.3", "v1.2.3"},
		{"1.2.3", "v1.2.3"},
		{"v1.0.0-beta.1", "v1.0.0-beta.1"},
		{"dev", "dev"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			v, err := ParseVersion(tt.input)
			if err != nil {
				t.Fatalf("ParseVersion(%q) error: %v", tt.input, err)
			}

			got := v.String()
			if got != tt.want {
				t.Errorf("ParseVersion(%q).String() = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsDevBuild(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"dev", true},
		{"unknown", true},
		{"dev-abc1234", true},
		{"v1.0.0", false},
		{"v0.0.1", false},
		{"v1.0.0-beta", false},
		{"77bf625", false},                    // commit hash is production, not dev
		{"abc1234-dirty", true},               // dirty suffix = dev
		{"77bf625-3-gc2cfefa-dirty", true},    // dirty suffix = dev
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			v, err := ParseVersion(tt.input)
			if err != nil {
				t.Fatalf("ParseVersion(%q) error: %v", tt.input, err)
			}

			got := v.IsDevBuild()
			if got != tt.want {
				t.Errorf("ParseVersion(%q).IsDevBuild() = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
