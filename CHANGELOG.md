# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- **Self-update.** Every command runs a background probe (cached for
  24h) against `releases/latest`. When a newer release is available
  in a TTY, you get a confirm prompt — saying yes downloads the
  matching tarball, verifies SHA-256 against the release's
  `checksums.txt`, and atomically swaps the running binary in place.
  Saying no remembers the declined version for the rest of the 24h
  window so you're not pestered every command. New `thicket update`
  command bypasses the cache for a manual check-and-apply.
  Disable entirely with `THICKET_NO_UPDATE_CHECK=1` or
  `--no-update-check`. Skipped for dev/dirty builds, non-TTY output
  (a one-line hint is printed instead), and binaries installed under
  Homebrew / Nix / `go install` / source-build paths — the prompt
  for those falls back to a copy-paste install command.

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
