# Contributing to thicket

## Prerequisites

- Go (see `go.mod` for the version) and [Task](https://taskfile.dev) (`task --list` shows everything).
- [changie](https://changie.dev) for changelog management:
  ```
  go install github.com/miniscruff/changie@latest
  ```
  Make sure `$(go env GOPATH)/bin` is on your `PATH`.

## Changelog

`CHANGELOG.md` is **generated** by changie — don't hand-edit it, and don't add an
`## [Unreleased]` section. Instead, every user-facing change ships a small
**fragment** describing it. This is what removes the old manual "cut the changelog"
step and the merge conflicts that came with everyone editing the same `[Unreleased]`
block.

Add a fragment as part of your change:

```
task changelog:new      # interactive: pick a kind, type the entry
```

This writes a YAML file under `.changes/unreleased/`. Commit it alongside your code.

- **Kinds:** `Added`, `Changed`, `Fixed`, `Internal`.
- **Body:** the same prose you'd have written as a `CHANGELOG.md` bullet — a bold
  lead sentence, then the details. Wrap at ~72 columns with a 2-space continuation
  indent to match the house style. The `- ` bullet prefix is added for you.
- Pending changes don't appear in `CHANGELOG.md` until a release is cut — they live
  as fragment files. Preview how they'll render at any time:
  ```
  task changelog:preview
  ```

## Cutting a release

Releases are automated — you do not run `changie batch`/`merge` or push tags by hand.

1. As soon as any fragment lands on `main`, the **`release-pr.yaml`** bot opens (or updates) a
   standing **`Release vX.Y.Z`** PR: it batches the pending fragments, regenerates `CHANGELOG.md`,
   and drafts a one-paragraph intro via GitHub Models.
2. When you're ready to ship, review that PR. Optionally tweak the intro by pushing a commit to its
   `automated/release` branch (your edit survives later bot updates as long as the version number
   doesn't change). Approve and **squash-merge** it.

   > The bot opens the PR with the built-in `GITHUB_TOKEN`, so the required status checks
   > (`test (ubuntu-latest)`, `test (macos-latest)`, `lint`, `check`) don't run on it. The PR's
   > diff is changelog-only — it can't affect
   > the Go build, lint, or cache-bust check — so merge it using your maintainer bypass ("merge
   > without waiting for requirements"). The underlying code already passed CI on its own feature PR.
3. Merging triggers **`release-publish.yaml`**, which tags `vX.Y.Z` and runs GoReleaser to publish
   the GitHub Release.

The version bump is chosen automatically from the fragment kinds (any `Added`/`Changed` → minor,
only `Fixed`/`Internal` → patch).

### Repository setup (one-time)

The release-PR bot needs **Settings → Actions → General → Workflow permissions → "Allow GitHub
Actions to create and approve pull requests"** enabled. Without it, `release-pr.yaml` runs to the
end but fails the final create-PR step — the error is
`GitHub Actions is not permitted to create or approve pull requests`, though it still pushes the
`automated/release` branch (there's just no PR). Already enabled for this repo.

### Manual release (escape hatch)

You can still cut a release by hand if needed: `task changelog:batch -- <version>`,
`task changelog:merge`, commit, then `git tag vX.Y.Z && git push origin vX.Y.Z` — the unchanged
`release.yaml` publishes on the tag push.

### GitHub Models

The intro draft uses GitHub Models via the built-in token (no API key). If Models is not enabled
for the account, the intro is simply skipped and the release proceeds.
