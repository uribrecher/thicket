# Ticket Ranking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the shortcut-source-local ranking with a cross-source `rank` package that scores tickets by `state * 1000 + iterationFactor * 300 + workspace * 100`, where iteration is graduated by distance from the current sprint and future-iteration tickets are filtered out.

**Architecture:** A new `internal/ticket/rank` package owns ranking. The shortcut source is reduced to fetching, filtering (now including future-iteration filtering), and annotating each ticket with `IterationDistance`. Both callers of `ListAssigned` (legacy non-interactive picker in `cmd/thicket/start.go` and the TUI wizard's ticket page) invoke `rank.Sort` after the source returns.

**Tech Stack:** Go 1.24, stdlib `sort.SliceStable`, existing `net/http`-based Shortcut client. No new dependencies.

**Reference spec:** `docs/superpowers/specs/2026-05-16-ticket-ranking-design.md`

---

## File Structure

| File | Role |
|---|---|
| `internal/ticket/source.go` (modify) | Cross-source `Ticket` struct — add `UpdatedAt` and `IterationDistance` |
| `internal/ticket/rank/rank.go` (new) | `Sort`, `Score`, `stateRank`, `iterationFactor10` — the ranking |
| `internal/ticket/rank/rank_test.go` (new) | Tests covering the score formula, state buckets, iteration decay, workspace boost, state dominance invariant |
| `internal/ticket/shortcut/client.go` (modify) | Add `iteration_id` to `storyResponse`, fetch `/api/v3/iterations`, build timeline, set `IterationDistance` on `Ticket`, filter future-iteration stories. Remove the in-source sort and `stateRank` |
| `internal/ticket/shortcut/client_test.go` (modify) | Extend `listAssignedServer` to handle `/api/v3/iterations`. Replace ranking-shape tests with iteration-distance + future-filter tests. Update `TestListAssigned_filtersDoneArchivedAndExcludedStates` to expect `in review` survives |
| `cmd/thicket/start.go` (modify) | In `pickAssignedTicketLegacy`, call `rank.Sort` after `ListAssigned` |
| `internal/tui/wizard/start/ticket.go` (modify) | In `listTicketsCmd`, call `rank.Sort` after `ListAssigned` |

---

## Task 1: Extend the `Ticket` struct with `UpdatedAt` and `IterationDistance`

**Why:** The new ranker is cross-source and needs both fields surfaced on the generic `Ticket`. Today `UpdatedAt` lives only on `storyResponse` (Shortcut-internal) and `IterationDistance` doesn't exist at all.

**Files:**
- Modify: `internal/ticket/source.go:47-57` (the `Ticket` struct)
- Modify: `internal/ticket/shortcut/client.go:230-249` (`toTicket`)
- Test: `internal/ticket/shortcut/client_test.go` (existing — add one assertion)

- [ ] **Step 1: Write the failing test**

Append a sub-test to `TestListAssigned_filtersDoneArchivedAndExcludedStates` in `internal/ticket/shortcut/client_test.go`, OR add a new small focused test alongside it:

```go
func TestListAssigned_setsUpdatedAtAndIterationDistanceDefault(t *testing.T) {
	member := memberResponse{ID: "u"}
	workflows := []workflowResponse{{
		States: []workflowStateResponse{{ID: 1, Name: "In Development", Type: "started"}},
	}}
	updated := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	stories := []storyResponse{
		{ID: 1, Name: "t", WorkflowStateID: 1, UpdatedAt: updated},
	}
	srv := listAssignedServer(t, member, workflows, stories)
	defer srv.Close()

	got, err := New("tok", srv.URL).ListAssigned(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d tickets, want 1", len(got))
	}
	if !got[0].UpdatedAt.Equal(updated) {
		t.Errorf("UpdatedAt = %v, want %v", got[0].UpdatedAt, updated)
	}
	if got[0].IterationDistance != -1 {
		t.Errorf("IterationDistance = %d, want -1 (sentinel for no iteration)",
			got[0].IterationDistance)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/ticket/shortcut/ -run TestListAssigned_setsUpdatedAtAndIterationDistanceDefault -v
```

Expected: FAIL — `Ticket` has no field `UpdatedAt` or `IterationDistance` (compile error).

- [ ] **Step 3: Add the fields to `Ticket`**

Edit `internal/ticket/source.go` — locate the `Ticket` struct (around lines 47-57) and add two fields. The struct should end up as:

```go
type Ticket struct {
	SourceID  string            // e.g. "sc-12345"
	Title     string            // single-line title
	Body      string            // markdown description
	URL       string            // canonical web URL
	State     string            // workflow state name; "" if not resolved
	Owner     string            // mention name / handle; "" if not resolved
	Requester string            // display name of whoever filed the ticket; "" if not resolved
	Labels    []string          // ticket labels in source order; nil if none
	Extra     map[string]string // source-specific extras

	// UpdatedAt is the last-modified timestamp at the source. Used by
	// the ranker as a tiebreaker (most-recently-touched first).
	// Sources that don't surface this leave it zero.
	UpdatedAt time.Time

	// IterationDistance is the integer step from the source's
	// "current" iteration to this ticket's iteration, in timeline
	// order:
	//   0  → current iteration
	//   1  → previous iteration
	//   N  → N iterations back
	//  -1  → no iteration, or iteration could not be resolved
	//        (sentinel; ranker treats as factor 0). Sources that
	//        don't know about iterations always emit -1.
	IterationDistance int
}
```

You'll also need to add `"time"` to the imports at the top of the file if it's not already there.

- [ ] **Step 4: Populate the new fields in `toTicket`**

Edit `internal/ticket/shortcut/client.go:230-249`. Update `toTicket` to set both fields. New version:

```go
func (s *Source) toTicket(sr storyResponse, stateName string) ticket.Ticket {
	var labels []string
	for _, l := range sr.Labels {
		if l.Name != "" {
			labels = append(labels, l.Name)
		}
	}
	return ticket.Ticket{
		SourceID:          ID(sr.ID).String(),
		Title:             sr.Name,
		Body:              sr.Description,
		URL:               sr.AppURL,
		State:             stateName,
		Labels:            labels,
		UpdatedAt:         sr.UpdatedAt,
		IterationDistance: -1, // overwritten in ListAssigned when iteration data is available
		Extra: map[string]string{
			"formatted_vcs_branch_name": sr.FormattedVCSBranchName,
			"workflow_state_id":         strconv.Itoa(sr.WorkflowStateID),
		},
	}
}
```

- [ ] **Step 5: Run the new test and the full shortcut suite**

```bash
go test ./internal/ticket/... -v
```

Expected: PASS on the new test. The existing tests `TestListAssigned_filtersDoneArchivedAndExcludedStates`, `TestListAssigned_sortsByUpdatedAtDescending`, `TestListAssigned_tieredSortByStateThenUpdatedAt`, `TestListAssigned_emptyStoriesNotAnError` should also still pass — we have not changed any ranking behavior yet.

- [ ] **Step 6: Commit**

```bash
git add internal/ticket/source.go internal/ticket/shortcut/client.go internal/ticket/shortcut/client_test.go
git commit -m "feat(ticket): add UpdatedAt and IterationDistance to Ticket

UpdatedAt surfaces the source's last-modified timestamp on the
cross-source Ticket so the new ranker can use it as a tiebreaker.

IterationDistance carries the integer step from the source's
'current' iteration to this ticket's iteration. -1 is the explicit
sentinel for 'no iteration / unresolved' so the ranker can
distinguish it from distance 0 (current). Sources that don't know
about iterations emit -1.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Create the `rank` package — `stateRank`, `iterationFactor10`, `Score`, `Sort`

**Why:** Owns ranking in a cross-source package, fully testable in isolation. Encodes the new state buckets (`backlog` → neutral, `in code review` → neutral) and the graduated iteration decay (`factor = max(0, 1 - distance/10)`).

**Files:**
- Create: `internal/ticket/rank/rank.go`
- Create: `internal/ticket/rank/rank_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/ticket/rank/rank_test.go` with the following table-driven tests covering state buckets, iteration decay, the score formula, the state-dominance invariant, and `Sort` ordering:

```go
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
		"Backlog":               1,
		"In Review":             1,
		"In Code Review":        1,
		"Some Custom State":     1,
		"":                      1,

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
		{10, 2000 + 0},   // floor
		{15, 2000 + 0},   // way past floor
		{-1, 2000 + 0},   // sentinel — no iteration
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
		true,                                                    // + workspace
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
		"live-cur",       // 2300
		"live-5back-ws",  // 2250
		"live-ws",        // 2100
		"live-b",         // 2000 + newer UpdatedAt
		"live-a",         // 2000 + older UpdatedAt
		"stalled",        // 0
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
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/ticket/rank/ -v
```

Expected: FAIL — package `internal/ticket/rank` does not exist yet (compile error or "no Go files").

- [ ] **Step 3: Create the package**

Create `internal/ticket/rank/rank.go` with:

```go
// Package rank scores and orders tickets for the `thicket start`
// picker. The score formula and state-bucket / iteration-decay rules
// live in docs/superpowers/specs/2026-05-16-ticket-ranking-design.md.
package rank

import (
	"sort"
	"strings"

	"github.com/uribrecher/thicket/internal/ticket"
)

// Score returns the composite ranking score for one ticket.
//
//   score = 1000 * stateTier  +  30 * iterationFactor10  +  100 * workspace
//
// where iterationFactor10 is iterationFactor*10 (kept integer to
// avoid floating-point quantization at the 0.1 step boundary), and
// workspace is 0 or 1.
//
// State dominance is the load-bearing invariant: maxNonLive is
//   1000 + 30*10 + 100 = 1400
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
// exactly representable as a float64, so a 300*float64(0.1) could
// land on 29 or 30 depending on rounding. Multiplying ints avoids it.
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
//   2 — live: in development, ready for development, waiting for r&d
//   0 — stalled: waiting for cs, paused
//   1 — neutral fallback: backlog, in review, in code review,
//       unknown / custom state names
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
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/ticket/rank/ -v
```

Expected: all six tests PASS.

- [ ] **Step 5: Run `go vet` on the new package**

```bash
go vet ./internal/ticket/rank/
```

Expected: no output (success).

- [ ] **Step 6: Commit**

```bash
git add internal/ticket/rank/
git commit -m "feat(rank): new package — state-dominant scoring with iteration decay

internal/ticket/rank owns ticket ordering for the start picker.
Score = 1000*stateTier + 30*iterationFactor10 + 100*hasWorkspace,
ordered by score desc, UpdatedAt desc. State dominance is enforced
by construction (maxNonLive=1400, minLive=2000).

backlog moved out of the live tier; in code review moved out of the
stalled tier — both fall through to neutral. Iteration distance >= 10
or sentinel -1 (no iteration / unresolved) contributes 0.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Wire `rank.Sort` into the legacy picker in `cmd/thicket/start.go`

**Why:** First of two call sites that consume `ListAssigned`. Inserting `rank.Sort` here, while the source still sorts internally, is a no-op user-visible — but it puts the new ranking on the user-facing path so we can validate end-to-end before stripping the in-source sort in Task 5.

**Files:**
- Modify: `cmd/thicket/start.go:565-575` (the chunk that builds `slugByTicket`)

- [ ] **Step 1: Add `rank.Sort` after the workspace lookup**

In `cmd/thicket/start.go`, edit `pickAssignedTicketLegacy`. Right after the `slugByTicket` map is built (currently around line 575), add a `rank.Sort` call:

```go
slugByTicket := make(map[string]string, len(workspaces))
for _, w := range workspaces {
	slugByTicket[w.State.TicketID] = w.Slug
}

// Re-rank tickets using the cross-source ranker. The shortcut source
// still returns them in its own order; rank.Sort imposes the
// state-dominant scoring described in
// docs/superpowers/specs/2026-05-16-ticket-ranking-design.md.
rank.Sort(tickets, func(sourceID string) bool {
	return slugByTicket[sourceID] != ""
})
```

Then add `"github.com/uribrecher/thicket/internal/ticket/rank"` to the imports at the top of `cmd/thicket/start.go` (alphabetical position among existing `internal/...` imports).

- [ ] **Step 2: Build to verify wiring**

```bash
go build ./...
```

Expected: success, no errors.

- [ ] **Step 3: Run all tests**

```bash
go test ./...
```

Expected: PASS. No behavior change at the user-visible level — every ticket has `IterationDistance=-1` after Task 1, so iteration factor is 0 everywhere, and `rank.Sort` produces the same ordering as the old shortcut-internal sort except for the bucket-mapping changes (`backlog` → neutral, `in code review` → neutral). Two existing tests in `client_test.go` may still fail because of those bucket changes — that's expected and will be fixed in Task 5.

Note: if `TestListAssigned_tieredSortByStateThenUpdatedAt` happens to fail at this point, leave it red and proceed to Task 4. It will be rewritten in Task 5.

- [ ] **Step 4: Commit**

```bash
git add cmd/thicket/start.go
git commit -m "feat(start): apply rank.Sort to the legacy picker

Re-rank the result of ListAssigned through the new cross-source
ranker. The shortcut source still sorts internally — that gets
stripped in a follow-up commit; this lands the wiring first so the
two-step change stays bisectable.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Wire `rank.Sort` into the TUI wizard's ticket page

**Why:** Second call site. Same pattern as Task 3, but the workspace-presence signal is exposed as a `FindExistingWorkspace(sourceID) *ManagedWorkspace` lookup func rather than a map.

**Files:**
- Modify: `internal/tui/wizard/start/ticket.go:96-105` (`listTicketsCmd`)

- [ ] **Step 1: Add `rank.Sort` inside `listTicketsCmd`**

Find `listTicketsCmd` in `internal/tui/wizard/start/ticket.go` (around line 96). Update it to sort the returned tickets before posting the result:

```go
func listTicketsCmd(m *wizard.Model) tea.Cmd {
	return func() tea.Msg {
		if m.Deps.Lister == nil {
			return wizard.TicketsLoadedMsg{Err: errors.New("ticket source does not support listing — pass a ticket id explicitly")}
		}
		tks, err := m.Deps.Lister.ListAssigned(m.Deps.Ctx)
		if err == nil {
			rank.Sort(tks, func(sourceID string) bool {
				return m.Deps.FindExistingWorkspace != nil &&
					m.Deps.FindExistingWorkspace(sourceID) != nil
			})
		}
		return wizard.TicketsLoadedMsg{Tickets: tks, Err: err}
	}
}
```

Then add `"github.com/uribrecher/thicket/internal/ticket/rank"` to the imports.

- [ ] **Step 2: Build to verify wiring**

```bash
go build ./...
```

Expected: success.

- [ ] **Step 3: Run all tests**

```bash
go test ./...
```

Expected: same state as after Task 3 — the `start` and `wizard` packages compile and pass; the shortcut bucket-mismatch tests remain red, to be fixed in Task 5.

- [ ] **Step 4: Commit**

```bash
git add internal/tui/wizard/start/ticket.go
git commit -m "feat(wizard): apply rank.Sort to the TUI ticket page

Mirror the cmd/thicket/start.go change for the interactive wizard
path — listTicketsCmd now re-ranks the ListAssigned result through
the cross-source ranker, with a closure over
m.Deps.FindExistingWorkspace as the workspace-presence predicate.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Strip the in-source sort and `stateRank` from shortcut; unfilter `in review`

**Why:** With both callers now invoking `rank.Sort`, the duplicate sort and the now-stale `stateRank` in the shortcut source are dead weight. This task removes them, also drops `in review` from the exclude set per spec, and refits the test file to match.

**Files:**
- Modify: `internal/ticket/shortcut/client.go:276-316, 369-379` (`excludedStateNames`, `stateRank`, the sort block in `ListAssigned`)
- Modify: `internal/ticket/shortcut/client_test.go:243-410` (existing ranking and filter tests)

- [ ] **Step 1: Update the existing filter test to expect `in review` to survive**

Open `internal/ticket/shortcut/client_test.go`. Find `TestListAssigned_filtersDoneArchivedAndExcludedStates` (around line 244). It currently asserts `in review` is filtered. Edit the test so `in review` is **kept** in the output and ends up with `State == "In Review"`. Concretely: change the assertion list so the expected output includes whichever fixture story is in `In Review` state.

If the existing test is too brittle to extend cleanly, replace it with this rewritten version:

```go
func TestListAssigned_filtersDoneAndArchivedAndExcludedStates(t *testing.T) {
	member := memberResponse{ID: "user-abc"}
	workflows := []workflowResponse{{
		States: []workflowStateResponse{
			{ID: 100, Name: "Ready for Dev", Type: "unstarted"},
			{ID: 101, Name: "In Development", Type: "started"},
			{ID: 102, Name: "In Review", Type: "started"}, // no longer excluded
			{ID: 103, Name: "Verifying", Type: "started"}, // still excluded
			{ID: 104, Name: "Done", Type: "done"},         // filtered by Type
		},
	}}
	stories := []storyResponse{
		{ID: 1, Name: "dev", WorkflowStateID: 101},
		{ID: 2, Name: "review", WorkflowStateID: 102},
		{ID: 3, Name: "verifying", WorkflowStateID: 103},
		{ID: 4, Name: "done", WorkflowStateID: 104},
		{ID: 5, Name: "archived", WorkflowStateID: 101, Archived: true},
	}
	srv := listAssignedServer(t, member, workflows, stories)
	defer srv.Close()

	got, err := New("tok", srv.URL).ListAssigned(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// 'dev' and 'review' survive. 'verifying' is excluded by name,
	// 'done' by workflow state Type, 'archived' by Archived flag.
	gotIDs := make(map[string]bool, len(got))
	for _, tk := range got {
		gotIDs[tk.SourceID] = true
	}
	want := map[string]bool{"sc-1": true, "sc-2": true}
	for id := range want {
		if !gotIDs[id] {
			t.Errorf("missing %s from output: %+v", id, got)
		}
	}
	for id := range gotIDs {
		if !want[id] {
			t.Errorf("unexpected ticket %s in output", id)
		}
	}
}
```

- [ ] **Step 2: Delete the now-obsolete sort/rank tests**

Delete these tests from `internal/ticket/shortcut/client_test.go` — their concerns now live in `rank_test.go`:

- `TestListAssigned_sortsByUpdatedAtDescending`
- `TestListAssigned_tieredSortByStateThenUpdatedAt`
- `TestStateRank`

- [ ] **Step 3: Run tests — expect the new filter test to fail until source code is updated**

```bash
go test ./internal/ticket/shortcut/ -run TestListAssigned_filtersDoneAndArchivedAndExcludedStates -v
```

Expected: FAIL — `in review` is still in `excludedStateNames`, so `sc-2` is missing.

- [ ] **Step 4: Update the shortcut source**

Edit `internal/ticket/shortcut/client.go`:

(a) **Remove `"in review"`** from `excludedStateNames` (around line 277). The map should now read:

```go
var excludedStateNames = map[string]bool{
	"ready for verification": true,
	"verifying":              true,
	"in verification":        true,
	"awaiting verification":  true,
	"qa":                     true,
	"ready for deploy":       true,
}
```

(b) **Delete the entire `stateRank` function** and its doc comment (currently lines ~286-316). It lives in `internal/ticket/rank/` now.

(c) **Remove the sort block** at the end of `ListAssigned` (currently around lines 369-379) — the `sort.SliceStable(kept, ...)` call and its accompanying comments. The function should hand `kept` straight into the output projection:

```go
	// Filter only — the cross-source ranker (internal/ticket/rank)
	// orders these in the caller. We keep the filtered slice in
	// whatever order Shortcut returned it; rank.Sort is stable, so
	// identical-score tickets preserve that order.
	out := make([]ticket.Ticket, 0, len(kept))
	for _, k := range kept {
		out = append(out, s.toTicket(k.sr, k.state))
	}
	return out, nil
}
```

Also delete the `"sort"` import if it's no longer used in the file.

- [ ] **Step 5: Run the shortcut test suite**

```bash
go test ./internal/ticket/shortcut/ -v
```

Expected: PASS.

- [ ] **Step 6: Run the full test suite**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/ticket/shortcut/client.go internal/ticket/shortcut/client_test.go
git commit -m "refactor(shortcut): remove in-source sort and stateRank; unfilter 'in review'

The cross-source ranker (internal/ticket/rank) now owns ordering for
both ListAssigned consumers. Drop the duplicate sort and stateRank
here, and let 'in review' through the filter — per the spec it lands
in the neutral band rather than being hidden entirely.

Ranking-shape tests move to rank_test.go; only the filter test stays
in this file, rewritten to expect 'in review' to survive.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Iteration support — fetch `/api/v3/iterations`, build timeline, set `IterationDistance`, filter future stories

**Why:** Adds the actual iteration signal. After this lands, `IterationDistance` on each Ticket reflects the story's position in the iteration timeline, and stories in future iterations are filtered out of the picker.

**Files:**
- Modify: `internal/ticket/shortcut/client.go:118-131` (`storyResponse`), `322-...` (`ListAssigned`)
- Modify: `internal/ticket/shortcut/client_test.go` (extend `listAssignedServer`, add iteration tests)

- [ ] **Step 1: Write the failing tests**

Add these tests to `internal/ticket/shortcut/client_test.go`. First, extend the existing test server helper so it can serve `/api/v3/iterations` — define a new helper alongside or replace `listAssignedServer` with one that takes an `iterations` slice:

```go
type iterationResponse struct {
	ID        int       `json:"id"`
	Status    string    `json:"status"`
	StartDate time.Time `json:"start_date"`
	EndDate   time.Time `json:"end_date"`
}

// listAssignedServerWithIterations is listAssignedServer + a handler
// for /api/v3/iterations. Existing callers can keep using
// listAssignedServer (which now delegates with iterations=nil).
func listAssignedServerWithIterations(t *testing.T, member memberResponse,
	workflows []workflowResponse, stories []storyResponse,
	iterations []iterationResponse) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Shortcut-Token"); got != "tok" {
			t.Errorf("token header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v3/member" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(member)
		case r.URL.Path == "/api/v3/workflows" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(workflows)
		case r.URL.Path == "/api/v3/iterations" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(iterations)
		case r.URL.Path == "/api/v3/stories/search" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var sb searchBody
			if err := json.Unmarshal(body, &sb); err != nil {
				t.Errorf("decode search body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(stories)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// Update the older helper to delegate so existing tests still compile:
func listAssignedServer(t *testing.T, member memberResponse,
	workflows []workflowResponse, stories []storyResponse) *httptest.Server {
	return listAssignedServerWithIterations(t, member, workflows, stories, nil)
}
```

Then add the iteration-behavior tests:

```go
func TestListAssigned_setsIterationDistance(t *testing.T) {
	member := memberResponse{ID: "u"}
	workflows := []workflowResponse{{
		States: []workflowStateResponse{{ID: 1, Name: "In Development", Type: "started"}},
	}}
	// Timeline (sorted by StartDate asc):
	//   id 10 (older)        — 2026-04-01 .. 04-14, done
	//   id 11 (previous)     — 2026-04-15 .. 04-28, done
	//   id 12 (current)      — 2026-04-29 .. 05-12, started
	// distances: current=0, previous=1, older=2.
	iter := func(id int, start string, status string) iterationResponse {
		s, _ := time.Parse("2006-01-02", start)
		return iterationResponse{ID: id, Status: status, StartDate: s, EndDate: s.AddDate(0, 0, 13)}
	}
	iterations := []iterationResponse{
		iter(10, "2026-04-01", "done"),
		iter(11, "2026-04-15", "done"),
		iter(12, "2026-04-29", "started"),
	}
	iter12 := 12
	iter11 := 11
	iter10 := 10
	stories := []storyResponse{
		{ID: 1, Name: "current", WorkflowStateID: 1, IterationID: &iter12},
		{ID: 2, Name: "previous", WorkflowStateID: 1, IterationID: &iter11},
		{ID: 3, Name: "older", WorkflowStateID: 1, IterationID: &iter10},
		{ID: 4, Name: "no-iter", WorkflowStateID: 1, IterationID: nil},
	}
	srv := listAssignedServerWithIterations(t, member, workflows, stories, iterations)
	defer srv.Close()

	got, err := New("tok", srv.URL).ListAssigned(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := map[string]int{
		"sc-1": 0,
		"sc-2": 1,
		"sc-3": 2,
		"sc-4": -1,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d tickets, want %d", len(got), len(want))
	}
	for _, tk := range got {
		w, ok := want[tk.SourceID]
		if !ok {
			t.Errorf("unexpected ticket %s", tk.SourceID)
			continue
		}
		if tk.IterationDistance != w {
			t.Errorf("%s: IterationDistance=%d, want %d", tk.SourceID, tk.IterationDistance, w)
		}
	}
}

func TestListAssigned_filtersFutureIterationStories(t *testing.T) {
	member := memberResponse{ID: "u"}
	workflows := []workflowResponse{{
		States: []workflowStateResponse{{ID: 1, Name: "In Development", Type: "started"}},
	}}
	iter := func(id int, start string, status string) iterationResponse {
		s, _ := time.Parse("2006-01-02", start)
		return iterationResponse{ID: id, Status: status, StartDate: s, EndDate: s.AddDate(0, 0, 13)}
	}
	iterations := []iterationResponse{
		iter(11, "2026-04-15", "done"),
		iter(12, "2026-04-29", "started"), // current
		iter(13, "2026-05-13", "unstarted"), // future
	}
	iter13 := 13
	iter12 := 12
	stories := []storyResponse{
		{ID: 1, Name: "future", WorkflowStateID: 1, IterationID: &iter13},
		{ID: 2, Name: "current", WorkflowStateID: 1, IterationID: &iter12},
	}
	srv := listAssignedServerWithIterations(t, member, workflows, stories, iterations)
	defer srv.Close()

	got, err := New("tok", srv.URL).ListAssigned(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].SourceID != "sc-2" {
		t.Errorf("expected only sc-2 to survive future-iteration filter, got %+v", got)
	}
}

func TestListAssigned_noStartedIterationLeavesDistanceSentinel(t *testing.T) {
	member := memberResponse{ID: "u"}
	workflows := []workflowResponse{{
		States: []workflowStateResponse{{ID: 1, Name: "In Development", Type: "started"}},
	}}
	iter := func(id int, start string, status string) iterationResponse {
		s, _ := time.Parse("2006-01-02", start)
		return iterationResponse{ID: id, Status: status, StartDate: s, EndDate: s.AddDate(0, 0, 13)}
	}
	iterations := []iterationResponse{
		iter(11, "2026-04-15", "done"),
		iter(12, "2026-04-29", "done"),
	}
	iter12 := 12
	stories := []storyResponse{
		{ID: 1, Name: "any", WorkflowStateID: 1, IterationID: &iter12},
	}
	srv := listAssignedServerWithIterations(t, member, workflows, stories, iterations)
	defer srv.Close()

	got, err := New("tok", srv.URL).ListAssigned(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d tickets, want 1", len(got))
	}
	// No started iteration → everyone gets sentinel -1.
	if got[0].IterationDistance != -1 {
		t.Errorf("IterationDistance=%d, want -1 when no started iteration exists",
			got[0].IterationDistance)
	}
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

```bash
go test ./internal/ticket/shortcut/ -run 'TestListAssigned_(setsIterationDistance|filtersFutureIterationStories|noStartedIterationLeavesDistanceSentinel)' -v
```

Expected: FAIL — `storyResponse` has no `IterationID` field, and `ListAssigned` does not call `/api/v3/iterations`.

- [ ] **Step 3: Add `IterationID` to `storyResponse`**

In `internal/ticket/shortcut/client.go`, update `storyResponse` (lines 118-131):

```go
type storyResponse struct {
	ID                     int             `json:"id"`
	Name                   string          `json:"name"`
	Description            string          `json:"description"`
	AppURL                 string          `json:"app_url"`
	FormattedVCSBranchName string          `json:"formatted_vcs_branch_name"`
	WorkflowStateID        int             `json:"workflow_state_id"`
	OwnerIDs               []string        `json:"owner_ids"`
	RequestedByID          string          `json:"requested_by_id"`
	Labels                 []labelResponse `json:"labels"`
	Archived               bool            `json:"archived"`
	UpdatedAt              time.Time       `json:"updated_at"`
	IterationID            *int            `json:"iteration_id"` // nil when not assigned
}
```

- [ ] **Step 4: Add the `iterationResponse` type and the timeline builder**

In `internal/ticket/shortcut/client.go`, near the other response types (right after `workflowStateResponse`), add:

```go
type iterationResponse struct {
	ID        int       `json:"id"`
	Status    string    `json:"status"` // unstarted | started | done
	StartDate time.Time `json:"start_date"`
	EndDate   time.Time `json:"end_date"`
}
```

Then, near the top of the file (after the existing helper functions like `excludedStateNames`), add a free function that computes the timeline:

```go
// buildIterationTimeline returns:
//
//   distance — sourceID-keyed map of iteration ID → step from the
//              current iteration. 0 = current, 1 = previous, etc.
//   future   — set of iteration IDs that are later in the timeline
//              than the current one. Stories in these iterations are
//              filtered out of the picker.
//
// "Current" is the latest-StartDate iteration with status="started".
// Ties broken by EndDate asc, then ID asc.
//
// If no started iteration exists, returns empty maps — the caller
// then treats every IterationID as the sentinel (factor 0) and
// nothing is filtered.
func buildIterationTimeline(iters []iterationResponse) (distance map[int]int, future map[int]bool) {
	distance = make(map[int]int, len(iters))
	future = make(map[int]bool)
	if len(iters) == 0 {
		return distance, future
	}

	// Stable order: StartDate asc, EndDate asc, ID asc.
	ordered := make([]iterationResponse, len(iters))
	copy(ordered, iters)
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if !a.StartDate.Equal(b.StartDate) {
			return a.StartDate.Before(b.StartDate)
		}
		if !a.EndDate.Equal(b.EndDate) {
			return a.EndDate.Before(b.EndDate)
		}
		return a.ID < b.ID
	})

	// Find the latest-indexed "started" iteration — that's "current".
	currentIdx := -1
	for i, it := range ordered {
		if it.Status == "started" {
			currentIdx = i
		}
	}
	if currentIdx == -1 {
		return distance, future
	}

	for i, it := range ordered {
		switch {
		case i > currentIdx:
			future[it.ID] = true
		case i == currentIdx:
			distance[it.ID] = 0
		default:
			distance[it.ID] = currentIdx - i
		}
	}
	return distance, future
}
```

You'll need `"sort"` back in the imports (Task 5 may have removed it; re-add if so).

- [ ] **Step 5: Wire the iteration fetch + timeline into `ListAssigned`**

Update `ListAssigned` in `internal/ticket/shortcut/client.go`. After the `workflows` fetch and before the stories search, fetch iterations and build the timeline. After the existing `stateByID` map build:

```go
	// ----- iterations -----
	//
	// Best-effort: if the iterations endpoint fails, we proceed with
	// an empty timeline. Every story then gets IterationDistance=-1
	// (factor 0 at the ranker) and nothing is filtered as "future".
	// This keeps the picker functional even if Shortcut briefly 5xx's
	// or the auth token loses iteration scope. The error is silently
	// swallowed because this file doesn't take a logger today;
	// surface-level logging belongs in a follow-up if we want it.
	var iterations []iterationResponse
	if err := s.doRequest(ctx, http.MethodGet, "/api/v3/iterations", nil, &iterations); err != nil {
		iterations = nil
	}
	distanceByIter, futureIter := buildIterationTimeline(iterations)
```

Then update the filter loop. The existing loop builds a `kept []filtered` slice; extend it to skip future-iteration stories:

```go
	kept := make([]filtered, 0, len(stories))
	for _, sr := range stories {
		if sr.Archived {
			continue
		}
		st, ok := stateByID[sr.WorkflowStateID]
		if !ok || st.Type == "done" {
			continue
		}
		if excludedStateNames[strings.ToLower(st.Name)] {
			continue
		}
		if sr.IterationID != nil && futureIter[*sr.IterationID] {
			continue // future iteration — out of scope for the picker
		}
		kept = append(kept, filtered{sr, st.Name})
	}
```

- [ ] **Step 6: Annotate `IterationDistance` in the output projection**

Either pass the `distanceByIter` map into `toTicket`, or compute `IterationDistance` in the output loop after `toTicket` returns. The cleanest change with the smallest API impact is to do it inline in the loop at the end of `ListAssigned`:

```go
	out := make([]ticket.Ticket, 0, len(kept))
	for _, k := range kept {
		tk := s.toTicket(k.sr, k.state)
		if k.sr.IterationID != nil {
			if d, ok := distanceByIter[*k.sr.IterationID]; ok {
				tk.IterationDistance = d
			}
			// If the iteration isn't in our timeline (e.g. archived
			// after the workflow fetch), tk.IterationDistance stays at
			// the toTicket default of -1.
		}
		out = append(out, tk)
	}
	return out, nil
}
```

- [ ] **Step 7: Run the iteration tests**

```bash
go test ./internal/ticket/shortcut/ -run 'TestListAssigned_(setsIterationDistance|filtersFutureIterationStories|noStartedIterationLeavesDistanceSentinel)' -v
```

Expected: all three PASS.

- [ ] **Step 8: Run the full shortcut suite and the full repo suite**

```bash
go test ./internal/ticket/shortcut/ -v
go test ./...
```

Expected: PASS.

- [ ] **Step 9: Run `go vet` across the repo**

```bash
go vet ./...
```

Expected: no output.

- [ ] **Step 10: Commit**

```bash
git add internal/ticket/shortcut/client.go internal/ticket/shortcut/client_test.go
git commit -m "feat(shortcut): graduated iteration distance + future-iteration filtering

ListAssigned now fetches /api/v3/iterations, builds a start_date-
sorted timeline, finds the latest-indexed started iteration as the
'current' anchor, and annotates each surfaced Ticket with
IterationDistance: 0 for current, 1 for previous, … Stories in
future iterations (later than the anchor) are filtered out of the
picker entirely.

Best-effort: if the iterations endpoint fails, the picker still
loads with every distance left at the -1 sentinel (factor 0 at the
ranker). 'In review' is no longer hidden (handled in the previous
commit). Tests cover distance assignment, future filtering, and the
no-started-iteration fallback.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Final Verification

- [ ] **Run the full repo test suite**

```bash
go test ./... -race -count=1
```

Expected: PASS, no race warnings.

- [ ] **Run `go vet` and `go build`**

```bash
go vet ./...
go build ./...
```

Expected: clean.

- [ ] **Manual smoke against a real Shortcut workspace**

Confirm in a real `thicket start` invocation:

- A ticket in the current (started) iteration ranks above a ticket from a previous iteration in the same state band.
- A ticket several iterations back with a local thicket workspace ranks above a same-band ticket in the same old iteration without a workspace.
- A ticket in a **future** iteration does not appear in the picker.
- A ticket with no iteration assigned still appears (just without the iteration boost).
- A ticket in `In Review` now appears in the picker (was filtered before).
- A ticket in `In Code Review` lands among the neutral entries (not sunk to the bottom alongside `Paused`).
- A ticket in `Backlog` now lands in the neutral band, not at the top.

- [ ] **Graceful-degradation check**

Temporarily break the iterations call (e.g. point the base URL at a host that 404s `/api/v3/iterations`, or shadow the token) and confirm the picker still loads — every iteration distance stays at `-1`, no tickets are filtered as "future".
