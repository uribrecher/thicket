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
score = 1000 * stateTier + 300 * activeIteration + 100 * hasWorkspace
```

Where `stateTier ∈ {0, 1, 2}`, `activeIteration ∈ {0, 1}`, and
`hasWorkspace ∈ {0, 1}`. Primary sort: `score desc`. Tiebreaker:
`UpdatedAt desc`. Stable sort, so identical-score tickets preserve the
order Shortcut returned them in.

State dominates intentionally: a "still in dev" ticket the user forgot
to migrate into the active iteration should outrank a "paused" ticket
inside the active iteration. Within a state band, active-iteration is a
3× larger boost than has-workspace, matching the "active iteration
much higher than other iterations" requirement while still letting the
workspace signal break ties.

Resulting order (top → bottom):

```
2400  live    + active-iter + workspace
2300  live    + active-iter
2100  live    + workspace
2000  live
1400  neutral + active-iter + workspace
1300  neutral + active-iter
1100  neutral + workspace
1000  neutral
 400  stalled + active-iter + workspace
 300  stalled + active-iter
 100  stalled + workspace
   0  stalled
```

Every "live" beats every "neutral" beats every "stalled". This is a
deliberate invariant — it gives the user a predictable mental model
when they're looking for "where did my ticket go?".

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

## Active-iteration detection

A Shortcut iteration has `status ∈ {unstarted, started, done}`. Active
iterations are those with `status == "started"`. A user in multiple
groups can be in several started iterations at once — all count.

Implementation:

- Add `IterationID *int` to `storyResponse` (the JSON shape) so we can
  read `iteration_id` off each story.
- One extra round-trip in `ListAssigned`:
  `GET /api/v3/iterations`, then client-side filter to entries with
  `status == "started"`. Build a `set[int]` of started iteration IDs.
  (Client-side filter is the safe choice — the Shortcut API has at
  times accepted a `status` query param but documentation has not
  always reflected it; the result set is small enough that filtering
  locally costs nothing.)
- For each kept story, set a new field `IterationActive bool` on the
  returned `ticket.Ticket` to `iterationID != nil && startedSet[*iterationID]`.

If the iterations endpoint fails, log a warning and treat
`IterationActive` as `false` for everything — the ranking degrades
gracefully to "state + workspace" instead of failing the whole
`ListAssigned` call.

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
  fetch + filter + annotate `IterationActive`. The `stateRank` helper
  moves to `internal/ticket/rank/` (it's a pure `string → int` mapping
  with no Shortcut-specific behavior).
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

## `Ticket` struct change

Add one field to `internal/ticket/source.go:Ticket`:

```go
IterationActive bool  // true iff the ticket is in a Shortcut iteration with status=started
```

It's a typed cross-source field (rather than living in `Extra`)
because the ranker reads it on every ticket — pushing it into a
map-of-strings would force a string parse on a hot path.

Sources that don't know about iterations leave it `false` (the zero
value). When more sources gain iteration support, they populate it the
same way.

## Files touched (implementation summary)

| File | Change |
|---|---|
| `internal/ticket/source.go` | Add `IterationActive bool` to `Ticket` |
| `internal/ticket/shortcut/client.go` | Add `iteration_id` to `storyResponse`; fetch `/api/v3/iterations`; remove `in review` from `excludedStateNames`; remove `in code review` from `stateRank`; delete the in-source sort and the `stateRank` function (moved); annotate `IterationActive` in `toTicket` |
| `internal/ticket/shortcut/client_test.go` | Drop ranking-shape assertions from filter tests; keep filter coverage |
| `internal/ticket/rank/rank.go` *(new)* | `Sort(tickets, hasWorkspace)` + the moved `stateRank` |
| `internal/ticket/rank/rank_test.go` *(new)* | Cover the score formula and bucket invariants |
| `cmd/thicket/start.go` | After `ListAssigned` in `pickAssignedTicketLegacy`, call `rank.Sort` with a closure over `slugByTicket` |
| `internal/tui/wizard/start/ticket.go` | In `listTicketsCmd`, call `rank.Sort` with a closure over `m.Deps.FindExistingWorkspace` before handing tickets to the page |

## Verification

End-to-end:

1. `go test ./...` — all green, new `rank` package coverage in place.
2. Manual smoke against a real Shortcut workspace:
   - At least one ticket in an active iteration, one with a local
     thicket workspace but in a closed sprint, and one in a closed
     sprint with no workspace.
   - Run `thicket start` and verify the bucket order matches the
     table in the **Scoring** section.
3. Confirm `in review` tickets now appear in the picker (they were
   filtered before) and land in the neutral band.
4. Confirm `in code review` tickets no longer sink to the bottom —
   they appear among the neutral entries.
5. Verify graceful degradation: temporarily break the
   `/api/v3/iterations` call (e.g. point at a 404) and confirm the
   picker still loads with iteration-boost disabled rather than
   failing.

## Non-goals

- **No UI badges.** Iteration membership and workspace presence are
  used for sort only. The picker's row format stays unchanged.
- **No source-pluggable scoring.** Other ticket sources (Linear,
  Jira…) reuse the same `rank` package; per-source overrides aren't
  needed yet.
- **No persisted ranking config.** Weights are constants in the
  `rank` package. If the user ever wants to tune them, that's a
  follow-up change.
