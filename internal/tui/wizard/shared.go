package wizard

import (
	"sort"
	"strings"
	"time"

	"github.com/sahilm/fuzzy"
)

// Shared types + helpers used by more than one wizard sub-package
// (start, edit, config). Anything strictly internal to a single
// wizard lives in that sub-package; this file is the cross-cutting
// bag.

// MaxRepoMatches caps how many rows the Repos pages of `start` and
// `edit` render under the cursor at any given time.
const MaxRepoMatches = 8

// CloneState tracks one in-flight clone's lifecycle so the Plan /
// Submit pages can show "cloning…" / "✓" / "✗" lines as the work
// progresses. Both `start` (initial workspace creation) and `edit`
// (attaching new repos to an existing workspace) reuse this.
type CloneState struct {
	Name      string
	CloneURL  string
	TargetDir string
	Started   time.Time
	Done      bool
	Err       error
}

// RankFuzzy runs fuzzy.Find but re-orders the matches so that a
// query appearing as a contiguous substring beats a scattered match.
// Plain fuzzy.Find prefers earlier-starting matches; without this
// re-rank, a scattered "s-e-(n)-t-(r-a-)-u-(s-e-r-)p" match at index
// 0 outranks a clean "setup" substring deeper into the string. The
// users-expect-substring-first behavior is what every Repos page
// wants.
func RankFuzzy(query string, names []string) fuzzy.Matches {
	matches := fuzzy.Find(query, names)
	qLower := strings.ToLower(query)
	sort.SliceStable(matches, func(i, j int) bool {
		ai := strings.Index(strings.ToLower(matches[i].Str), qLower)
		aj := strings.Index(strings.ToLower(matches[j].Str), qLower)
		// Substring matches beat scattered ones.
		if (ai == -1) != (aj == -1) {
			return ai != -1
		}
		// Both substring: earlier index wins.
		if ai != -1 && aj != -1 && ai != aj {
			return ai < aj
		}
		// Otherwise fall back to fuzzy's own descending score.
		return matches[i].Score > matches[j].Score
	})
	return matches
}

// RemoveFromSlice returns a copy of `s` with every occurrence of `v`
// dropped. Order of remaining elements is preserved. Used to maintain
// the picked-repos list as the user toggles selections.
func RemoveFromSlice(s []string, v string) []string {
	out := s[:0]
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
