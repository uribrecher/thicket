# Release automation (changie release-PR bot) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the manual release cut with a Changesets-style bot — an always-current "Release vX.Y.Z" PR that publishes when merged — using only the built-in `GITHUB_TOKEN`.

**Architecture:** A `release-pr.yaml` workflow watches `main`; when fragments are pending it runs `changie batch`/`merge`, drafts a one-paragraph intro via GitHub Models (reuse-first, auto-skip on failure), and opens/updates the release PR. A `release-publish.yaml` workflow runs on the release-PR merge, tags the version, and runs GoReleaser in the same job. Existing CI workflows are extended to also run on the bot branch so the four required checks attach to the PR (no PAT). The only non-trivial logic — preserving/injecting the intro — lives in a unit-tested Python helper.

**Tech Stack:** GitHub Actions, changie, GoReleaser, GitHub Models (`actions/ai-inference`), `peter-evans/create-pull-request`, Python 3 stdlib (helper + tests), Go 1.24.

## Global Constraints

- **Zero secrets / no PAT or App token.** Every workflow uses only the built-in `GITHUB_TOKEN`. GitHub Models is reached via `permissions: models: read` on that same token.
- **The four required status-check contexts are exactly:** `test (ubuntu-latest)`, `test (macos-latest)`, `lint`, `check`. Job names must not change.
- **`main` ruleset:** squash-only merges, 1 approving review, review-thread resolution, signed commits (satisfied by the GitHub-signed squash-merge commit), Copilot review on push. Ruleset targets `~DEFAULT_BRANCH` only — the bot branch `automated/release` is unconstrained.
- **changie version files are `v<MAJOR.MINOR.PATCH>.md`; changelog headers are bare `## [X.Y.Z]`.** Released history is frozen in `.changes/v0.9.6.md` — never regenerate it.
- **The bot branch is `automated/release`.** Intermediate files (prompt, intro) must be written under `$RUNNER_TEMP`, never inside the repo, so they are not committed to that branch.
- **Go 1.24**; Python is stdlib-only (no pip installs — repo already uses `python3` for `scripts/cachebust.py`).

---

### Task 1: `changelog_intro.py` helper (extract + inject)

The only non-trivial, unit-testable logic: read or replace the intro paragraph in a single-version changie file. A version file is `## [V] - DATE`, an optional intro, then `### Kind` sections. The intro is the text between the header line and the first `### ` line.

**Files:**
- Create: `scripts/changelog_intro.py`
- Create: `scripts/test_changelog_intro.py`
- Modify: `Taskfile.yaml` (add a `test:scripts` task)

**Interfaces:**
- Produces (consumed by Task 3's workflow):
  - CLI `python3 scripts/changelog_intro.py extract <version-file>` → prints the current intro (empty string if none).
  - CLI `python3 scripts/changelog_intro.py inject <version-file> <intro-file>` → rewrites `<version-file>` in place, inserting/replacing the intro after the `## [..]` header (no-op paragraph if the intro file is empty). Idempotent.
  - Python API: `extract(text: str) -> str`, `inject(text: str, intro: str) -> str`.

- [ ] **Step 1: Write the failing tests**

Create `scripts/test_changelog_intro.py`:

```python
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import changelog_intro as ci

NO_INTRO = "## [0.10.0] - 2026-06-21\n\n### Added\n\n- A feature.\n"
WITH_INTRO = "## [0.10.0] - 2026-06-21\n\nA short intro paragraph.\n\n### Added\n\n- A feature.\n"


class ExtractTests(unittest.TestCase):
    def test_no_intro_returns_empty(self):
        self.assertEqual(ci.extract(NO_INTRO), "")

    def test_with_intro_returns_text(self):
        self.assertEqual(ci.extract(WITH_INTRO), "A short intro paragraph.")

    def test_missing_header_raises(self):
        with self.assertRaises(ValueError):
            ci.extract("no version header here\n")


class InjectTests(unittest.TestCase):
    def test_inject_into_no_intro(self):
        self.assertEqual(ci.inject(NO_INTRO, "A short intro paragraph."), WITH_INTRO)

    def test_inject_replaces_existing_intro(self):
        replaced = ci.inject(WITH_INTRO, "Brand new intro.")
        self.assertEqual(ci.extract(replaced), "Brand new intro.")
        self.assertEqual(replaced.count("###"), 1)

    def test_inject_empty_intro_is_noop_shape(self):
        self.assertEqual(ci.inject(NO_INTRO, ""), NO_INTRO)

    def test_roundtrip_preserves_intro(self):
        self.assertEqual(ci.inject(WITH_INTRO, ci.extract(WITH_INTRO)), WITH_INTRO)


if __name__ == "__main__":
    unittest.main()
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `python3 -m unittest discover -s scripts -p 'test_*.py' -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'changelog_intro'`.

- [ ] **Step 3: Write the implementation**

Create `scripts/changelog_intro.py`:

```python
#!/usr/bin/env python3
"""Extract or inject the intro paragraph of a single-version changie file.

A version file looks like:

    ## [0.10.0] - 2026-06-21

    <intro paragraph>

    ### Added

    - ...

The intro is the text between the `## [` header line and the first
`### ` kind header. Used by release-pr.yaml to preserve a hand-edited
intro across bot re-runs and to inject a freshly drafted one.
"""
import sys


def _split(lines):
    """Return (header_idx, kind_idx): the `## [` line index and the index
    of the first `### ` line (len(lines) if there is none)."""
    header_idx = None
    for i, line in enumerate(lines):
        if line.startswith("## ["):
            header_idx = i
            break
    if header_idx is None:
        raise ValueError("no version header (`## [`) found")
    kind_idx = len(lines)
    for i in range(header_idx + 1, len(lines)):
        if lines[i].startswith("### "):
            kind_idx = i
            break
    return header_idx, kind_idx


def extract(text):
    lines = text.splitlines()
    header_idx, kind_idx = _split(lines)
    return "\n".join(lines[header_idx + 1:kind_idx]).strip()


def inject(text, intro):
    lines = text.splitlines()
    header_idx, kind_idx = _split(lines)
    header = lines[header_idx]
    rest = lines[kind_idx:]
    intro = intro.strip()
    block = [header, "", intro, ""] if intro else [header, ""]
    return "\n".join(block + rest).rstrip("\n") + "\n"


def main(argv):
    if len(argv) < 3:
        sys.exit("usage: changelog_intro.py {extract|inject} <version-file> [intro-file]")
    cmd, version_file = argv[1], argv[2]
    with open(version_file) as f:
        text = f.read()
    if cmd == "extract":
        print(extract(text))
    elif cmd == "inject":
        if len(argv) < 4:
            sys.exit("inject requires <intro-file>")
        with open(argv[3]) as f:
            intro = f.read()
        with open(version_file, "w") as f:
            f.write(inject(text, intro))
    else:
        sys.exit(f"unknown command: {cmd}")


if __name__ == "__main__":
    main(sys.argv)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `python3 -m unittest discover -s scripts -p 'test_*.py' -v`
Expected: PASS (7 tests OK).

- [ ] **Step 5: Add a Taskfile task to run the script tests**

In `Taskfile.yaml`, add under `tasks:` (place it just after the existing `test:race:` task):

```yaml
  test:scripts:
    desc: Run the Python helper-script unit tests (stdlib unittest)
    cmds:
      - python3 -m unittest discover -s scripts -p 'test_*.py' -v
```

- [ ] **Step 6: Verify the task runs and commit**

Run: `task test:scripts`
Expected: PASS.

```bash
git add scripts/changelog_intro.py scripts/test_changelog_intro.py Taskfile.yaml
git commit -m "feat(release): add changelog intro extract/inject helper"
```

---

### Task 2: CI bridge — run required checks on the bot branch

A PR opened by `GITHUB_TOKEN` does not trigger `pull_request` workflows, so the four required checks would never run on the release PR. Make them run on `push` to `automated/release` instead; same job names → same required-check contexts → they attach to the PR.

**Files:**
- Modify: `.github/workflows/ci.yaml:3-7` (the `on:` block)
- Modify: `.github/workflows/cachebust-check.yaml:16-17` (the `on:` block)

**Interfaces:**
- Produces: the contexts `test (ubuntu-latest)`, `test (macos-latest)`, `lint`, `check` now also run on pushes to `automated/release`. No new outputs.

- [ ] **Step 1: Extend `ci.yaml` triggers**

Replace the `on:` block in `.github/workflows/ci.yaml` (currently lines 3-7):

```yaml
on:
  push:
    branches: [main, automated/release]
  pull_request:
    branches: [main]
```

- [ ] **Step 2: Extend `cachebust-check.yaml` triggers**

Replace the `on:` block in `.github/workflows/cachebust-check.yaml` (currently lines 16-17):

```yaml
on:
  pull_request:
  push:
    branches: [automated/release]
```

- [ ] **Step 3: Validate both workflows parse**

Run:
```bash
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/ci.yaml')); yaml.safe_load(open('.github/workflows/cachebust-check.yaml')); print('ok')"
```
Expected: `ok`

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/ci.yaml .github/workflows/cachebust-check.yaml
git commit -m "ci: run required checks on the automated/release branch"
```

---

### Task 3: `release-pr.yaml` — maintain the release PR

**Files:**
- Create: `.github/workflows/release-pr.yaml`

**Interfaces:**
- Consumes: `scripts/changelog_intro.py` (`extract`, `inject`) from Task 1; the `automated/release` branch checks from Task 2.
- Produces: a branch `automated/release` and an open PR titled `Release vX.Y.Z` whenever `.changes/unreleased/` is non-empty on `main`.

- [ ] **Step 1: Create the workflow**

Create `.github/workflows/release-pr.yaml`:

```yaml
name: release-pr

on:
  push:
    branches: [main]
  workflow_dispatch:

permissions:
  contents: write
  pull-requests: write
  models: read

concurrency:
  group: release-pr
  cancel-in-progress: true

jobs:
  release-pr:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - uses: actions/setup-go@v5
        with:
          go-version: "1.24"
          cache: true

      - name: Install changie
        run: go install github.com/miniscruff/changie@latest

      - name: Check for pending fragments
        id: pending
        run: |
          if ls .changes/unreleased/*.yaml >/dev/null 2>&1; then
            echo "has=true" >> "$GITHUB_OUTPUT"
          else
            echo "has=false" >> "$GITHUB_OUTPUT"
            echo "No unreleased fragments; nothing to release."
          fi

      - name: Compute next version
        if: steps.pending.outputs.has == 'true'
        id: ver
        run: echo "version=$(changie next auto)" >> "$GITHUB_OUTPUT"

      - name: Reuse an existing intro if the release PR already has one
        if: steps.pending.outputs.has == 'true'
        id: existing
        run: |
          V="${{ steps.ver.outputs.version }}"
          git fetch origin automated/release --depth=1 2>/dev/null || true
          if git show "origin/automated/release:.changes/${V}.md" > "$RUNNER_TEMP/prev.md" 2>/dev/null; then
            python3 scripts/changelog_intro.py extract "$RUNNER_TEMP/prev.md" > "$RUNNER_TEMP/intro.md"
          else
            : > "$RUNNER_TEMP/intro.md"
          fi
          if [ -s "$RUNNER_TEMP/intro.md" ]; then
            echo "reused=true" >> "$GITHUB_OUTPUT"
          else
            echo "reused=false" >> "$GITHUB_OUTPUT"
          fi

      - name: Batch fragments into the version file
        if: steps.pending.outputs.has == 'true'
        run: changie batch auto

      - name: Build the intro prompt
        if: steps.pending.outputs.has == 'true' && steps.existing.outputs.reused == 'false'
        run: |
          V="${{ steps.ver.outputs.version }}"
          {
            echo "Release ${V}. Write the intro for these change entries:"
            echo
            sed -n '/^### /,$p' ".changes/${V}.md"
          } > "$RUNNER_TEMP/prompt.txt"

      - name: Draft intro via GitHub Models
        if: steps.pending.outputs.has == 'true' && steps.existing.outputs.reused == 'false'
        id: ai
        continue-on-error: true
        uses: actions/ai-inference@v1
        with:
          model: openai/gpt-4o-mini
          system-prompt: |
            You write a one-paragraph changelog intro for the "thicket" CLI in its house voice:
            terse, 1-3 sentences, present tense, leading with the user-visible change. Output the
            paragraph only — no markdown heading, no bullet list, no surrounding quotes.
          prompt-file: ${{ runner.temp }}/prompt.txt

      - name: Save the drafted intro (or fall back to none)
        if: steps.pending.outputs.has == 'true' && steps.existing.outputs.reused == 'false'
        env:
          RESP: ${{ steps.ai.outputs.response }}
        run: |
          if [ "${{ steps.ai.outcome }}" = "success" ] && [ -n "$RESP" ]; then
            printf '%s\n' "$RESP" > "$RUNNER_TEMP/intro.md"
          else
            : > "$RUNNER_TEMP/intro.md"
            echo "GitHub Models unavailable or empty; releasing without an intro."
          fi

      - name: Inject the intro into the version file
        if: steps.pending.outputs.has == 'true'
        run: python3 scripts/changelog_intro.py inject ".changes/${{ steps.ver.outputs.version }}.md" "$RUNNER_TEMP/intro.md"

      - name: Regenerate CHANGELOG.md
        if: steps.pending.outputs.has == 'true'
        run: changie merge

      - name: Create or update the release PR
        if: steps.pending.outputs.has == 'true'
        uses: peter-evans/create-pull-request@v6
        with:
          branch: automated/release
          base: main
          title: "Release ${{ steps.ver.outputs.version }}"
          commit-message: "docs: cut CHANGELOG ${{ steps.ver.outputs.version }} section"
          delete-branch: true
          labels: release
          body: |
            Automated release PR maintained by `.github/workflows/release-pr.yaml`.

            - Rolls every pending `.changes/unreleased/` fragment into `${{ steps.ver.outputs.version }}`.
            - The intro paragraph is GitHub Models-drafted — edit it by pushing to this branch; your edit is preserved on later bot updates.
            - **Merging this PR publishes the release:** `release-publish.yaml` tags `${{ steps.ver.outputs.version }}` and runs GoReleaser.
```

- [ ] **Step 2: Validate the workflow parses**

Run: `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/release-pr.yaml')); print('ok')"`
Expected: `ok`

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/release-pr.yaml
git commit -m "feat(release): add release-pr workflow (changie batch + Models intro)"
```

---

### Task 4: `release-publish.yaml` — publish on release-PR merge

**Files:**
- Create: `.github/workflows/release-publish.yaml`

**Interfaces:**
- Consumes: the merged `.changes/v*.md` on `main`; the existing `.goreleaser.yaml`.
- Produces: a pushed git tag `vX.Y.Z` and a published GitHub Release. Idempotent — exits when the tag already exists.

> The GoReleaser step intentionally mirrors `release.yaml` (kept unchanged as the manual tag-push escape hatch). The ~12-line duplication is preferred over a reusable workflow to avoid tag-timing coupling between jobs.

- [ ] **Step 1: Verify `changie latest` output shape**

Run (locally, on this branch): `changie latest`
Expected: prints the newest batched version. Confirm it includes the `v` prefix (e.g. `v0.9.6`). If it does NOT include `v`, prefix it in Step 2 by changing `V="$(changie latest)"` to `V="v$(changie latest)"`.

- [ ] **Step 2: Create the workflow**

Create `.github/workflows/release-publish.yaml`:

```yaml
name: release-publish

on:
  push:
    branches: [main]

permissions:
  contents: write

jobs:
  publish:
    runs-on: macos-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - uses: actions/setup-go@v5
        with:
          go-version: "1.24"
          cache: true

      - name: Install changie
        run: go install github.com/miniscruff/changie@latest

      - name: Decide whether to publish
        id: gate
        run: |
          V="$(changie latest)"
          echo "version=$V" >> "$GITHUB_OUTPUT"
          git fetch --tags --quiet
          if git rev-parse -q --verify "refs/tags/$V" >/dev/null; then
            echo "publish=false" >> "$GITHUB_OUTPUT"
            echo "Tag $V already exists; nothing to publish."
          else
            echo "publish=true" >> "$GITHUB_OUTPUT"
            echo "New version $V detected; will tag and publish."
          fi

      - name: Create and push the tag
        if: steps.gate.outputs.publish == 'true'
        run: |
          V="${{ steps.gate.outputs.version }}"
          git tag "$V"
          git push origin "$V"

      - name: Publish with GoReleaser
        if: steps.gate.outputs.publish == 'true'
        uses: goreleaser/goreleaser-action@v6
        with:
          distribution: goreleaser
          version: "~> v2"
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

- [ ] **Step 3: Validate the workflow parses**

Run: `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/release-publish.yaml')); print('ok')"`
Expected: `ok`

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/release-publish.yaml
git commit -m "feat(release): add release-publish workflow (tag + GoReleaser on merge)"
```

---

### Task 5: Document the automated flow in CONTRIBUTING.md

**Files:**
- Modify: `CONTRIBUTING.md` (replace the "Cutting a release" section)

**Interfaces:** none (docs only).

- [ ] **Step 1: Replace the release section**

In `CONTRIBUTING.md`, replace the entire `## Cutting a release` section (the numbered manual steps) with:

```markdown
## Cutting a release

Releases are automated — you do not run `changie batch`/`merge` or push tags by hand.

1. As soon as any fragment lands on `main`, the **`release-pr.yaml`** bot opens (or updates) a
   standing **`Release vX.Y.Z`** PR: it batches the pending fragments, regenerates `CHANGELOG.md`,
   and drafts a one-paragraph intro via GitHub Models.
2. When you're ready to ship, review that PR. Optionally tweak the intro by pushing a commit to its
   `automated/release` branch (your edit survives later bot updates). Approve and **squash-merge**.
3. Merging triggers **`release-publish.yaml`**, which tags `vX.Y.Z` and runs GoReleaser to publish
   the GitHub Release.

The version bump is chosen automatically from the fragment kinds (any `Added`/`Changed` → minor,
only `Fixed`/`Internal` → patch).

### Manual release (escape hatch)

You can still cut a release by hand if needed: `task changelog:batch -- <version>`,
`task changelog:merge`, commit, then `git tag vX.Y.Z && git push origin vX.Y.Z` — the unchanged
`release.yaml` publishes on the tag push.

### GitHub Models

The intro draft uses GitHub Models via the built-in token (no API key). If Models is not enabled
for the account, the intro is simply skipped and the release proceeds.
```

- [ ] **Step 2: Verify the doc has no stale manual-cut instructions**

Run: `grep -n "changie batch" CONTRIBUTING.md`
Expected: the only remaining mention is under "Manual release (escape hatch)".

- [ ] **Step 3: Commit**

```bash
git add CONTRIBUTING.md
git commit -m "docs: document the automated release-PR flow"
```

---

## Acceptance test (dogfood — runs after this branch merges)

This is the integration test for the whole loop; it exercises real GitHub Actions, so it runs once the PR for this branch is merged to `main`:

1. On merge, `release-pr.yaml` runs and opens a **`Release v0.10.0`** PR containing the
   color-swatch-picker entry under `### Added` plus a Models-drafted intro. The four required checks
   (`test (ubuntu-latest)`, `test (macos-latest)`, `lint`, `check`) appear on it via the CI bridge.
2. Confirm the rendered section matches house format; optionally edit the intro on the branch.
3. Approve + squash-merge the release PR.
4. `release-publish.yaml` runs, pushes tag `v0.10.0`, and GoReleaser publishes the release with the
   `thicket` binaries and bundled `CHANGELOG.md`.

If GitHub Models is not enabled for the account, step 1 produces the PR with no intro (expected,
non-blocking) — add one by hand on the branch before merging.

## Self-review notes

- **Spec coverage:** release-pr (Task 3), release-publish (Task 4), CI bridge (Task 2), intro
  generation + preservation via GitHub Models + helper (Task 1 + Task 3), `release.yaml` left
  unchanged (noted in Task 4), docs (Task 5), dogfood (Acceptance test). All spec sections mapped.
- **Secrets:** none introduced; `models: read` is the only added permission.
- **Naming consistency:** branch `automated/release`, version var `V` from `changie next auto`
  (Task 3) / `changie latest` (Task 4), helper subcommands `extract`/`inject` used identically in
  Task 1 and Task 3.
