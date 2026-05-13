# thicket

> A thicket is a cluster of trees grown around the same root cause —
> in our case, one ticket and the worktrees it spawns.

`thicket` is a CLI that turns one ticket into an isolated, ready-to-code
multi-repo workspace. Given a Shortcut ticket id it:

1. Fetches the ticket
2. Asks Claude which repos in your GitHub org(s) need code changes
3. Lets you confirm/edit the list interactively
4. Materializes a workspace folder with one git worktree per repo on a
   ticket-named branch
5. Auto-clones any repos that aren't on your machine yet
6. Drops a `CLAUDE.local.md` into the workspace so your AI coding session
   inside it has full ticket context, across reboots and context resets
7. Launches Claude Code in the new workspace

End result: `thicket start sc-12345` → two prompts → you're coding.

> **Unofficial, community-built. Not affiliated with or endorsed by
> Anthropic.** "Claude" is a trademark of Anthropic, PBC.

## Install

### Homebrew (macOS, Linux)

```sh
brew install uribrecher/thicket/thicket
```

### Manual

Download a binary from
[the latest release](https://github.com/uribrecher/thicket/releases/latest)
and drop it on your `$PATH`.

### From source

```sh
git clone https://github.com/uribrecher/thicket
cd thicket
go install ./cmd/thicket
```

Requires Go 1.22+, `git`, and [`gh`](https://cli.github.com/) on `$PATH`.

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
thicket list                  # show active workspaces
thicket rm sc-12345-fix-x     # remove worktrees + folder
```

## How it works

```
~/code/                          # repos_root — where you keep clones
├── sentra-foo/   ← source clone, stays on main
└── sentra-bar/

~/tasks/                         # workspace_root — where thicket puts workspaces
└── sc-12345-fix-inventory/
    ├── CLAUDE.local.md          ← seeded with ticket context
    ├── .thicket/state.json      ← manifest for `thicket rm`
    ├── sentra-foo/              ← git worktree on uri/sc-12345-fix-inventory
    └── sentra-bar/              ← git worktree on uri/sc-12345-fix-inventory
```

Source clones never get checked out to a feature branch — they stay clean
on their default branch. The workspace contains only worktrees, which
share the `.git` of their source clone (storage cheap, no double-fetch).

## Configuration

`~/.config/thicket/config.toml`:

```toml
repos_root      = "~/code"
workspace_root  = "~/tasks"
default_branch  = "main"
claude_model    = "claude-haiku-4-5"
claude_binary   = "claude"

ticket_source   = "shortcut"
github_orgs     = ["your-org"]

[shortcut]
workspace_slug  = "your-shortcut-workspace"

# Optional aliases for `--only` and LLM hints
[[repo_alias]]
name    = "service-name"
aliases = ["short", "alias"]
```

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

The config looks like:

```toml
[passwords]
manager             = "1password"
shortcut_token_ref  = "op://Private/Shortcut/credential"
anthropic_key_ref   = "op://Private/Anthropic/credential"
```

`thicket doctor` verifies the CLI is installed, the vault is unlocked, and
each reference resolves to a value — without ever showing the value.

## Command reference

```
thicket init            First-run wizard.
thicket start <ticket>  Spawn a workspace.
   --only foo,bar       Skip the LLM; use exactly these repos.
   --branch <name>      Override the branch name.
   --no-interactive     Accept the LLM picks; auto-clone missing repos.
   --no-launch          Don't auto-launch claude after creating.
   --dry-run            Print the plan, change nothing on disk.

thicket list            Show active workspaces.
thicket rm <slug>       Remove a workspace + its worktrees.
   --force              Allow removing dirty worktrees.

thicket catalog         Show the GitHub-org repo cache.
   --refresh            Re-fetch via `gh`.

thicket doctor          Diagnose config, tokens, external tools.
thicket version         Print version info.
```

## Troubleshooting

**`doctor` says `claude binary not found`** — that's a warning, not an error.
Thicket still works; it just won't auto-launch your AI session and instead
prints `cd` instructions when the workspace is ready.

**`gh` 401 / 403** — run `gh auth login` and ensure the resulting token has
read access to your orgs.

**LLM picks the wrong repos** — pass `--only foo,bar` to bypass the LLM,
or just toggle in the interactive selector before confirming.

**"workspace exists"** — pick a different `--branch` or
`thicket rm <slug>` first.

**Worktree creation fails partway** — thicket rolls back already-created
worktrees and removes the workspace dir. The source repos are untouched.

## Status

Pre-1.0. Tested on macOS (arm64/amd64) and Linux (amd64). Windows is not
supported in this version (the launcher uses `syscall.Exec`).

## License

MIT — see [LICENSE](./LICENSE).
