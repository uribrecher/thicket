package rank_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/uribrecher/thicket/internal/ticket"
	"github.com/uribrecher/thicket/internal/ticket/rank"
)

func TestScore_stateBuckets(t *testing.T) {
	// IterationDistance = -1 (no iter), no workspace → factor 0.
	// Score is then 1000 * stateTier, so we can read stateTier off
	// the score directly.
	cases := map[string]int{
		// Tier 2 (live)
		"In Development":        2,
		"in development":        2, // case-insensitive
		"  Ready for Dev  ":     1, // unknown name → neutral (NOT live)
		"Ready for Development": 2,
		"Waiting for R&D":       2,

		// Tier 1 (neutral) — includes the names we moved here per spec
		"Backlog":           1,
		"In Review":         1,
		"In Code Review":    1,
		"Some Custom State": 1,
		"":                  1,

		// Tier 0 (stalled)
		"Paused":         0,
		"Waiting for CS": 0,
	}
	for state, wantTier := range cases {
		got := rank.Score(ticket.Ticket{State: state, IterationDistance: -1}, false)
		want := 1000 * wantTier
		if got != want {
			t.Errorf("State=%q: Score=%d, want %d (tier %d)", state, got, want, wantTier)
		}
	}
}

func TestScore_iterationDecay(t *testing.T) {
	// Live state (2000) + workspace=false, vary IterationDistance.
	// iterationFactor10 → integer in [0,10]; contribution = 30*factor10.
	cases := []struct {
		distance int
		want     int // expected score given state=live, ws=false
	}{
		{0, 2000 + 300},  // current → factor 1.0
		{1, 2000 + 270},  // previous → factor 0.9
		{2, 2000 + 240},
		{5, 2000 + 150},
		{9, 2000 + 30},
		{10, 2000 + 0}, // floor
		{15, 2000 + 0}, // way past floor
		{-1, 2000 + 0}, // sentinel — no iteration
	}
	for _, c := range cases {
		got := rank.Score(
			ticket.Ticket{State: "In Development", IterationDistance: c.distance},
			false,
		)
		if got != c.want {
			t.Errorf("distance=%d: Score=%d, want %d", c.distance, got, c.want)
		}
	}
}

func TestScore_workspaceBoost(t *testing.T) {
	tk := ticket.Ticket{State: "In Development", IterationDistance: -1}
	without := rank.Score(tk, false)
	with := rank.Score(tk, true)
	if with-without != 100 {
		t.Errorf("workspace boost = %d, want 100", with-without)
	}
}

// State dominance is the spec's load-bearing invariant: every live
// ticket beats every neutral ticket, every neutral beats every
// stalled, regardless of iteration/workspace combinations.
func TestScore_stateDominanceInvariant(t *testing.T) {
	maxNonLive := rank.Score(
		ticket.Ticket{State: "In Review", IterationDistance: 0}, // neutral + current
		true, // + workspace
	)
	minLive := rank.Score(
		ticket.Ticket{State: "In Development", IterationDistance: -1}, // live, nothing else
		false,
	)
	if minLive <= maxNonLive {
		t.Errorf("state dominance broken: minLive=%d, maxNonLive=%d", minLive, maxNonLive)
	}

	maxStalled := rank.Score(
		ticket.Ticket{State: "Paused", IterationDistance: 0},
		true,
	)
	minNeutral := rank.Score(
		ticket.Ticket{State: "In Review", IterationDistance: -1},
		false,
	)
	if minNeutral <= maxStalled {
		t.Errorf("state dominance broken: minNeutral=%d, maxStalled=%d", minNeutral, maxStalled)
	}
}

func TestSort_scoreThenUpdatedAtDesc(t *testing.T) {
	t0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	tickets := []ticket.Ticket{
		// stalled, no iter, no ws → score 0
		{SourceID: "stalled", State: "Paused", IterationDistance: -1, UpdatedAt: t0.Add(100 * time.Hour)},
		// live, current iter → score 2300
		{SourceID: "live-cur", State: "In Development", IterationDistance: 0, UpdatedAt: t0},
		// live, no iter, ws → score 2100
		{SourceID: "live-ws", State: "In Development", IterationDistance: -1, UpdatedAt: t0.Add(50 * time.Hour)},
		// live, 5-back iter, ws → score 2250
		{SourceID: "live-5back-ws", State: "In Development", IterationDistance: 5, UpdatedAt: t0},
		// two live tickets with identical score — newest by UpdatedAt should win
		{SourceID: "live-a", State: "In Development", IterationDistance: -1, UpdatedAt: t0.Add(10 * time.Hour)},
		{SourceID: "live-b", State: "In Development", IterationDistance: -1, UpdatedAt: t0.Add(20 * time.Hour)},
	}
	hasWS := func(id string) bool { return id == "live-ws" || id == "live-5back-ws" }

	rank.Sort(tickets, hasWS)

	gotIDs := make([]string, len(tickets))
	for i, tk := range tickets {
		gotIDs[i] = tk.SourceID
	}
	wantIDs := []string{
		"live-cur",      // 2300
		"live-5back-ws", // 2250
		"live-ws",       // 2100
		"live-b",        // 2000 + newer UpdatedAt
		"live-a",        // 2000 + older UpdatedAt
		"stalled",       // 0
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("Sort order:\n got  %v\n want %v", gotIDs, wantIDs)
	}
}

func TestSort_nilHasWorkspacePredicate(t *testing.T) {
	// A nil predicate should behave as "no ticket has a workspace".
	t0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	tickets := []ticket.Ticket{
		{SourceID: "a", State: "In Development", IterationDistance: -1, UpdatedAt: t0},
		{SourceID: "b", State: "In Development", IterationDistance: -1, UpdatedAt: t0.Add(time.Hour)},
	}
	rank.Sort(tickets, nil)
	if tickets[0].SourceID != "b" {
		t.Errorf("nil predicate: position 0 = %q, want %q", tickets[0].SourceID, "b")
	}
}
