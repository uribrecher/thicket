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
- OS-keychain secret storage with env-var and plain-text fallbacks.
- `CLAUDE.local.md` workspace memory file.
- Atomic-ish workspace creation with rollback on failure.
