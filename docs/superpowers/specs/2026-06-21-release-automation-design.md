# Release automation (changie release-PR bot) — design

- **Date:** 2026-06-21
- **Status:** Approved (brainstorming)
- **Issue context:** Follow-on to #59/#60 (changie adoption). Goal: remove the manual release-cut toil.

## Problem

Even after adopting changie (#60), cutting a release is still manual: branch →
`changie batch` → hand-write the intro → `changie merge` → commit → PR → tag → push.
Seven steps, every release. We want the Changesets-style **release-PR bot**: contributors
only ever add fragments; an always-current "Release vX.Y.Z" PR is maintained automatically;
**merging that PR publishes the release.** The only recurring human action is review + approve + merge.

## Decisions (locked during brainstorming)

1. **Release-PR bot**, not a one-command local script or manual workflow_dispatch.
2. **Zero secrets / zero extra git token.** Publish runs on the release-PR *merge* via the
   built-in `GITHUB_TOKEN`, so we never need a PAT/App token to bridge the tag-trigger gap.
3. **Intro paragraph drafted by GitHub Models** (first-party, `GITHUB_TOKEN` + `models: read`)
   — no `ANTHROPIC_API_KEY`. Editable on the PR. **Auto-skips** to no-intro if Models is
   unavailable or errors.
4. **Discard the in-progress manual v0.10.0 cut**; dogfood 0.10.0 through the new bot.

## Constraints discovered (the `main` ruleset)

`main` is protected by a ruleset targeting `~DEFAULT_BRANCH` only (so non-`main` branches,
including the bot's branch, are unconstrained). Relevant rules:

- **Required status checks:** `test (ubuntu-latest)`, `test (macos-latest)`, `lint`, `check`.
- **Pull request:** squash-only, 1 approving review, review-thread resolution required,
  `dismiss_stale_reviews_on_push: true`, `require_code_owner_review: true` (but **no
  CODEOWNERS file exists**, so this collapses to "1 approval").
- **Signed commits** required on `main` (satisfied: GitHub signs the squash-merge commit).
- **Copilot code review on push** (`review_on_push: true`) — auto-reviews every PR.

**Key implication:** a PR created by `GITHUB_TOKEN` does **not** trigger `pull_request`
workflows, so the four required checks would never run on the release PR and it would be
unmergeable. We solve this with the *CI bridge* below — no PAT required.

## Architecture

```
contributor PR ──merges fragment──▶ main
                                     │ (push: main)
                                     ▼
                    ┌──────────────────────────────────┐
                    │ release-pr.yaml                    │
                    │  • fragments pending? else exit    │
                    │  • V = changie next auto           │
                    │  • intro: reuse existing OR draft  │
                    │    via GitHub Models (else none)   │
                    │  • changie batch + inject + merge   │
                    │  • create/update "Release vX.Y.Z" PR│
                    └──────────────────────────────────┘
                                     │
            you: review, tweak intro, approve, squash-merge
                                     │ (push: main)
                                     ▼
                    ┌──────────────────────────────────┐
                    │ release-publish.yaml               │
                    │  • V = changie latest              │
                    │  • tag vV missing? else exit       │
                    │  • create+push tag vV (built-in tok)│
                    │  • GoReleaser publish (same run)    │
                    └──────────────────────────────────┘
```

## Components

### 1. `.github/workflows/release-pr.yaml` — maintains the release PR

- **Triggers:** `push: branches: [main]`, `workflow_dispatch`.
- **Permissions:** `contents: write`, `pull-requests: write`, `models: read`.
- **Concurrency:** `group: release-pr`, `cancel-in-progress: true`.
- **Steps:**
  1. Checkout `main` with `fetch-depth: 0`.
  2. Install changie (pinned version via `go install`, or `miniscruff/changie-action`).
  3. **Guard:** if `.changes/unreleased/` has no `*.yaml` fragments → exit 0 (no release pending).
  4. `V := changie next auto`.
  5. **Intro resolution** (see "Intro generation" below) → produces `intro.md` (possibly empty).
  6. `changie batch auto` (consumes fragments into `.changes/<V>.md` on the work branch).
  7. Inject `intro.md` into `.changes/<V>.md` via the helper script (no-op if empty).
  8. `changie merge` → regenerate `CHANGELOG.md`.
  9. `peter-evans/create-pull-request@v6`: branch `automated/release`, title `Release vX.Y.Z`,
     labels e.g. `release`, body explains "review, optionally edit the intro, merge to publish."
     Idempotent — creates or updates the standing PR.
- **Net:** every push to `main` refreshes the release PR. Fragment deletions live only on the
  bot branch; `main` keeps its fragments until the release PR merges (Changesets semantics).

### 2. `.github/workflows/release-publish.yaml` — publishes on merge

- **Trigger:** `push: branches: [main]`.
- **Permissions:** `contents: write` (create tags/releases).
- **Steps:**
  1. Checkout `main`, `fetch-depth: 0`, fetch tags.
  2. `V := changie latest` (newest batched version; fallback: highest `.changes/v*.md` filename).
  3. **Guard:** if tag `vV` already exists (local/remote) → exit 0 (idempotent; ordinary pushes
     and re-runs are no-ops).
  4. Create tag `vV`, push it with `GITHUB_TOKEN` (push succeeds; it simply won't *trigger*
     `release.yaml`, which is fine — we publish in this same run).
  5. Run GoReleaser (`goreleaser release --clean`) with `GITHUB_TOKEN` → builds + publishes the
     GitHub Release (same logic as `release.yaml`).
- **DRY option:** refactor the GoReleaser job in `release.yaml` into a reusable workflow
  (`on: workflow_call`) that both `release.yaml` (tag trigger) and `release-publish.yaml` call.
  Decided at plan time; duplication of the ~10-line job is an acceptable fallback.

### 3. CI bridge — make required checks run on the bot PR

Extend the existing workflows so the four required check contexts run on the bot branch head
(and thus attach to the release PR), letting it merge **normally** with no ruleset bypass and
no PAT:

- `ci.yaml`: add `automated/release` to `on.push.branches` (currently `[main]`). Job names
  (`test (…)`, `lint`) are unchanged, so the required-check contexts match.
- `cachebust-check.yaml`: add `on.push.branches: [automated/release]` (currently `pull_request`
  only) so the `check` context runs on the bot branch too.

`strict_required_status_checks_policy` is `false`, so the bot branch need not be kept in sync
with `main`.

### 4. `release.yaml` (existing) — unchanged manual escape hatch

Still fires on a manually pushed `v*.*.*` tag. The auto path's `GITHUB_TOKEN` tag push does
**not** re-trigger it, so there is no double-publish. Manual `git tag && git push` remains a
valid way to cut a release by hand.

## Intro generation (GitHub Models, no secret)

Goal: a one-paragraph, house-voice intro under `## [V]`, drafted once and preserving manual edits.

- **Reuse-first:** if branch `automated/release` already exists and its `.changes/<V>.md` has an
  intro (non-empty text between the `## [V]` header and the first `###`), reuse it verbatim — no
  model call. This preserves any edit you made on the PR and avoids re-drafting on every push.
- **Else draft:** call `actions/ai-inference@v1` (GitHub Models, `models: read`, built-in token)
  with the fragment bodies and a system prompt describing the house style (terse, leads with the
  user-visible change, 1–3 sentences, present tense). Output → `intro.md`.
- **Fallback:** if the action is unavailable (Models not enabled for the account) or errors, the
  step writes an empty `intro.md` and continues — the release proceeds with no intro.
- **Editable:** the drafted intro lands in the release PR; you can rewrite it before merging.

### Helper script (the testable unit)

`scripts/changelog_intro.py` (Python — repo already uses `python3` for `scripts/cachebust.py`):

- `extract <version-file>` → prints the current intro (text between `## [..]` header and first
  `### `), empty if none.
- `inject <version-file> <intro-file>` → inserts/replaces the intro paragraph just after the
  `## [..]` header, with the house single-blank-line spacing. Idempotent.

This isolates the only non-trivial logic from YAML so it can be unit-tested locally; the
workflow wires `extract` (reuse-first), the `ai-inference` step, and `inject`.

## One-time setup

- **None required for git.** No PAT, no App, no `ANTHROPIC_API_KEY`.
- **GitHub Models** must be enabled for the account for the intro to draft; if not, intros are
  simply skipped (no failure). This can be enabled later with no code change.

## Error handling / edge cases

- No fragments → `release-pr.yaml` exits cleanly; no PR churn.
- Model unavailable/errors → empty intro, release proceeds.
- `changie latest` tag already exists → `release-publish.yaml` exits (ordinary pushes are no-ops).
- GoReleaser failure → workflow fails loudly (same as today).
- Concurrent bot runs → `concurrency` group serializes; `cancel-in-progress` drops stale runs.
- Stale approval after a bot update → `dismiss_stale_reviews_on_push` re-requests approval (expected).
- Version bump changes between runs (e.g. patch→minor when an `Added` fragment lands) → the file
  is renamed to the new `.changes/<V>.md`; a previously-drafted intro is re-used if still present.

## Testing

- **Unit:** `scripts/changelog_intro.py` `extract`/`inject` round-trip and idempotency, against
  fixtures matching the house format.
- **Integration (dogfood):** land this branch → `release-pr.yaml` opens `Release v0.10.0`
  (color-picker entry + Models intro) → review → merge → `release-publish.yaml` tags `v0.10.0`
  and GoReleaser publishes. This is the acceptance test for the whole loop.

## Rollout

1. Land `feat/release-automation` (these workflows + CI-bridge edits + helper script + docs) via PR.
2. The merge to `main` triggers `release-pr.yaml`, which opens the `Release v0.10.0` PR.
3. Review/approve/merge it → `release-publish.yaml` publishes `v0.10.0` (ships the color picker).

## Out of scope (YAGNI)

- Pre-release / RC channels, multi-module versioning, changelog backfill (history stays frozen in
  `.changes/v0.9.6.md`), and any non-GitHub CI. Manual tag releases via `release.yaml` are retained
  but not enhanced.
