package updater

import (
	"fmt"
	"strconv"
	"strings"
)

// Release is the minimal subset of a semver tag that thicket cares
// about for self-update purposes. Pre-release / build-metadata
// suffixes (e.g. `-rc1`, `-13-gabcd`, `-dirty`) are explicitly
// rejected — they represent non-release builds that should not be
// candidates for an auto-update prompt in either direction.
type Release struct {
	Major, Minor, Patch int
}

// ParseRelease accepts a leading "v" optionally and requires exactly
// three numeric segments with no suffix. Returns an error for
// anything else; callers should treat the error as "skip the update
// check entirely" (the build is a dev/dirty/unparseable version).
func ParseRelease(s string) (Release, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if s == "" {
		return Release{}, fmt.Errorf("empty version")
	}
	if strings.ContainsAny(s, "-+ ") {
		return Release{}, fmt.Errorf("non-release version %q (dev/dirty build)", s)
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Release{}, fmt.Errorf("not vX.Y.Z: %q", s)
	}
	var r Release
	var err error
	if r.Major, err = parseSeg(parts[0]); err != nil {
		return Release{}, err
	}
	if r.Minor, err = parseSeg(parts[1]); err != nil {
		return Release{}, err
	}
	if r.Patch, err = parseSeg(parts[2]); err != nil {
		return Release{}, err
	}
	return r, nil
}

func parseSeg(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("bad version segment %q", s)
	}
	return n, nil
}

// Compare returns -1, 0, +1 in the usual sense: a < b → -1, a > b → 1.
func (a Release) Compare(b Release) int {
	switch {
	case a.Major != b.Major:
		return cmpInt(a.Major, b.Major)
	case a.Minor != b.Minor:
		return cmpInt(a.Minor, b.Minor)
	default:
		return cmpInt(a.Patch, b.Patch)
	}
}

func (r Release) String() string {
	return fmt.Sprintf("v%d.%d.%d", r.Major, r.Minor, r.Patch)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
