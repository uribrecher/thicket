<p align="center">
  <img src=".github/assets/thicket.png" alt="thicket — CLI for Claude &amp; Tickets" width="600">
</p>

<p align="center">
  <a href="https://github.com/uribrecher/thicket/actions/workflows/ci.yaml"><img src="https://github.com/uribrecher/thicket/actions/workflows/ci.yaml/badge.svg" alt="CI"></a>
  <a href="https://github.com/uribrecher/thicket/actions/workflows/release.yaml"><img src="https://github.com/uribrecher/thicket/actions/workflows/release.yaml/badge.svg" alt="Release"></a>
  <a href="https://github.com/uribrecher/thicket/releases/latest"><img src="https://img.shields.io/github/v/release/uribrecher/thicket?display_name=tag&amp;sort=semver" alt="Latest release"></a>
  <a href="https://github.com/uribrecher/thicket/releases"><img src="https://img.shields.io/github/downloads/uribrecher/thicket/total" alt="Downloads"></a>
  <a href="./go.mod"><img src="https://img.shields.io/github/go-mod/go-version/uribrecher/thicket" alt="Go version"></a>
  <a href="./LICENSE"><img src="https://img.shields.io/github/license/uribrecher/thicket" alt="License"></a>
</p>

# thicket

> A thicket is a cluster of trees grown around the same root cause —
> in our case, one ticket and the worktrees it spawns.

`thicket` is a CLI that turns one ticket into an isolated, ready-to-code
multi-repo workspace. Given a Shortcut ticket id it:

1. Fetches the ticket from your tracker
2. Asks Claude which repos in your GitHub org(s) need code changes
3. Opens a bubbletea picker so you can fuzzy-search the catalog and
   refine the LLM's pre-selection (type-ahead, ↑/↓, Enter toggles)
4. Materializes a workspace folder with one git worktree per repo on a
   ticket-id-prefixed branch (slug is always `<lower-ticket-id>-<title>`,
   so two tickets with the same title never collide on disk)
5. Auto-clones any repos that aren't on your machine yet
6. Drops a `CLAUDE.local.md` into the workspace so your AI coding session
   inside it has full ticket context, across reboots and context resets
7. Launches Claude Code (`claude --name <slug>`) so the session is
   labelled in the prompt box, `/resume` picker, and terminal title —
   handy for distinguishing several open workspaces

End result: `thicket start sc-12345` → fuzzy-pick repos → you're coding.

> **Unofficial, community-built. Not affiliated with or endorsed by
> Anthropic.** "Claude" is a trademark of Anthropic, PBC.

## Install

### One-liner (macOS, Linux)

```sh
curl -fsSL https://github.com/uribrecher/thicket/releases/latest/download/install.sh | sh
```

The script:
- Detects your OS + arch (`darwin`/`linux`, `amd64`/`arm64`).
- Downloads the matching release tarball and `checksums.txt`.
- Verifies the SHA-256 against the checksums file before installing.
- Drops the `thicket` binary into `$HOME/.local/bin` and tells you to
  add that to `$PATH` if it isn't already.

Pin a specific version: `THICKET_VERSION=v0.1.0 sh`. Pick a different
install dir: `INSTALL_DIR=/usr/local/bin sh` (will need `sudo`).

### With Go

```sh
go install github.com/uribrecher/thicket/cmd/thicket@latest
```

Drops the binary at `$GOBIN` or `$GOPATH/bin`. If `go install` complains
"`cannot install, GOBIN must be an absolute path`" fix once with
`go env -w GOBIN="$HOME/go/bin"`.

### Manual

Grab a tarball from
[the latest release](https://github.com/uribrecher/thicket/releases/latest)
and drop `thicket` on your `$PATH`.

### From source

```sh
git clone https://github.com/uribrecher/thicket
cd thicket
task build           # produces ./bin/thicket
# or
go install ./cmd/thicket
```

Requires Go 1.24+, `git`, [`gh`](https://cli.github.com/), and optionally
[`task`](https://taskfile.dev/) on `$PATH`. `task --list` shows the full
dev task set.

## Quickstart

```sh
# One-time setup wizard: prompts for tokens, GitHub orgs, and paths.
thicket init

# Sanity check.
thicket doctor

# Spawn a workspace for a ticket.
thicket start sc-12345
```

When you're done with a workspace:

```sh
thicket list                  # show active workspaces, newest first

thicket rm                    # interactive picker over all workspaces
thicket rm 12345              # picker pre-filtered to the typed query
thicket rm sc-12345-fix-x     # exact slug → preview + Y/N confirm, then remove
thicket rm sc-12345-fix-x --yes   # skip the confirm (for scripts)
```

`rm` always prints a preview (workspace path, worktrees, source repos)
and asks for explicit Y/N confirmation before deleting — `--force`
lets it remove dirty worktrees, but the absence of a state manifest
also requires `--force` to protect non-thicket folders that happen to
live under `workspace_root`.

## How it works

```
~/code/                          # repos_root — where you keep clones
├── acme-foo/   ← source clone, stays on main
└── acme-bar/

~/tasks/                         # workspace_root — where thicket puts workspaces
└── sc-12345-fix-inventory/
    ├── CLAUDE.local.md          ← seeded with ticket context
    ├── .thicket/state.json      ← manifest for `thicket rm`
    ├── acme-foo/              ← git worktree on uri/sc-12345-fix-inventory
    └── acme-bar/              ← git worktree on uri/sc-12345-fix-inventory
```

Source clones never get checked out to a feature branch — they stay clean
on their default branch. The workspace contains only worktrees, which
share the `.git` of their source clone (storage cheap, no double-fetch).

## Interactive UX

Most of the interactive flows are bubbletea + lipgloss views with live
fuzzy search:

- **Repo picker** (`thicket start`) — type a partial name, see top
  matches ranked by `sahilm/fuzzy`, Enter toggles selection. Empty
  query shows your current selection so you can drop entries quickly.
- **1Password item picker** (`thicket init` under 1Password) — tabular
  view: `Item | Vault | Type`, live filter, ↑/↓ + Enter.
- **Workspace picker** (`thicket rm`) — tabular view:
  `Slug | Ticket | Created | Repos`, newest first.

Slow operations show a single-line in-place elapsed-time spinner so
the CLI never looks stuck:

```
fetching repo catalog from GitHub ([acme]) — 2.4s
looking for relevant repos — 6.8s
cloning git@github.com:acme/acme-foo.git → /Users/uri/code/acme-foo — 5.1s
```

## Configuration

`~/.config/thicket/config.toml`:

```toml
repos_root      = "~/code"
workspace_root  = "~/tasks"
default_branch  = "main"
claude_model    = "claude-haiku-4-5"
claude_binary   = "claude"
claude_backend  = "cli"          # "cli" | "api" — see Secrets section

ticket_source   = "shortcut"
github_orgs     = ["your-org"]

[shortcut]
workspace_slug  = "your-shortcut-workspace"

# Optional aliases for `--only` and LLM hints
[[repo_alias]]
name    = "service-name"
aliases = ["short", "alias"]
```

`thicket init` populates `github_orgs` from a multi-select over the
orgs your `gh` user actually belongs to (no typed placeholders) and
runs `gh repo list <org> --limit 1` for each to warn if any are
unreachable (auth vs. typo distinguished).

### Secrets

Thicket **never asks you to paste raw tokens**. Instead, you point it at
your password manager and we fetch on demand. The config records only a
*reference* per secret — the live value never touches `config.toml`.

Supported managers (pick one in `thicket init`):

| Manager | CLI | Reference format |
| ------- | --- | ---------------- |
| 1Password | [`op`](https://developer.1password.com/docs/cli/) | `op://<vault>/<item>/<field>` |
| Bitwarden | [`bw`](https://bitwarden.com/help/cli/) | item id or name (after `bw unlock`) |
| pass | [`pass`](https://www.passwordstore.org/) | store-relative path (e.g. `work/shortcut`) |
| env | (none) | environment variable name (for CI / headless) |

Two top-level toggles drive the Claude-side cost/auth story:

```toml
# How thicket talks to Claude for repo detection:
#   "cli" — shell out to the local `claude` binary; reuses Claude
#           Code / Enterprise auth, no API key needed.
#   "api" — call the Anthropic API directly; requires anthropic_key_ref.
claude_backend = "cli"
```

The secrets block is per-secret — each can live in a different
1Password account:

```toml
[passwords]
manager = "1password"

shortcut_token_ref     = "op://Employee/Shortcut/credential"
shortcut_token_account = "576UUGKY6NCYTDLB42Z2C3XNH4"   # 1password only

# Only needed when claude_backend = "api"
anthropic_key_ref     = "op://Personal/Anthropic/credential"
anthropic_key_account = "CUFCNCRFFVCXLBZ2BJCBZGVFOY"
```

When `claude_backend = "cli"`, `thicket init` skips the Anthropic key
slot entirely. For 1Password, init walks each secret one at a time:
account picker → item autocomplete → field picker. The previous slot's
account is offered as the default for the next, so single-account users
just press Enter and multi-account users can switch per secret.

### Env-var overrides

Two env vars always short-circuit the password manager at runtime:

- `SHORTCUT_API_TOKEN` — used as the Shortcut token if set
- `ANTHROPIC_API_KEY` — used as the Anthropic key if set (irrelevant when
  `claude_backend = "cli"`)

If either is set when you run `thicket init`, the corresponding slot is
skipped with a notice. Useful for CI, one-off runs, and the
`SHORTCUT_API_TOKEN=… thicket start sc-123` quick-test pattern.

`thicket doctor` verifies the CLI is installed, the vault is unlocked, and
each reference resolves to a value — without ever showing the value.

## Command reference

```
thicket init            First-run wizard (or re-run to edit existing config).
thicket start <ticket>  Spawn a workspace.
   --only foo,bar       Skip the LLM; use exactly these repos.
   --branch <name>      Override the branch name (slug stays ticket-id-prefixed).
   --no-interactive     Accept the LLM picks; auto-clone missing repos.
   --no-launch          Don't auto-launch claude after creating.
   --dry-run            Print the plan, change nothing on disk.

thicket list            Show active workspaces (newest first).
thicket rm [slug]       Remove a workspace + its worktrees.
                          - No arg: interactive picker.
                          - Partial slug: picker pre-filtered.
                          - Exact slug: skip picker, jump straight to preview.
   --force              Allow removing dirty worktrees AND deleting
                          directories with no state manifest.
   --yes                Skip the Y/N confirm prompt.

thicket catalog         Show the GitHub-org repo cache.
   --refresh            Re-fetch via `gh`.

thicket doctor          Diagnose config, tokens, external tools.
                        Reports password-manager status, per-secret
                        fetchability, and env-var overrides.
thicket version         Print version info.
```

## Troubleshooting

**`doctor` says `claude binary not found`** — that's a warning, not an error.
Thicket still works; it just won't auto-launch your AI session and instead
prints `cd` instructions when the workspace is ready.

**`gh` 401 / 403** — run `gh auth login` and ensure the resulting token has
read access to your orgs. `thicket init` will say so explicitly.

**LLM picks the wrong repos** (or none) — type in the bubbletea picker
to fuzzy-add, prefix the query with `-` to drop, or pass `--only foo,bar`
to bypass the LLM entirely. An empty ticket body triggers a warning
that the LLM has nothing to route on; the picker still works.

**"workspace exists"** — pick a different `--branch` or
`thicket rm <slug>` first.

**Worktree creation fails partway** — thicket rolls back already-created
worktrees and removes the workspace dir. The source repos are untouched.

**`thicket rm` refused because of "no state manifest"** — the workspace
wasn't created by thicket (or its `.thicket/state.json` was deleted).
Pass `--force` if you're sure you want it gone.

**`task install` says "GOBIN must be an absolute path"** — your Go env
has a relative path set. Fix once with
`go env -w GOBIN="$HOME/go/bin"`.

**`go.mod` complaints under Go 1.23** — bump to Go 1.24+; `go.mod`
pins `go 1.24` with a `toolchain go1.24.x` directive.

## Security notes

- **Slug validation.** `thicket rm <slug>` rejects absolute paths,
  `..`, and any value containing path separators before joining with
  `workspace_root`, so `thicket rm /tmp` or `thicket rm ../..` can't
  escape into arbitrary directories.
- **Manifest-required deletes.** When a target directory has no
  `.thicket/state.json`, `Workspace.Remove` refuses unless `--force`,
  so an accidental `thicket rm foo` over a non-thicket folder won't
  blind-`rm -rf` it.
- **No raw secrets at rest.** The config records only password-manager
  references; live values are fetched on demand. The `[secrets]`
  plain-text table is rejected by this tool — pick a real PM (`op` /
  `bw` / `pass`) or use the env-var override path.
- **No telemetry.** thicket calls Anthropic / Shortcut / GitHub only
  to do its job. No phone-home, no analytics.

## Status

Pre-1.0. Tested on macOS (arm64/amd64) and Linux (amd64). Windows is not
supported in this version (the launcher uses `syscall.Exec`).

## License

MIT — see [LICENSE](./LICENSE).
