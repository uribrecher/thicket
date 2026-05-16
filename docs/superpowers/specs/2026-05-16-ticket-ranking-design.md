# Ticket ranking: state-dominant scoring with iteration and workspace boosts

## Context

`thicket start` (no arg) opens a fuzzy-search picker over the user's
assigned Shortcut tickets. The order of the list matters a lot — the
top of the list is what the user picks 90% of the time. Today
`internal/ticket/shortcut/client.go:ListAssigned` sorts tickets by
`(stateRank tier desc, UpdatedAt desc)` where `stateRank` returns 2
(live dev), 1 (neutral), or 0 (stalled).

Two signals are missing from the ranking:

1. **Active iteration.** A ticket in the user's current sprint is much
   more likely to be the one they want to start work on than one from a
   past sprint, regardless of how recently it was updated.
2. **Local workspace presence.** If the user already has a thicket
   workspace materialized for a ticket, they almost certainly want it
   high in the list — even if that ticket pre-dates the current sprint.

This spec adds both signals via a composite score, refines the state
buckets, and moves the ranking logic out of the source into a small
package so it can sit at the layer that knows about workspaces.

## Scoring

```
score = 1000 * stateTier + 300 * iterationFactor + 100 * hasWorkspace

where iterationFactor = max(0, 1.0 - distance * 0.1)
   distance = 0  → current iteration            → factor 1.0
   distance = 1  → previous iteration           → factor 0.9
   distance = 2  → two iterations back          → factor 0.8
   ...
   distance ≥ 10 → factor 0.0
   no iteration  → factor 0.0   (not filtered, just no boost)
```

`stateTier ∈ {0, 1, 2}` and `hasWorkspace ∈ {0, 1}`. Primary sort:
`score desc`. Tiebreaker: `UpdatedAt desc`. Stable sort, so
identical-score tickets preserve the order Shortcut returned them in.

State dominates intentionally: a "still in dev" ticket the user forgot
to migrate into the current iteration should outrank a "paused" ticket
inside it. State dominance holds across all values of the other
signals — max non-live score is `1000 + 300 + 100 = 1400`, min live is
`2000`.

The graduated iteration signal means the workspace bonus (100) starts
to outweigh the iteration bonus once distance ≥ 7 (where `300 × 0.3 =
90 < 100`). This matches the intent that a ticket from an older
sprint with a live workspace should rank above one with no workspace
from a similarly-old sprint.

Example computed scores (state=live for compactness — same shape
inside neutral/stalled bands):

```
2400  live + current iter        + workspace
2300  live + current iter
2270  live + previous iter       + workspace
2170  live + previous iter
2250  live + 5-back iter         + workspace
2150  live + 5-back iter
2100  live                       + workspace
2000  live + 10-back-or-older iter           (≡ live alone, factor 0)
2000  live (no iteration)                    (factor 0)
```

Every "live" still beats every "neutral" beats every "stalled". This
invariant is deliberate — it gives the user a predictable mental
model when they're looking for "where did my ticket go?".

## State-bucket refinements

Two changes alongside the new scoring:

- **Unfilter `in review`.** Currently `excludedStateNames` drops it
  entirely. Move it out of the exclude list so it appears in the
  picker. With no entry in `stateRank` it falls through to the default
  neutral tier.
- **Move `in code review` from stalled → neutral.** Remove its case
  from the tier-0 arm of `stateRank` so it falls through to default
  neutral. A ticket waiting on a reviewer is "in flight" — it
  shouldn't sink to the bottom alongside `paused` and `waiting for cs`.

After the change:

- Tier 2 (live): `in development`, `ready for development`,
  `waiting for r&d`
- Tier 0 (stalled): `waiting for cs`, `paused`
- Tier 1 (neutral): default — includes `backlog`, `in review`,
  `in code review`, and any custom workflow state name we don't
  explicitly bucket

`backlog` moves out of the live bucket because it represents "not yet
started" — surfacing those above tickets the user is actively working
on inverts the picker's most-likely-pick ordering. They still rank
above stalled work, just below in-progress work.

Excluded states (unchanged except `in review` removed): `done`,
`completed`, `closed`, `verifying`, `in verification`, `awaiting
verification`, `qa`, `ready for deploy`. These are filtered before
ranking so they never appear in the picker.

## Iteration timeline, distance, and future filtering

A Shortcut iteration carries `status ∈ {unstarted, started, done}` and
a `start_date` / `end_date`. The "current" iteration is the latest
`status == "started"` entry by `start_date`. (Users in multiple groups
may have several started iterations simultaneously; picking the
latest-start one as the anchor avoids treating overlapping current
sprints as "future".)

Implementation:

- Add `IterationID *int` to `storyResponse` so we can read
  `iteration_id` off each story.
- One extra round-trip in `ListAssigned`: `GET /api/v3/iterations`,
  decoded into a small `iterationResponse{ID int, StartDate, EndDate
  time.Time, Status string}`.
- Build a timeline: all iterations sorted by `(StartDate asc,
  EndDate asc, ID asc)`. The current iteration is the latest-indexed
  entry with `Status == "started"`. Call its index `currentIdx`.
- For each iteration `iter` in the timeline at index `i`:
  - `i > currentIdx`  → mark "future".
  - `i == currentIdx` → distance = 0.
  - `i <  currentIdx` → distance = `currentIdx - i`.
  Persist as `map[iterationID]distance` plus a `set[iterationID]` of
  future ones.
- During the existing filter loop in `ListAssigned`, drop stories
  whose `iteration_id` is in the future set — same shape as the
  existing "exclude `done`/archived" check.
- For surviving stories, set `IterationDistance int` on the resulting
  `ticket.Ticket`:
  - `IterationDistance = distance` when `iteration_id` resolves to a
    known iteration in the timeline.
  - `IterationDistance = -1` when `iteration_id == nil` OR resolves to
    an iteration we don't have a record for (deleted/archived). The
    ranker treats anything < 0 as factor 0 — same as distance ≥ 10.

If no started iteration exists at all: `currentIdx` is undefined; the
future set is empty (no filtering); every ticket's distance is
sentinel `-1` (factor 0). The ranking degrades to "state + workspace"
gracefully.

If the iterations endpoint fails: proceed with an empty timeline.
Same gracefully-degraded behavior — nothing filtered, all iteration
factors 0.

**Observability:** the shortcut source doesn't carry a logger today,
so unresolved-iteration cases and iteration-fetch failures are
silently swallowed. Adding a logger is left as a follow-up; the
trade-off taken here is "ship the ranking; iterate on observability
later".

## Workspace-presence detection

Workspace state already lives in `internal/workspace`. Two call sites
need this signal — the legacy non-interactive picker in
`cmd/thicket/start.go:pickAssignedTicketLegacy` and the TUI wizard's
ticket page in `internal/tui/wizard/start/ticket.go:listTicketsCmd`.
Each already has its own way of knowing which tickets have a
workspace:

- The legacy picker already builds `slugByTicket` from
  `workspace.ListManaged(cfg.WorkspaceRoot)` at `start.go:572`.
- The wizard receives a `m.Deps.FindExistingWorkspace(sourceID)`
  lookup func from the wizard deps wiring.

To avoid forcing both callers into the same shape, the ranker takes a
predicate:

```go
func Sort(tickets []ticket.Ticket, hasWorkspace func(sourceID string) bool)
```

Each caller passes whatever closure is natural at that site:

```go
// legacy
rank.Sort(tickets, func(id string) bool { return slugByTicket[id] != "" })

// wizard
rank.Sort(tickets, func(id string) bool {
    return m.Deps.FindExistingWorkspace != nil &&
        m.Deps.FindExistingWorkspace(id) != nil
})
```

Match key: `ticket.Ticket.SourceID` against `workspace.State.TicketID`.
Both are the canonical stringified ID (e.g. `sc-12345`).

If `ListManaged` returns warnings, log them at debug level and proceed
with whatever workspaces it did surface — same gracefully-degrading
pattern as the iterations fetch.

## Architectural change

Ranking moves out of `shortcut.Source` into a new package:

- New package `internal/ticket/rank/`. Exposes:
  ```go
  func Sort(tickets []ticket.Ticket, hasWorkspace func(sourceID string) bool)
  ```
  It computes the composite score in-place and stable-sorts the slice
  by `score desc, UpdatedAt desc`. A `nil` predicate is treated as
  "no ticket has a workspace" — no boost applied.
- `internal/ticket/shortcut/client.go:ListAssigned` is reduced to
  fetch + filter + annotate `IterationDistance` (and `UpdatedAt`,
  surfaced from the existing `storyResponse.UpdatedAt`). The
  `stateRank` helper moves to `internal/ticket/rank/` (it's a pure
  `string → int` mapping with no Shortcut-specific behavior).
- Both callers of `ListAssigned` — `cmd/thicket/start.go:pickAssignedTicketLegacy`
  and `internal/tui/wizard/start/ticket.go:listTicketsCmd` — invoke
  `rank.Sort` after the source returns, passing the closure shown in
  the previous section.

Trade-off: this changes the contract of `ListAssigned`. The two
existing ranking tests in `client_test.go`
(`TestListAssigned_sortsByUpdatedAtDescending`,
`TestListAssigned_tieredSortByStateThenUpdatedAt`) move to a new
`rank_test.go` and gain coverage for the new signals. The filter tests
(`TestListAssigned_filtersDoneArchivedAndExcludedStates`,
`TestListAssigned_unauthorizedSurfacesClearError`,
`TestListAssigned_emptyStoriesNotAnError`) stay in `client_test.go`.

## `Ticket` struct changes

Add two fields to `internal/ticket/source.go:Ticket`:

```go
// UpdatedAt is the last-modified timestamp at the source. Used by
// the ranker as a tiebreaker (most-recently-touched first).
// Sources that don't surface this leave it zero.
UpdatedAt time.Time

// IterationDistance is the integer step from the source's "current"
// iteration to this ticket's iteration, in timeline order:
//   0  → ticket sits in the current iteration
//   1  → previous iteration
//   N  → N iterations back
//  -1  → ticket has no iteration, or its iteration could not be
//        resolved against the timeline (sentinel; ranker treats as
//        factor 0, same as distance ≥ 10). Sources that don't know
//        about iterations always emit -1.
IterationDistance int
```

Both are typed cross-source fields (rather than living in `Extra`)
because the ranker reads them on every ticket — pushing them into a
map-of-strings would force a string parse on a hot path. `UpdatedAt`
also lifts an existing per-source detail (it lived only on
`shortcut.storyResponse` before) onto the cross-source type so the
ranker doesn't need a per-source escape hatch for tiebreaking.

The zero value `0` of `IterationDistance` would mean "current
iteration" — which is the *wrong* default for sources that haven't
computed it. To make the sentinel unambiguous, all source-side
construction sites must set `IterationDistance = -1` explicitly when
no information is available. A small constructor / default in
`toTicket` keeps this from being forgotten.

## Files touched (implementation summary)

| File | Change |
|---|---|
| `internal/ticket/source.go` | Add `UpdatedAt time.Time` (tiebreaker) and `IterationDistance int` to `Ticket`; convention: `-1` = "unknown / no iteration" |
| `internal/ticket/shortcut/client.go` | Add `iteration_id` to `storyResponse`; fetch `/api/v3/iterations`; build the timeline; filter future-iteration stories; remove `in review` from `excludedStateNames`; remove `in code review` from `stateRank`; delete the in-source sort and the `stateRank` function (moved); annotate `IterationDistance` in `toTicket` |
| `internal/ticket/shortcut/client_test.go` | Drop ranking-shape assertions from filter tests; keep filter coverage |
| `internal/ticket/rank/rank.go` *(new)* | `Sort(tickets, hasWorkspace)` + the moved `stateRank` |
| `internal/ticket/rank/rank_test.go` *(new)* | Cover the score formula and bucket invariants |
| `cmd/thicket/start.go` | After `ListAssigned` in `pickAssignedTicketLegacy`, call `rank.Sort` with a closure over `slugByTicket` |
| `internal/tui/wizard/start/ticket.go` | In `listTicketsCmd`, call `rank.Sort` with a closure over `m.Deps.FindExistingWorkspace` before handing tickets to the page |

## Verification

End-to-end:

1. `go test ./...` — all green, new `rank` package coverage in place.
2. Manual smoke against a real Shortcut workspace, with at least:
   - A ticket in the current (started) iteration.
   - A ticket in the immediately-previous iteration.
   - A ticket several iterations back that has a local thicket
     workspace.
   - A ticket in a future iteration (should NOT appear in the picker).
   - A ticket with no iteration assigned at all (should appear, no
     iteration boost).
   - Run `thicket start` and verify the order matches the formula —
     specifically that the older-iteration-with-workspace ticket lands
     above same-band tickets in same-old iterations without workspace.
3. Confirm `in review` tickets now appear in the picker (they were
   filtered before) and land in the neutral band.
4. Confirm `in code review` tickets no longer sink to the bottom —
   they appear among the neutral entries.
5. Confirm future-iteration tickets are filtered out entirely.
6. Verify graceful degradation: temporarily break the
   `/api/v3/iterations` call (e.g. point at a 404) and confirm the
   picker still loads with iteration distance = -1 for everyone
   (factor 0) rather than failing.

## UI surface

Both picker tables (the legacy `cmd/thicket/start.go` picker and the
TUI wizard's ticket page) gain a single new column, **`Iter`**,
rendered after **`Workspace`**:

- `0` for the current iteration, `1` for the previous, etc.
- `—` (em dash) when `IterationDistance < 0` — no iteration / could
  not resolve.

`rank.FormatIterationDistance(d int) string` centralises the
rendering so both call sites agree on the format. The column is
debug-grade context — useful for eyeballing why a ticket landed
where it did. Other row content (`Ticket`, `State`, `Title`,
`Workspace`) is unchanged.

## Non-goals

- **No iteration / workspace badges beyond the Iter column.** No
  highlighting, no row colour change, no group-by-iteration view.
- **No source-pluggable scoring.** Other ticket sources (Linear,
  Jira…) reuse the same `rank` package; per-source overrides aren't
  needed yet.
- **No persisted ranking config.** Weights are constants in the
  `rank` package. If the user ever wants to tune them, that's a
  follow-up change.
