// Package update provides self-update functionality for the tunnelmesh CLI.
package update

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version represents a parsed semantic version.
type Version struct {
	Major      int
	Minor      int
	Patch      int
	Prerelease string // e.g., "alpha", "beta.1"
	Raw        string // original string
}

// semverRegex matches semantic versions with optional v prefix and prerelease.
// Examples: v1.2.3, 1.2.3, v1.2.3-beta.1, 1.0.0-alpha
var semverRegex = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([a-zA-Z0-9.-]+))?$`)

// ParseVersion parses a version string into a Version struct.
// Handles formats: "v1.2.3", "1.2.3", "v1.2.3-beta.1", "dev", "unknown", commit hashes
func ParseVersion(s string) (*Version, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty version string")
	}

	v := &Version{Raw: s}

	// Handle special cases for explicit dev builds
	if s == "dev" || s == "unknown" || strings.HasPrefix(s, "dev-") {
		return v, nil
	}

	matches := semverRegex.FindStringSubmatch(s)
	if matches == nil {
		// Not a semver - could be a commit hash release (valid production version)
		// Store as-is, comparison will use raw string matching
		return v, nil
	}

	var err error
	v.Major, err = strconv.Atoi(matches[1])
	if err != nil {
		return nil, fmt.Errorf("invalid major version: %w", err)
	}

	v.Minor, err = strconv.Atoi(matches[2])
	if err != nil {
		return nil, fmt.Errorf("invalid minor version: %w", err)
	}

	v.Patch, err = strconv.Atoi(matches[3])
	if err != nil {
		return nil, fmt.Errorf("invalid patch version: %w", err)
	}

	if len(matches) > 4 {
		v.Prerelease = matches[4]
	}

	return v, nil
}

// IsDevBuild returns true if this is an explicit development build.
// Commit hash releases are NOT considered dev builds - they are valid production versions.
func (v *Version) IsDevBuild() bool {
	// Only explicit dev markers are dev builds
	if v.Raw == "dev" || v.Raw == "unknown" || strings.HasPrefix(v.Raw, "dev-") {
		return true
	}
	// Dirty suffix indicates a local uncommitted build
	if strings.Contains(v.Raw, "-dirty") {
		return true
	}
	return false
}

// IsSemver returns true if this version follows semantic versioning.
func (v *Version) IsSemver() bool {
	return v.Major > 0 || v.Minor > 0 || v.Patch > 0 || v.Prerelease != ""
}

// Compare compares two versions.
// Returns:
//
//	-1 if v < other
//	 0 if v == other
//	 1 if v > other
//
// Dev builds (dirty) are always considered older than any release version.
// For non-semver versions (commit hashes), only exact matches are equal.
func (v *Version) Compare(other *Version) int {
	// Dev builds (dirty) are always older than non-dev
	if v.IsDevBuild() && !other.IsDevBuild() {
		return -1
	}
	if !v.IsDevBuild() && other.IsDevBuild() {
		return 1
	}
	if v.IsDevBuild() && other.IsDevBuild() {
		// Both dev - compare raw strings
		if v.Raw == other.Raw {
			return 0
		}
		return -1 // Dev builds can't be compared, assume need update
	}

	// If both are non-semver (commit hashes), compare raw strings
	if !v.IsSemver() && !other.IsSemver() {
		if v.Raw == other.Raw {
			return 0
		}
		// Can't determine order between different commit hashes
		// Return -1 to indicate "might need update"
		return -1
	}

	// If one is semver and one isn't, semver is "newer"
	// (this encourages updating from commit hash releases to semver releases)
	if v.IsSemver() && !other.IsSemver() {
		return 1
	}
	if !v.IsSemver() && other.IsSemver() {
		return -1
	}

	// Both are semver - compare major.minor.patch
	if v.Major != other.Major {
		if v.Major < other.Major {
			return -1
		}
		return 1
	}

	if v.Minor != other.Minor {
		if v.Minor < other.Minor {
			return -1
		}
		return 1
	}

	if v.Patch != other.Patch {
		if v.Patch < other.Patch {
			return -1
		}
		return 1
	}

	// Compare prerelease
	// No prerelease > has prerelease (1.0.0 > 1.0.0-beta)
	if v.Prerelease == "" && other.Prerelease != "" {
		return 1
	}
	if v.Prerelease != "" && other.Prerelease == "" {
		return -1
	}

	// Both have prerelease - compare lexicographically
	if v.Prerelease < other.Prerelease {
		return -1
	}
	if v.Prerelease > other.Prerelease {
		return 1
	}

	return 0
}

// String returns the canonical version string (with v prefix).
func (v *Version) String() string {
	if v.IsDevBuild() {
		return v.Raw
	}

	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Prerelease != "" {
		s += "-" + v.Prerelease
	}
	return s
}

// NeedsUpdate returns true if other is newer than v.
func (v *Version) NeedsUpdate(other *Version) bool {
	return v.Compare(other) < 0
}
