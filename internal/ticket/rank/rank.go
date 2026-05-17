// Package rank scores and orders tickets for the `thicket start`
// picker. The score formula and state-bucket / iteration-decay rules
// live in docs/superpowers/specs/2026-05-16-ticket-ranking-design.md.
package rank

import (
	"sort"
	"strconv"
	"strings"

	"github.com/uribrecher/thicket/internal/ticket"
	"github.com/uribrecher/thicket/internal/tui"
)

// FormatIterationDistance renders a Ticket.IterationDistance for
// human display in the picker tables. The sentinel -1 ("no iteration
// / unresolved") renders as an em dash; 0+ render as their integer.
func FormatIterationDistance(distance int) string {
	if distance < 0 {
		return "—"
	}
	return strconv.Itoa(distance)
}

// FormatPriority renders a Ticket.Priority label as a compact arrow
// glyph used by the picker's combined State column (see
// FormatPriorityState). Full labels ("Highest", "High", "Medium",
// "Low") chewed up too much width in the table; the four-glyph
// ladder still conveys magnitude and direction. Some glyphs are
// East-Asian-wide (2 cells) and some are narrow (e.g. "▪" or the
// blank fallback for unknown labels) — callers must pad to a fixed
// 2-cell slot for row alignment.
func FormatPriority(name string) string {
	switch priorityRank(name) {
	case 4:
		return "⏫"
	case 3:
		return "🔼"
	case 2:
		return "▪"
	case 1:
		return "🔽"
	default:
		return " "
	}
}

// FormatPriorityState renders the picker's combined State cell: a
// fixed 2-cell slot for the priority glyph, a single-space
// separator, then up to 11 cells of state prefix. Total visible
// width is 14 cells. Used by both the wizard picker and the
// cmd-side tui.PickOne fallback so the two layouts can't drift
// apart and silently misalign rows again.
func FormatPriorityState(priority, state string) string {
	return tui.PadRight(FormatPriority(priority), 2) + " " + tui.Truncate(state, 11)
}

// Score returns the composite ranking score for one ticket.
//
//	score = 1000 * stateTier
//	      +   50 * priorityTier   // 0..4 (Low..Highest), 0 if unknown
//	      +   30 * iterationFactor10
//	      +  100 * workspace
//
// where iterationFactor10 is iterationFactor*10 (kept integer to
// avoid floating-point quantization at the 0.1 step boundary), and
// workspace is 0 or 1.
//
// State dominance is the load-bearing invariant: maxNonLive is
//
//	1000 + 50*4 + 30*10 + 100 = 1600
//
// while minLive is 2000, so every live-tier ticket outranks every
// neutral-tier ticket regardless of the other signals.
func Score(t ticket.Ticket, hasWorkspace bool) int {
	state := stateRank(t.State)
	prio := priorityRank(t.Priority)
	iter := iterationFactor10(t.IterationDistance)
	ws := 0
	if hasWorkspace {
		ws = 1
	}
	return 1000*state + 50*prio + 30*iter + 100*ws
}

// Sort orders `tickets` in-place by Score desc, UpdatedAt desc. The
// sort is stable, so identical-score tickets preserve input order.
// hasWorkspace may be nil — treated as "no ticket has a workspace".
//
// Scores and workspace-presence are computed once per ticket up
// front, not per comparator call. This matters because callers may
// pass predicates that allocate (e.g. the wizard's
// FindExistingWorkspace returns a freshly-copied workspace value);
// the comparator runs O(n log n) times, so repeated invocation would
// be a hot loop of unnecessary allocs.
func Sort(tickets []ticket.Ticket, hasWorkspace func(sourceID string) bool) {
	hasWS := func(string) bool { return false }
	if hasWorkspace != nil {
		hasWS = hasWorkspace
	}
	scores := make([]int, len(tickets))
	for i, t := range tickets {
		scores[i] = Score(t, hasWS(t.SourceID))
	}
	sort.Stable(&rankedTickets{tickets: tickets, scores: scores})
}

// rankedTickets implements sort.Interface over two parallel slices so
// the comparator never has to recompute Score.
type rankedTickets struct {
	tickets []ticket.Ticket
	scores  []int
}

func (r *rankedTickets) Len() int { return len(r.tickets) }
func (r *rankedTickets) Less(i, j int) bool {
	if r.scores[i] != r.scores[j] {
		return r.scores[i] > r.scores[j]
	}
	return r.tickets[i].UpdatedAt.After(r.tickets[j].UpdatedAt)
}
func (r *rankedTickets) Swap(i, j int) {
	r.tickets[i], r.tickets[j] = r.tickets[j], r.tickets[i]
	r.scores[i], r.scores[j] = r.scores[j], r.scores[i]
}

// iterationFactor10 returns the iteration boost as an integer in
// [0,10] — i.e. `iterationFactor * 10`. distance < 0 is the sentinel
// for "no iteration / unresolved" and yields 0 (same as ≥10-back).
//
// Integer arithmetic keeps the score deterministic: 0.1 is not
// exactly representable as a float64, so 300*float64(0.1) could land
// on 29 or 30 depending on rounding. Multiplying ints avoids it.
func iterationFactor10(distance int) int {
	if distance < 0 {
		return 0
	}
	f := 10 - distance
	if f < 0 {
		return 0
	}
	return f
}

// priorityRank assigns each priority label a sort tier in [0, 4].
// Tiers map both Shortcut's default custom-field values ("Highest",
// "High", "Medium", "Low") and the "P0".."P3" convention some
// workspaces adopt. Unknown / empty labels return 0, matching the
// behaviour for tickets whose source doesn't expose a priority.
func priorityRank(name string) int {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "highest", "p0":
		return 4
	case "high", "p1":
		return 3
	case "medium", "p2":
		return 2
	case "low", "p3":
		return 1
	default:
		return 0
	}
}

// stateRank assigns each workflow-state name a sort tier:
//
//	2 — live: in development, ready for development, waiting for r&d
//	0 — stalled: waiting for cs, paused
//	1 — neutral fallback: backlog, in review, in code review,
//	    unknown / custom state names
//
// Names are matched case-insensitively after trimming so minor
// formatting variation in Shortcut workspaces (extra spaces, etc.)
// doesn't break the bucket.
func stateRank(name string) int {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "in development", "ready for development", "waiting for r&d":
		return 2
	case "waiting for cs", "paused":
		return 0
	default:
		return 1
	}
}
