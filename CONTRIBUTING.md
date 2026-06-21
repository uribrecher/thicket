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

1. Roll the pending fragments into a version note (auto-picks the SemVer bump from
   the fragment kinds — any `Added`/`Changed` → minor, only `Fixed`/`Internal` →
   patch; or pass an explicit `minor` / `patch` / `0.10.0`):
   ```
   task changelog:batch -- auto
   ```
   This creates `.changes/v<version>.md` and clears `.changes/unreleased/`.
2. **(Optional but conventional)** open the new `.changes/v<version>.md` and add the
   one short intro paragraph that every release section opens with, just under the
   `## [version]` header.
3. Regenerate `CHANGELOG.md`:
   ```
   task changelog:merge
   ```
4. Commit the result (`.changes/` + `CHANGELOG.md`), e.g. `docs: cut CHANGELOG [0.10.0]`.
5. Tag and push — the `release` workflow runs GoReleaser on the tag:
   ```
   git tag v<version>
   git push origin main --tags
   ```

GoReleaser is unchanged: it still bundles `CHANGELOG.md` into the release tarball and
builds the GitHub Release notes from the commit log. The curated `CHANGELOG.md` and
the auto-generated GitHub release notes are complementary.
