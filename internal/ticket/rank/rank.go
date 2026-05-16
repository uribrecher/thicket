// Package rank scores and orders tickets for the `thicket start`
// picker. The score formula and state-bucket / iteration-decay rules
// live in docs/superpowers/specs/2026-05-16-ticket-ranking-design.md.
package rank

import (
	"sort"
	"strconv"
	"strings"

	"github.com/uribrecher/thicket/internal/ticket"
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

// Score returns the composite ranking score for one ticket.
//
//	score = 1000 * stateTier  +  30 * iterationFactor10  +  100 * workspace
//
// where iterationFactor10 is iterationFactor*10 (kept integer to
// avoid floating-point quantization at the 0.1 step boundary), and
// workspace is 0 or 1.
//
// State dominance is the load-bearing invariant: maxNonLive is
//
//	1000 + 30*10 + 100 = 1400
//
// while minLive is 2000, so every live-tier ticket outranks every
// neutral-tier ticket regardless of the other signals.
func Score(t ticket.Ticket, hasWorkspace bool) int {
	state := stateRank(t.State)
	iter := iterationFactor10(t.IterationDistance)
	ws := 0
	if hasWorkspace {
		ws = 1
	}
	return 1000*state + 30*iter + 100*ws
}

// Sort orders `tickets` in-place by Score desc, UpdatedAt desc. The
// sort is stable, so identical-score tickets preserve input order.
// hasWorkspace may be nil — treated as "no ticket has a workspace".
func Sort(tickets []ticket.Ticket, hasWorkspace func(sourceID string) bool) {
	hasWS := func(string) bool { return false }
	if hasWorkspace != nil {
		hasWS = hasWorkspace
	}
	sort.SliceStable(tickets, func(i, j int) bool {
		si := Score(tickets[i], hasWS(tickets[i].SourceID))
		sj := Score(tickets[j], hasWS(tickets[j].SourceID))
		if si != sj {
			return si > sj
		}
		return tickets[i].UpdatedAt.After(tickets[j].UpdatedAt)
	})
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
