# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Initial implementation: `thicket init`, `start`, `list`, `rm`, `catalog`,
  `doctor`, `version` subcommands.
- Shortcut ticket source.
- Claude (Anthropic API, Haiku) repo detection via tool-use.
- Interactive `huh`-driven selection and clone-confirm prompts.
- Password-manager-backed secrets: 1Password (`op`), Bitwarden (`bw`),
  `pass`, and an `env` mode for CI. Thicket never asks for raw tokens —
  the config stores only item references and we fetch on demand.
- 1Password multi-account: when `op account list` shows more than one
  account, `thicket init` asks which one to use and scopes every later
  `op` call via `--account <uuid>`.
- `CLAUDE.local.md` workspace memory file.
- Atomic-ish workspace creation with rollback on failure.
- GoReleaser-based cross-compile for darwin/linux × amd64/arm64 published
  to GitHub Releases.
- `Taskfile.yaml` with `build`, `test`, `lint`, `release:*`, `ci`, etc.
  Run `task --list` to see the full set. `task build` produces a binary
  at `bin/thicket` with version/commit/date baked in.
