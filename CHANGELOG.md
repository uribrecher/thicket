# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

_Nothing yet._

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
