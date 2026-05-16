# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

_Nothing yet._

## [0.6.3] - 2026-05-17

Patch release — two follow-on bug fixes shaken out of daily use of
the v0.6.x build.

### Fixed

- **`thicket edit` workspace picker no longer misaligns on emoji.** The
  picker rendered with `wizard.PadRight` / `wizard.Truncate`, which
  measured rune count rather than visible terminal cells — a 🔍 in the
  Nickname column under-filled the column by one cell and pushed every
  subsequent column right. Same fix as v0.6.2's `thicket list`: the
  `wizard.*` helpers now delegate to the runewidth-backed `tui.*`
  helpers, which also fixes the `thicket start` ticket picker, the
  config picker, and the repo pickers in both wizards.

- **`thicket start` ticket picker shows real iteration numbers again,
  not `—` for every row.** Shortcut's `/api/v3/iterations` returns
  `start_date` / `end_date` as date-only strings (`"2026-05-06"`), but
  the response struct typed them as `time.Time`, which expects RFC 3339.
  Decoding failed silently — the error was swallowed, the iteration
  timeline came back empty, and every story fell back to
  `IterationDistance = -1`, rendering `—` in the `Iter` column for
  every ticket regardless of whether the story actually belonged to
  the started iteration. A custom `UnmarshalJSON` on `iterationResponse`
  now parses the date-only wire format, and the iteration test server
  emits the real format so the regression is now covered.

## [0.6.2] - 2026-05-17

Patch release — domain-aware shell completion plus two UX fixes
shaken out of daily use of the v0.6.x build.

### Added

- **Shell completion is now domain-aware.** `thicket completion <shell>`
  used to emit cobra boilerplate that only completed subcommand and
  flag names. Tab completion now suggests real values:
  - `thicket rm <Tab>` / `thicket edit <Tab>` list managed workspace
    slugs from `workspace_root`, with each slug's nickname surfaced as
    the zsh/fish description.
  - `thicket start --only <Tab>` lists repo names from the local
    catalog cache, with the repo's description as the completion
    description. Comma-aware: already-typed repos are filtered out,
    and `NoSpace` keeps the cursor flush so `,nextrepo` continuations
    work without backspacing.

  Intentional non-completions: `thicket start <ticket-id>` (would
  block the shell on a Shortcut API round-trip) and `--branch` (no
  obvious local candidate source) stay free-form. Completion errors
  are silent — a missing config or unreadable cache yields zero
  suggestions rather than a shell-side error. Descriptions are
  defensively flattened (tabs/newlines/CRs stripped) before joining
  with the value so a stray tab in a legacy manifest can't re-split
  the completion line.

### Fixed

- **`thicket list` no longer blows past 200 columns or misaligns rows
  with emoji nicknames.** The old `text/tabwriter` layout showed both
  SLUG and BRANCH at their natural width and counted bytes (not visible
  cells), so a 🔍 in the nickname shifted every column to its right.
  The new layout renders with `tui.PadRight` / `tui.Truncate` — the
  go-runewidth-backed helpers that pad and clip by visible terminal
  cells — and pins each column to a fixed width so the table comes in
  at 118 visible cells total (108 of content + five 2-space gaps),
  comfortably narrower than the 200+ cells the old layout produced on
  real workspaces. SLUG stays in the table because `thicket
  edit [slug]` and `thicket rm [slug]` still take a slug, and the
  branch column is truncated (and can be overridden with `--branch`),
  so BRANCH is not a reliable substitute.

- **`thicket version` no longer mislabels the date.** Output now reads
  `thicket X.Y.Z (sha, committed YYYY-MM-DDTHH:MM:SSZ)` instead of
  `built …`. Since v0.6.1 the field is the commit timestamp (for
  reproducibility), not the wall-clock build time, so `built` was
  misleading.

## [0.6.1] - 2026-05-16

Build-hygiene patch — no user-visible behavior change.

### Changed

- **Reproducible builds: same commit + same Go version → byte-identical
  binary.** Three changes get there:
  - `-trimpath` on `go build`/`go install` and in goreleaser, replacing
    absolute filesystem paths with module-relative ones. Released
    tarballs no longer leak the builder's `$HOME`/workspace path.
  - `BUILD_DATE` in the Taskfile now uses `git log -1 --format=%cI`
    (commit author date) instead of `date -u` (wall clock), so the
    `-X main.date=…` ldflag is pinned to the commit.
  - goreleaser's `ldflag` for `main.date` switches from `{{.Date}}` to
    `{{.CommitDate}}`, and `builds.mod_timestamp` is set to
    `{{.CommitTimestamp}}` so the binary's file metadata is also
    deterministic.

  Verified locally: two consecutive `task build` invocations produce
  the same SHA-256, and two consecutive `task release:snapshot` runs
  produce the same binary per target. Size delta from `-trimpath`
  alone is ~80 KB; the other changes are zero-byte.

  The `thicket version` line now shows the **commit date** ("built
  …") rather than the wall-clock build time. Semantically a better
  signal anyway: it tells you which version of the code you're
  running, not when someone happened to compile it.

## [0.6.0] - 2026-05-16

Feature release — the `thicket start` ticket picker now ranks tickets
on more than just state and `updated_at`. Active-iteration proximity
and the presence of a live local workspace both push tickets up,
state buckets are refined, and future-iteration tickets are hidden
from the picker entirely.

### Changed

- **`thicket start` ticket picker now scores tickets with a
  state-dominant composite — iteration distance and local workspace
  presence join state as ranking signals.** Score formula:

  ```
  score = 1000 * stateTier  +  30 * iterationFactor10  +  100 * hasWorkspace
  ```

  ordered by `score desc, UpdatedAt desc`.

  - **State is still the strongest signal.** A "live dev" ticket the
    user forgot to migrate into the current sprint still outranks a
    "paused" ticket inside it. State dominance is enforced by
    construction (max non-live score = 1400, min live = 2000).
  - **Iteration is graduated.** Current iteration adds the full
    boost; previous adds 90% of it; ten-back-and-older add nothing.
    Distance is computed in a `start_date`-sorted timeline anchored
    on the latest-`started` iteration.
  - **Future-iteration tickets are filtered out.** Stories sitting
    in a sprint that hasn't started yet no longer surface in the
    picker.
  - **Local-workspace boost.** A ticket with a thicket workspace
    materialized adds 100 — enough to push an older-sprint ticket
    above a same-band fresher-sprint ticket without a workspace.

- **State buckets refined.**
  - `In Review` is no longer hidden — it surfaces in the **neutral**
    band rather than being filtered out.
  - `In Code Review` moves from **stalled** to **neutral** — a
    ticket waiting on a reviewer is in-flight, not done.
  - `Backlog` moves from **live** to **neutral** — unstarted tickets
    no longer dominate work the user is actually doing.

- **`Iter` column added to the ticket picker.** Shows iteration
  distance per ticket (`0` = current sprint, `1` = previous, …,
  `—` = no iteration). Useful for eyeballing why a ticket landed
  where it did.

### Internal

- New `internal/ticket/rank` package owns the cross-source scoring.
  The Shortcut source is reduced to fetch + filter + annotate
  `Ticket.IterationDistance`; both `ListAssigned` consumers
  (`cmd/thicket/start.go:pickAssignedTicketLegacy` and
  `internal/tui/wizard/start/ticket.go:listTicketsCmd`) call
  `rank.Sort` after the source returns.
- `Ticket` carries two new fields: `UpdatedAt time.Time` (tiebreaker
  for the ranker) and `IterationDistance int` (`-1` sentinel =
  unknown / no iteration). Sources that don't know about iterations
  emit `-1`.

## [0.5.5] - 2026-05-15

Patch release — a handful of UX papercuts surfaced by daily use of
the v0.5.0 nickname + iTerm2 features, plus the v0.1.x
`gh api user/orgs` auto-detect behavior that got lost in the wizard
refactor.

### Changed

- **`thicket start` ticket picker prioritizes live dev work over
  stalled tickets.** The Shortcut `ListAssigned` source now sorts
  by a two-tier key: state-rank descending, then UpdatedAt
  descending within a tier.
  - **Top tier:** `In Development`, `Ready for Development`,
    `Backlog`, `Waiting for R&D` — the states where a developer is
    most likely to want a fresh workspace.
  - **Middle tier:** any unrecognized state name (sensible neutral
    default for custom workflows).
  - **Bottom tier:** `In Code Review`, `Waiting for CS`, `Paused` —
    work that's done from the dev's POV or explicitly stalled.
  - Within a tier the existing most-recently-touched ordering is
    preserved. Filter (`done`-type states, archived stories, the
    excluded-by-name list) still runs first.

### Fixed

- **Picker tables align correctly when nicknames contain emoji.**
  `tui.PadRight` and `tui.Truncate` now measure visible terminal
  cells via `go-runewidth` instead of rune count, so emoji-bearing
  cells (2 cells per glyph but only 1 rune) no longer over-fill
  their column and shift neighbouring columns right. The most
  visible symptom was `thicket rm` and `thicket list` rows looking
  misaligned whenever some workspaces had emoji nicknames and
  others didn't.

- **`thicket rm` search placeholder no longer collapses to "t".**
  The picker's filter input was missing the `Width = 60` workaround
  the start/edit wizards already had — bubbles' textinput truncates
  its placeholder to the first character when `Width == 0`, so
  "type to filter…" rendered as just "t". Width set explicitly.

- **`thicket config` Git page auto-detects GitHub orgs again.** The
  v0.1.x behavior — probe `gh api user/orgs` on page entry, auto-
  fill a single result, surface a checkbox multiselect for two or
  more — got lost in the wizard refactor and was left as a plain
  CSV textinput. Restored:
  - One org returned → silently fills the textinput so the user
    just hits enter through the page.
  - Two or more orgs → the field flips into an in-page checkbox
    picker (↑/↓ moves cursor, space toggles, tab still cycles to
    the next page-level field). Defaults to all selected; respects
    a re-run's previously-saved `github_orgs` subset.
  - gh missing / unauthenticated / no org memberships → the
    textinput stays as the fallback so the user can type manually.
  - The textinput is the source of truth in both modes, so the
    page's Complete-gate and commit logic stay mode-agnostic.

## [0.5.0] - 2026-05-15

Workspace-ergonomics pass on `thicket start`: skip the picker when
you're already cd'd into a workspace, get an LLM-suggested
nickname + iTerm2 tab tint per workspace so concurrent Claude
sessions are visually distinct in the tab strip, and stop fighting
macOS over per-`op` permission prompts in `thicket config`.

### Added

- **iTerm2 tab tinting + title + badge per workspace.** When `thicket
  start` launches Claude under iTerm2 (detected via
  `$LC_TERMINAL` / `$TERM_PROGRAM`), the workspace's nickname is
  written to the tab title AND iTerm2 tab badge, and the
  per-workspace color (new) tints the tab background — so multiple
  concurrent workspace tabs are immediately distinguishable in the
  tab strip.
  - The LLM suggester now returns **both** the nickname and a
    `#RRGGBB` color in one call. The prompt asks the model to mine
    distinctive entities from the ticket (customer / company names
    like Wix / Munich Re / Workday / Rivian, hosting-service names
    like SharePoint / S3 / Snowflake / Databricks, file-format
    keywords like CAD / DICOM) and to choose a color drawing from
    famous brand palettes (AWS orange, MS blue, Snowflake cyan,
    Atlassian blue, etc.) with work-type fallbacks (red for bugs,
    purple for spikes, etc.). The currently-in-use colors of OTHER
    open workspaces are passed in too so the model picks a
    contrasting hue — no more "every tab is the same red".
  - Nickname length bumped from 20 → **25 chars** so emoji-prefixed
    acronyms like "🐛 MR Snowflake enum" or "⚡ WD GDrive scan" fit
    without being chopped.
  - New `color` field in `workspace.State` (`omitempty` — pre-color
    manifests still load cleanly). The Plan page renders a swatch
    + the hex code as a read-only row beside the nickname.
  - Non-iTerm2 terminals: every escape-writing helper is a no-op.
    No garbage in the terminal, no visual change.

- **Per-workspace nicknames — short, friendly, LLM-suggested.**
  Every new workspace now carries an optional `nickname` field
  alongside its slug: a short (≤20 chars), human-readable label that
  allows spaces and emoji and doesn't have to be unique. The Plan
  page in the `thicket start` wizard shows an editable input
  pre-populated with an LLM-suggested label (built from the ticket
  title + body, fired in parallel with the existing summary call so
  it doesn't add to perceived latency). The user can accept the
  suggestion or type their own; ↑/↓ moves between the missing-clones
  rows, the nickname input, and the Create button.
  - Stored in `<workspace>/.thicket/state.json` as a new
    `nickname` JSON field (`omitempty` — existing manifests
    round-trip cleanly with no migration).
  - Displayed in `thicket list` (new `NICKNAME` column), `thicket
    rm` (picker column + confirmation header), `thicket edit`
    (picker column + filter haystack), and the cwd-shortcut log
    line (`✓ using existing workspace "🐛 picker fix" (sc-12-fix-flaky-picker)`).
  - Passed to Claude as `--name <nickname>` when set (slug
    otherwise), so the session label in Claude's prompt box,
    `/resume` picker, and terminal title is the friendly label
    instead of the long slug.
  - `thicket start --nickname '<label>'` for the non-interactive /
    scripted path: overrides the LLM suggestion.

- **`thicket start` from inside a workspace skips the ticket picker.**
  When the working directory is already under an existing thicket
  workspace (`workspace_root/<slug>/...`, including any worktree
  subdir like `workspace_root/<slug>/<repo>`), running `thicket
  start` with no positional id detects the containing workspace and
  re-launches Claude on it immediately — no ticket fetch, no wizard,
  no network round-trip. Same end state as the existing
  "ticket-id-already-has-a-workspace" reuse shortcut, just driven by
  pwd. Explicit `thicket start <id>` still wins (the user picked a
  specific ticket; don't second-guess). `--no-launch` and `--dry-run`
  both print the `cd` line instead of execing claude. Applies to
  both the interactive wizard path and the legacy non-interactive
  path — the pwd signal is unambiguous either way.

### Changed

- **`thicket config` runs `op signin` once per account to silence the
  cascading macOS prompts.** The 1Password walk used to fire a
  separate "iTerm would like to access data from other apps" dialog
  on each of `op account list`, `op item list`, and `op item get`.
  After the user picks an account, the wizard now runs `op signin
  --account <X>` once for that account; the desktop-app integration
  warms up, and the item-list + item-get pair runs silently. Signin
  always passes `--account` so `op` can never fall back to its
  interactive account picker (which would have stolen `/dev/tty`
  from the Bubble Tea TUI). Only successful signins are cached —
  a dismissed biometric prompt leaves the entry absent so the next
  item-pick attempt retries cleanly within the same wizard run.
- **Submit page drops the misleading top-level `manager:` line.**
  Each secret picks its own manager via the per-slot picker, so a
  single global `manager:` value on the review page was
  oversimplifying. The `op://` prefix and the `(env var $X)` hint
  already make each ref's provenance explicit.

## [0.4.0] - 2026-05-15

Replaces `thicket init` with `thicket config` — a 5-page Bubble Tea
wizard that walks you through first-run setup (or lets you edit an
existing config on re-run).

### Changed

- **`thicket init` is now `thicket config`.** The wizard reads and
  writes `~/.config/thicket/config.toml`, so `config` describes the
  user-visible thing it does (and avoids the confusion of "init"
  looking like `git init`). `thicket init` is no longer recognized
  — re-run with `thicket config`.

- **`thicket config` is a 5-page Bubble Tea wizard.** Welcome → Git
  → Tickets → Agent → Submit, with the same tab/footer/← /→ keymap
  as `thicket start` and `thicket edit`. Pages render conditionally:
  Welcome only on first run, Tickets only when `$SHORTCUT_API_TOKEN`
  is unset — so the tab bar shows exactly the steps you'll see.
  - **1Password picker preserved inline.** The account → item →
    field cascade is a 9-state state machine in the wizard rather
    than a chain of `huh` sub-prompts. Items are cached per account
    so the Tickets and Agent pages share a single biometric prompt
    instead of paying for it twice.
  - **macOS App Management hint.** On macOS, after the first
    1Password walk-through, the picker offers to open System
    Settings → Privacy & Security → App Management directly via
    `x-apple.systempreferences:`. Granting iTerm there silences
    the recurring "iTerm would like to access data from other apps"
    prompt that fires on every `op` invocation.
  - **Re-running is safe.** Existing values pre-fill every input;
    the Welcome page is skipped on re-run; an existing `op://…`
    reference jumps straight to "validated" so you don't have to
    re-walk the cascade just to confirm what's already saved.

## [0.3.0] - 2026-05-14

Adds `thicket edit` for attaching repos to an existing workspace, an
LLM-generated 3-line ticket summary on the Repos page (replacing the
dumb first-3-lines-of-description view), and a most-recently-touched
sort on the Shortcut ticket picker.

### Added

- **`thicket edit`: add repos to an existing workspace.** New command
  that opens a 3-page Bubble Tea wizard (Workspace → Repos → Submit)
  for attaching more git worktrees to a workspace you've already
  created. Solves the "I forgot a repo at start time" recovery case
  without destroying the workspace and its uncommitted work.
  - **Workspace page** picks from `workspace.ListManaged`, the same
    source `thicket rm` uses. Columns: Slug · Ticket · Branch ·
    Created · Repos. Workspaces without a state manifest are
    filtered out (can't safely add without knowing the branch).
  - **Repos page** is start's catalog picker minus the LLM suggestion
    section. Repos already in the workspace render as dim `[locked]`
    rows that ignore Enter/space — MVP doesn't support repo removal
    (use `thicket rm` + `thicket start` for that). New picks land in
    an "Adding" group; fuzzy search re-ranks the catalog the same way
    `thicket start` does.
  - **Submit page** mirrors `start`'s Plan page: builds an `AddPlan`,
    shows what will be cloned + what worktrees will be attached, runs
    clones in-page with the same proceed-without-failed-repo policy
    on failure.
  - **`thicket edit <slug>`** preselects the workspace and skips the
    first page — parallel to `thicket start <id>`.
  - **CLAUDE.local.md is regenerated** with the union of old + new
    repos via a new `memory.RegenPreservingStatusLog` helper that
    splits the file at the `## Status log` heading and preserves
    everything below it verbatim. So past Status-log entries an agent
    appended across sessions survive the edit. On parse failure
    (existing file lacks the marker, e.g. user heavily edited it) we
    fall back to a fresh render and warn on stderr.
  - **State manifest writes are now atomic** (temp + rename) so a
    crash mid-edit can't leave the workspace with a corrupt
    `.thicket/state.json`.

- **`thicket start`: real LLM-generated ticket summary.** The
  three-line "summary" block at the top of the Repos page used to
  be the literal first three non-empty lines of the description —
  fine for short tickets, useless when the body opens with a
  markdown heading or a fenced code block. The wizard now calls
  Claude (via whichever backend `claude_backend` is set to — same
  CLI or API path the repo detector uses) to produce an actual
  3-line summary of the ticket and caches it per ticket id. The
  call runs in parallel with the repo-detection call so it doesn't
  add to perceived latency; while it's in flight (or if the
  summarizer fails / isn't wired) the renderer falls back to the
  old first-N-lines view so the panel always shows something.

### Changed

- **`thicket start` ticket picker is now sorted by last-modified
  descending.** The Shortcut `ListAssigned` source orders the
  authenticated user's open assigned stories by `updated_at` (most
  recently touched first) before handing them to the wizard, so the
  tickets you've been actively poking at land at the top of the
  picker instead of in whatever order `/stories/search` returned
  them in. Stable sort — stories with identical timestamps keep
  Shortcut's relative order.

## [0.2.0] - 2026-05-14

`thicket start` now runs as a three-page Bubble Tea wizard (Ticket →
Repos → Plan) with tab navigation, a unified match list that puts
every repo in exactly one place, in-page clone progress, and a
substring-preferring fuzzy search. The Shortcut client surfaces
ticket body / requester / labels inline. `thicket rm` and
`thicket init` are unchanged.

### Added

- **`thicket start`: interactive wizard with tab navigation.** The
  interactive TTY path now runs as a single Bubble Tea program with
  three pages — `Ticket`, `Repos`, `Plan` — rendered as a horizontal
  tab bar at the top of the screen. The active step is a filled
  pink pill (black on bright pink), completed steps are green, and
  untouched steps are dim gray — pure foreground/background
  contrast does the wayfinding, no extra underline row. `←/→` move
  between completed steps; `Esc` cancels. Each page binds `Enter`
  to its own commit action (pick / toggle / create) so it never
  lies about what Enter does. A single consolidated footer line
  combines the active page's local key hints with the wizard-level
  nav keys — no duplicate "type to filter" between the placeholder
  and the footer. The Ticket page picks from your open assigned
  tickets in a fuzzy-searchable table. After you pick a ticket the
  Repos page shows its body, requester, and labels at the top so
  you can sanity-check context before deciding on repos; it seeds
  the catalog eagerly so fuzzy search works immediately while a
  charm spinner runs the LLM call in parallel. The match list is
  one unified view with three groups — Selected at the top (with
  `relevance N% — <reason>` tags preserved when the item came from
  the model), Available fuzzy matches in the middle, and Suggested
  LLM picks at the bottom — so every repo appears in exactly one
  place and toggling on/off is just `↑/↓` to the row + `Enter`.
  Suggestions are sorted by descending confidence, and the fuzzy
  search re-ranks `sahilm/fuzzy` output so contiguous substring
  matches beat scattered character-pluck matches (typing `setup`
  surfaces `sentra-setup-service` first, not the scattered hits in
  `sentra-user-ops`). LLM picks are cached by ticket id — going
  back to peek at the ticket and forward again skips the 15-30s
  re-fetch. The Plan page lists the cloned-on-create repos
  ahead of the workspace summary, with checkboxes for any missing
  clones (default checked; uncheck to drop the repo). When you hit
  `Create`, clones stream in-page with ✓/✗ lines; a clone failure
  drops the failed repo from the workspace and continues with the
  rest (skipped repos are surfaced on stderr after the wizard
  exits).
- **`thicket start <id>`: pre-selected ticket flow.** Passing a
  ticket id on the command line short-circuits the picker — the
  wizard lands on the Repos page, with the Ticket page rendering a
  read-only summary you can still peek at via `←`.
- **Shortcut: ticket body, requester, and labels.** `Ticket.Body`,
  `Ticket.Requester` (resolved via `/api/v3/members/{id}`), and
  `Ticket.Labels` are now populated on fetch and surfaced inline by
  the wizard's Repos page summary block. Best-effort: a failed
  member lookup leaves `Requester` empty rather than aborting the
  flow.

### Changed

- **`thicket start` falls back to the pre-wizard CLI flow when the
  wizard can't run** — `--no-interactive`, `--dry-run`, and non-TTY
  stdin (CI, pipes) keep today's line-oriented output. `thicket rm`
  and `thicket init` are unchanged; they still use `tui.PickOne`
  for their own pickers.

## [0.1.4] - 2026-05-14

`thicket start` and `thicket rm` are now transparent about what
they're about to do and what they're doing — no more "press Enter
and pray". Plus a small CI-infra change so the cache-busting
check can be enforced as a Required status check on `main`.

### Added

- **`thicket start`: plan preview + confirm + per-step progress.**
  Before touching disk, `thicket start` now prints the workspace
  plan (workspace dir, branch, worktrees count + per-repo branch
  mode) with a yellow-bold `plan:` header. A confirm prompt
  follows (defaults to Yes — Enter accepts; Esc/Ctrl+C exits with
  a friendly `cancelled.`). `--no-interactive` skips the prompt;
  non-TTY stdin auto-skips with a clear notice. During the actual
  create, ✓ lines stream per worktree + memory file + state
  manifest so the user sees exactly what landed when.
- **`thicket rm`: per-step ✓ progress.** Removal now streams one
  ✓ per worktree (with its source-repo path) and a final
  `✓ deleted workspace directory: <dir>` instead of jumping
  straight from the confirm to a one-line `removed`. Failure
  paths surface live too: `✗ could not remove worktree foo: …`
  followed by `(workspace directory preserved — re-run with
  --force …)`. Ctrl+C / Esc on the confirm prints `cancelled.`
  (was a hard error).
- **Required-check enforcement for the cachebust workflow.**
  `.github/workflows/cachebust-check.yaml` dropped its path
  filter and now runs on every PR, so it can be registered as a
  Required status check in branch protection. Idempotent and
  ~5s end-to-end. (Repo-settings step still has to be flipped
  once by an admin: Settings → Branches → Branch protection
  rules → `main` → Require status checks → add
  `cachebust-check / check`.)

## [0.1.3] - 2026-05-13

### Added

- **GitHub Pages landing site.** Single-page `docs/index.html`
  with hero splash, asciinema-player demo of the `thicket start`
  flow, six-card feature grid, install + quickstart sections.
  Pure HTML/CSS + a single CDN-loaded player script (pinned + SRI),
  no build step. Served at https://uribrecher.github.io/thicket/.
  Includes a `cachebust-check` GitHub Action that fails PRs which
  forget to bump the `styles.css?v=…` content-hash stamp.
  "Install on macOS" CTA copies the install one-liner + opens a
  modal walking the user through pasting it into Terminal.
  Live GitHub-star pill in the top-right with stargazer count
  fetched anonymously from the API.

### Fixed

- **`thicket start` interactive picker: Enter no longer silently
  finishes when you meant to deselect.** Previously, Enter with an
  empty search query unconditionally finished the picker — even
  when the cursor was sitting on a selected repo, so users moving
  the cursor onto a row and pressing Enter expecting "drop this
  row" instead silently finished with that repo still in the
  workspace. Now: Enter ALWAYS toggles the cursor row (search
  view and selection view alike); `Tab` finishes (with a guard
  against finishing an empty selection). Help text updated:
  `↑/↓ navigate · enter toggle · tab finish · esc cancel`.
- **Spurious "ticket has no description" warning after the
  interactive ticket picker.** Tickets that DO have a description
  in Shortcut were triggering the warning because
  `/api/v3/stories/search` returns a slim `StorySearchResult` that
  doesn't reliably carry the Markdown body. `thicket start` now
  re-fetches the full story by id after the picker returns,
  preserving the picker-resolved workflow-state name on top of
  the fetched ticket.

## [0.1.2] - 2026-05-13

Self-update. Most commands now check for a newer release once a day
and offer to apply it; new `thicket update` command for the manual
path. `THICKET_NO_UPDATE_CHECK=1` or `--no-update-check` opts out.

### Added

- **Self-update.** Most commands (all except `version`, `help`, and
  `update` itself) run a quick probe (cached for 24h, bounded by a
  2-second HTTP timeout) against `releases/latest`. When
  a newer release is available in a TTY, you get a confirm prompt —
  saying yes downloads the matching tarball, verifies SHA-256 against
  the release's `checksums.txt`, and atomically swaps the running
  binary in place. Saying no remembers the declined version for the
  rest of the 24h window so you're not pestered every command. New
  `thicket update` command bypasses the cache for a manual
  check-and-apply. Disable entirely with `THICKET_NO_UPDATE_CHECK=1`
  or `--no-update-check`. Skipped for dev/dirty builds, non-TTY
  output (a one-line hint is printed instead), and binaries
  installed under Homebrew / Nix / `go install` / source-build
  paths — the prompt for those falls back to a copy-paste install
  command. Cache lives at `$XDG_CONFIG_HOME/thicket/.update-check.json`.

## [0.1.1] - 2026-05-13

Polish round driven by the v0.1.0 beta. Mostly `thicket init` UX
plus an interactive ticket picker for `thicket start`.

### Added

- **`thicket start` reuses an existing workspace.** If a workspace
  already exists for the chosen ticket (matched by ticket id, so
  renamed tickets still resolve), `start` skips repo detection,
  selection, and workspace creation and opens Claude directly on the
  existing directory with the same `--name <slug>` label. Works
  whether the ticket comes from the picker or from
  `thicket start <id>`.
- **`thicket start` interactive ticket picker.** With no id arg,
  opens a fuzzy-search picker over your active assigned Shortcut
  tickets (`Ticket | State | Title | Workspace` columns). Filters
  out archived stories, `done`-type states, and common "out of dev
  hands" states (In Review, Verifying, In Verification, Ready for
  Verification, Awaiting Verification, QA, Ready for Deploy).
  Cross-references against existing workspaces so each row shows
  the slug already on disk, if any. Powered by a new optional
  `ticket.Lister` interface; the Shortcut source implements it via
  `GET /member` + `GET /workflows` + `POST /stories/search`.
- **Env-var detection announced earlier in `init`.** When
  `$SHORTCUT_API_TOKEN` / `$ANTHROPIC_API_KEY` are set, the
  `✓ found $X in env` lines now print before the Claude-backend
  picker, so the cli-vs-api decision is informed.

### Changed

- **`init` is now a 3-step state machine; Esc goes back.** Pressing
  Esc at any prompt during `init` returns to the previous step
  instead of cancelling the whole flow. From step 0, Esc cancels.
  Caveat: huh binds Esc and Ctrl-C to the same exit return, so
  Ctrl-C also acts as "back" mid-flow — only on step 0 does it
  actually quit.
- **`init` skips the welcome note on re-runs.** Only first-time
  invocations (when no config file exists) get the hello screen.
  Re-running `init` to tweak settings jumps straight to the form.
- **`init` auto-picks the only available GitHub org.** When
  `gh api user/orgs` returns exactly one org, the multiselect is
  skipped and we print `✓ GitHub org: <name>`.
- **`init` skips the password-manager picker when all secrets are
  env-covered.** If `$SHORTCUT_API_TOKEN` (and `$ANTHROPIC_API_KEY`
  for `claude_backend = api`) are exported, the picker and per-
  secret ref collection are bypassed; `passwords.manager` is set to
  `"env"` for config validation.
- **Welcome copy trimmed.** "Walk through the prerequisites once…"
  is now "First time here — let's configure your workflow."

### Fixed

- Welcome note no longer interrupts re-runs of `init` that only
  intend to change a value or two.

## [0.1.0] - 2026-05-13

Initial release.

### Added
- Initial implementation: `thicket init`, `start`, `list`, `rm`, `catalog`,
  `doctor`, `version` subcommands.
- Shortcut ticket source.
- Claude (Anthropic API, Haiku) repo detection via tool-use.
- Interactive `huh`-driven selection and clone-confirm prompts.
- Password-manager-backed secrets: 1Password (`op`), Bitwarden (`bw`),
  `pass`, and an `env` mode for CI. Thicket never asks for raw tokens —
  the config stores only item references and we fetch on demand.
- 1Password multi-account: each secret carries its own account
  (`shortcut_token_account`, `anthropic_key_account`). `thicket init`
  prompts per secret; the previous slot's account is the default for
  the next.
- `claude_backend = "cli" | "api"` config. CLI mode shells out to the
  local `claude` binary — no Anthropic API key needed (handy for
  users on a Claude Enterprise subscription). Init wizard skips the
  Anthropic key slot entirely when `claude_backend = cli`.
- `SHORTCUT_API_TOKEN` and `ANTHROPIC_API_KEY` env vars short-circuit
  the password-manager lookup at runtime; `thicket init` skips those
  slots when the env vars are already set. `thicket doctor` reports
  the override.
- `CLAUDE.local.md` workspace memory file.
- Atomic-ish workspace creation with rollback on failure.
- GoReleaser-based cross-compile for darwin/linux × amd64/arm64 published
  to GitHub Releases.
- `Taskfile.yaml` with `build`, `test`, `lint`, `release:*`, `ci`, etc.
  Run `task --list` to see the full set. `task build` produces a binary
  at `bin/thicket` with version/commit/date baked in.
