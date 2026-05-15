// Package workspace orchestrates the creation and removal of a thicket
// workspace: a directory holding one git worktree per repo selected for a
// ticket, plus a CLAUDE.local.md seed and a small state manifest used by
// `thicket list` and `thicket rm`.
package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/uribrecher/thicket/internal/git"
	"github.com/uribrecher/thicket/internal/memory"
	"github.com/uribrecher/thicket/internal/term"
)

// PlanRepo is one repo in the plan: where its source clone lives, what
// worktree directory to create for it, and whether the branch already
// exists in source (so we know whether to pass -b on git worktree add).
type PlanRepo struct {
	Name         string
	SourcePath   string
	WorktreePath string
	BranchExists bool
}

// Plan describes the workspace to materialize.
type Plan struct {
	WorkspaceDir string
	Branch       string
	// Nickname is a short, human-friendly label (max 20 chars,
	// spaces and emoji allowed, uniqueness not required). Optional —
	// when empty, display sites fall back to the workspace slug.
	Nickname string
	// Color is the workspace's tab-color hint, hex `#RRGGBB`. Used
	// by iTerm2 to tint the tab background so concurrent workspace
	// sessions are visually distinguishable. Optional — when empty,
	// the launcher leaves the tab uncolored. Sanitized at the
	// persistence boundary; invalid input is dropped to "".
	Color  string
	Repos  []PlanRepo
	Memory memory.Input

	// Progress, when non-nil, receives one line per materialization
	// step (`✓ worktree: …`, `✓ wrote CLAUDE.local.md (…)`, etc.).
	// Lets the CLI stream feedback during a multi-second `Create`
	// without leaking presentation concerns into the workspace
	// package. Leave nil for silent operation (tests, scripts).
	Progress io.Writer
}

// State is the persisted manifest written into <workspace>/.thicket/state.json.
// It lets `thicket rm` clean up worktrees without scanning every repo.
type State struct {
	TicketID string `json:"ticket_id"`
	Branch   string `json:"branch"`
	// Nickname is the per-workspace display label set at creation
	// time. `omitempty` so manifests written before this field
	// existed round-trip cleanly.
	Nickname string `json:"nickname,omitempty"`
	// Color is the tab-color hint, hex `#RRGGBB`. iTerm2 uses it to
	// tint the tab background at session start. `omitempty` —
	// manifests without a color decode as "".
	Color     string      `json:"color,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
	Repos     []StateRepo `json:"repos"`
}

type StateRepo struct {
	Name         string `json:"name"`
	SourcePath   string `json:"source_path"`
	WorktreePath string `json:"worktree_path"`
}

// Workspace exposes Create + Remove against a git.Git.
type Workspace struct {
	Git *git.Git
}

func New(g *git.Git) *Workspace { return &Workspace{Git: g} }

// ErrExists is returned by Create when the target directory already exists.
var ErrExists = errors.New("workspace directory already exists")

// Create materializes the plan. On any failure it best-effort rolls back
// previously created worktrees and the workspace directory.
func (w *Workspace) Create(p Plan) error {
	if p.WorkspaceDir == "" {
		return errors.New("workspace dir is required")
	}
	if p.Branch == "" {
		return errors.New("branch is required")
	}
	if len(p.Repos) == 0 {
		return errors.New("plan has no repos")
	}
	if _, err := os.Stat(p.WorkspaceDir); err == nil {
		return fmt.Errorf("%w: %s", ErrExists, p.WorkspaceDir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat workspace: %w", err)
	}
	if err := os.MkdirAll(p.WorkspaceDir, 0o755); err != nil {
		return fmt.Errorf("create workspace dir: %w", err)
	}

	created := make([]PlanRepo, 0, len(p.Repos))
	rollback := func() {
		// Best-effort: remove worktrees, then the workspace dir.
		for _, r := range created {
			_ = w.Git.RemoveWorktree(r.SourcePath, r.WorktreePath, true)
		}
		_ = os.RemoveAll(p.WorkspaceDir)
	}

	for _, r := range p.Repos {
		if err := w.Git.AddWorktree(r.SourcePath, r.WorktreePath, p.Branch,
			!r.BranchExists); err != nil {
			rollback()
			return fmt.Errorf("add worktree for %s: %w", r.Name, err)
		}
		created = append(created, r)
		progressf(p.Progress, "%s worktree: %s\n", checkMark, r.Name)
	}

	// Render and write CLAUDE.local.md
	if p.Memory.CreatedAt.IsZero() {
		p.Memory.CreatedAt = time.Now()
	}
	body, err := memory.Render(p.Memory)
	if err != nil {
		rollback()
		return fmt.Errorf("render memory: %w", err)
	}
	memPath := filepath.Join(p.WorkspaceDir, memory.FileName)
	if err := os.WriteFile(memPath, body, 0o644); err != nil {
		rollback()
		return fmt.Errorf("write %s: %w", memory.FileName, err)
	}
	progressf(p.Progress, "%s wrote %s (ticket context, %s)\n",
		checkMark, memory.FileName, p.Memory.TicketID)

	// Write the state manifest
	if err := writeState(p); err != nil {
		rollback()
		return fmt.Errorf("write state: %w", err)
	}
	progressf(p.Progress, "%s wrote .thicket/state.json (manifest for `thicket rm`)\n",
		checkMark)
	return nil
}

// Remove tears down the workspace at workspaceDir by removing every
// worktree listed in its state manifest, then the directory itself.
// force=true tolerates dirty worktrees AND lets the caller delete a
// directory that has no state manifest (i.e. wasn't created by thicket
// or had its manifest deleted) — see safety note below. progress, if
// non-nil, receives human-readable status lines as the teardown
// proceeds:
//
//   - per worktree: a ✓-prefixed line on success (followed by a
//     continuation line listing the source repo), or a ✗-prefixed
//     line on failure;
//   - a final ✓ for the workspace directory delete, OR a
//     "(workspace directory preserved — re-run with --force …)"
//     note if any worktree removal failed.
//
// Pass nil for silent operation (tests, scripts).
//
// Safety:
//   - If any worktree refuses to be removed (e.g. dirty changes with
//     force=false), the workspace directory is NOT deleted. Otherwise
//     we'd silently destroy uncommitted work while leaving stale
//     worktree metadata in the source repos.
//   - If the state manifest is missing, Remove refuses to delete the
//     directory unless force=true. This stops `thicket rm` from
//     becoming a blind `rm -rf` against any folder that happens to
//     live under workspace_root.
func (w *Workspace) Remove(workspaceDir string, force bool, progress io.Writer) error {
	st, err := ReadState(workspaceDir)
	if err != nil {
		if errors.Is(err, ErrNoState) {
			return w.removeNoManifest(workspaceDir, force, progress)
		}
		return err
	}
	return w.removeWithState(workspaceDir, st, force, progress)
}

// RemoveWithState is the optimized entry point for callers that already
// loaded the manifest (e.g. via ListManaged) and want to avoid a second
// ReadState. Pass nil for st to indicate "no manifest" — Remove's
// safety semantics still apply (refuses unless force=true). progress
// has the same meaning as in Remove.
func (w *Workspace) RemoveWithState(workspaceDir string, st *State, force bool, progress io.Writer) error {
	if st == nil {
		return w.removeNoManifest(workspaceDir, force, progress)
	}
	return w.removeWithState(workspaceDir, *st, force, progress)
}

func (w *Workspace) removeNoManifest(workspaceDir string, force bool, progress io.Writer) error {
	if !force {
		return fmt.Errorf(
			"%w at %s — refusing to delete (use --force to override)",
			ErrNoState, workspaceDir)
	}
	// force=true: explicit operator opt-in for legacy / orphaned
	// directories. Just nuke the directory.
	if err := os.RemoveAll(workspaceDir); err != nil {
		return err
	}
	progressf(progress, "%s deleted workspace directory: %s (no manifest, --force)\n",
		checkMark, workspaceDir)
	return nil
}

func (w *Workspace) removeWithState(workspaceDir string, st State, force bool, progress io.Writer) error {
	var firstErr error
	for _, r := range st.Repos {
		if err := w.Git.RemoveWorktree(r.SourcePath, r.WorktreePath, force); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("remove worktree %s: %w", r.Name, err)
			}
			progressf(progress, "%s could not remove worktree %s: %v\n", crossMark, r.Name, err)
			// keep going — best effort
			continue
		}
		progressf(progress, "%s removed worktree %s\n    from source repo %s\n",
			checkMark, r.Name, r.SourcePath)
	}
	if firstErr != nil {
		// Preserve the workspace dir so the user's uncommitted changes
		// survive. Re-run with --force after triaging.
		progressf(progress, "(workspace directory preserved — re-run with --force after fixing the worktrees above)\n")
		return firstErr
	}
	if err := os.RemoveAll(workspaceDir); err != nil {
		return err
	}
	progressf(progress, "%s deleted workspace directory: %s\n", checkMark, workspaceDir)
	return nil
}

// AddPlan describes the worktrees `thicket edit` is about to attach
// to an already-materialized workspace. The branch is the existing
// workspace's branch (read from state.json) — Add does not change the
// workspace's branch. Memory carries the refreshed ticket header used
// to regenerate CLAUDE.local.md; the post-add file will splice in the
// existing Status log via memory.RegenPreservingStatusLog so the
// agent's prior progress notes survive.
type AddPlan struct {
	WorkspaceDir string
	NewRepos     []PlanRepo
	Memory       memory.Input // refreshed header + the FULL repo set (old+new)
	Progress     io.Writer    // optional, see Plan.Progress
}

// AddResult reports how Add went: which adds succeeded, which were
// skipped (one entry per failure with the underlying error), and the
// final state manifest that was written.
type AddResult struct {
	Added   []PlanRepo
	Skipped []AddSkip
	Final   State
}

// AddSkip is one repo Add couldn't attach, with the reason.
type AddSkip struct {
	Name   string
	Reason error
}

// Add attaches NewRepos to an existing workspace. Per-repo failure is
// non-fatal (proceed-without-failed-repo policy mirrors start). The
// state manifest is rewritten atomically only AFTER all attempts —
// partial success rewrites with what landed; total failure leaves the
// manifest as-is. CLAUDE.local.md regen runs at the end with the
// final repo set; a Status log preserve failure is logged via
// Progress but doesn't fail Add.
func (w *Workspace) Add(p AddPlan) (AddResult, error) {
	if p.WorkspaceDir == "" {
		return AddResult{}, errors.New("workspace dir is required")
	}
	if len(p.NewRepos) == 0 {
		return AddResult{}, errors.New("AddPlan has no new repos")
	}
	if _, err := os.Stat(p.WorkspaceDir); err != nil {
		return AddResult{}, fmt.Errorf("stat workspace: %w", err)
	}
	st, err := ReadState(p.WorkspaceDir)
	if err != nil {
		return AddResult{}, fmt.Errorf("read state: %w", err)
	}
	// Reject duplicates up front — a repo already in state shouldn't
	// be passed in. The wizard's locked-row UX prevents this; the
	// guard here is a safety net for non-wizard callers.
	have := make(map[string]bool, len(st.Repos))
	for _, r := range st.Repos {
		have[r.Name] = true
	}
	for _, r := range p.NewRepos {
		if have[r.Name] {
			return AddResult{}, fmt.Errorf("repo %s is already in this workspace", r.Name)
		}
	}

	var res AddResult
	for _, r := range p.NewRepos {
		err := w.Git.AddWorktree(r.SourcePath, r.WorktreePath, st.Branch, !r.BranchExists)
		if err != nil {
			progressf(p.Progress, "%s could not add worktree %s: %v\n", crossMark, r.Name, err)
			res.Skipped = append(res.Skipped, AddSkip{Name: r.Name, Reason: err})
			continue
		}
		progressf(p.Progress, "%s worktree: %s\n", checkMark, r.Name)
		res.Added = append(res.Added, r)
		st.Repos = append(st.Repos, StateRepo{
			Name:         r.Name,
			SourcePath:   r.SourcePath,
			WorktreePath: r.WorktreePath,
		})
	}

	if len(res.Added) == 0 {
		// Nothing landed — don't touch the manifest or the memory file.
		return res, errors.New("no worktrees could be added (see ✗ lines above)")
	}

	// Refresh CLAUDE.local.md, preserving the existing Status log if
	// one is there. A regen failure does NOT roll back the worktrees
	// we just added — the agent's view of the world being slightly
	// stale is preferable to dangling worktrees that survive a
	// re-run.
	memPath := filepath.Join(p.WorkspaceDir, memory.FileName)
	existing, readErr := os.ReadFile(memPath)
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		progressf(p.Progress, "%s could not read existing %s: %v (worktrees still attached)\n",
			crossMark, memory.FileName, readErr)
	} else {
		body, preserved, regenErr := memory.RegenPreservingStatusLog(p.Memory, existing)
		switch {
		case regenErr != nil:
			progressf(p.Progress, "%s could not refresh %s: %v (worktrees still attached)\n",
				crossMark, memory.FileName, regenErr)
		case len(existing) > 0 && !preserved:
			progressf(p.Progress, "(refreshed %s; could not locate `## Status log` in the existing file, prior notes may have been overwritten)\n",
				memory.FileName)
			if writeErr := os.WriteFile(memPath, body, 0o644); writeErr != nil {
				progressf(p.Progress, "%s could not write %s: %v\n", crossMark, memory.FileName, writeErr)
			}
		default:
			if writeErr := os.WriteFile(memPath, body, 0o644); writeErr != nil {
				progressf(p.Progress, "%s could not write %s: %v\n", crossMark, memory.FileName, writeErr)
			} else {
				progressf(p.Progress, "%s refreshed %s\n", checkMark, memory.FileName)
			}
		}
	}

	// Atomic manifest rewrite reflects every successful add.
	if err := writeStateAtomic(p.WorkspaceDir, st); err != nil {
		return res, fmt.Errorf("write state: %w", err)
	}
	progressf(p.Progress, "%s updated .thicket/state.json (now %d worktree(s))\n",
		checkMark, len(st.Repos))
	res.Final = st
	return res, nil
}

// Progress glyphs. Inline constants so a future "ascii-only" mode
// is a one-line flip.
const (
	checkMark = "✓"
	crossMark = "✗"
)

// progressf is a nil-safe Fprintf — drops silently when the caller
// passes nil (tests + scripts get no chatter).
func progressf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, format, args...)
}

// ManagedWorkspace is a directory under workspace_root that has a
// thicket state manifest.
type ManagedWorkspace struct {
	Slug  string
	Path  string
	State State
}

// DisplayName returns the workspace's nickname when set, falling back
// to the slug. Use in human-facing UI columns and prompts; never use
// for filesystem paths or unique keys (the slug is the only safe
// identifier on disk).
func (m ManagedWorkspace) DisplayName() string {
	if m.State.Nickname != "" {
		return m.State.Nickname
	}
	return m.Slug
}

// NicknameMaxChars is the upper bound on a workspace nickname in
// runes. Enforced at persistence time by SanitizeNickname; the
// wizard's textinput and the LLM prompt also reference it so the
// rule lives in exactly one place.
//
// 25 runes balances "fits in a tab strip" with "leaves room for a
// short emoji-prefixed acronym phrase like '🐛 MR Snowflake enum'".
const NicknameMaxChars = 25

// SanitizeNickname normalizes a candidate nickname before persistence
// or display: trims surrounding whitespace, replaces interior runs of
// any whitespace (incl. tab/newline) with a single ASCII space, drops
// control characters and other non-printables (including ANSI
// escapes), and truncates the result at NicknameMaxChars runes
// (preserving multi-byte runes like emoji intact).
//
// Inputs originate from three sources — the LLM suggester, the
// `--nickname` CLI flag, and the Plan-page textinput. The textinput
// already enforces a length cap; the other two come in unfiltered.
// Calling this at the persistence boundary in writeState means the
// rule applies regardless of caller, and downstream display code can
// treat the field as already-sanitized.
func SanitizeNickname(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	count := 0
	for _, r := range s {
		// Cap check up top so we never write past the limit — a
		// whitespace push at exactly the cap would otherwise let a
		// trailing rune sneak in.
		if count >= NicknameMaxChars {
			break
		}
		// Collapse any whitespace (incl. \t \n \r) into a single
		// space — the persisted nickname must be a single-line label.
		if unicode.IsSpace(r) {
			if !prevSpace && count > 0 {
				b.WriteByte(' ')
				count++
			}
			prevSpace = true
			continue
		}
		// Drop control characters (Unicode category Cc, e.g. ANSI
		// escape 0x1b, NUL, backspace) and other non-printables.
		// IsPrint covers letters/digits/punct/symbols across the
		// whole Unicode range, so emoji survive.
		if !unicode.IsPrint(r) {
			prevSpace = false
			continue
		}
		b.WriteRune(r)
		count++
		prevSpace = false
	}
	// Trailing space cleanup (we may have emitted one right before
	// the truncate cutoff).
	return strings.TrimRight(b.String(), " ")
}

// ListManaged enumerates thicket-managed workspaces under root, newest
// first by CreatedAt. Three return values keep the failure modes
// distinct:
//
//   - workspaces:  the usable entries
//   - warnings:    per-manifest errors (corrupt/unreadable state files
//     for individual workspaces). The caller should surface
//     these but continue.
//   - err:         a fatal failure to read root itself (permission
//     denied, etc.). Workspaces is nil; the caller should
//     stop. A missing root is NOT an error — that's a
//     fresh install / no-workspaces-yet state.
//
// Entries with no state manifest (.thicket/state.json missing) are
// skipped silently — those aren't thicket workspaces.
func ListManaged(root string) ([]ManagedWorkspace, []error, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read %s: %w", root, err)
	}
	var out []ManagedWorkspace
	var warnings []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ws := filepath.Join(root, e.Name())
		st, err := ReadState(ws)
		switch {
		case errors.Is(err, ErrNoState):
			continue
		case err != nil:
			warnings = append(warnings, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		out = append(out, ManagedWorkspace{Slug: e.Name(), Path: ws, State: st})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].State.CreatedAt.After(out[j].State.CreatedAt)
	})
	return out, warnings, nil
}

// FindContainingWorkspace returns the managed workspace whose
// directory contains `cwd`. Used by `thicket start` to detect "the
// user is already cd'd inside a workspace" and skip the ticket
// picker. Returns ErrNoState when cwd is not under `root`, is `root`
// itself, or is under `root` in a directory that doesn't carry a
// `.thicket/state.json` manifest.
//
// Symlinks are resolved on both inputs so a tmpfs/home symlink can't
// defeat the prefix check. The returned `Path` uses the user-facing
// `root` (not the symlink-resolved one) so the workspace dir we hand
// back is the path the user would type themselves.
func FindContainingWorkspace(root, cwd string) (ManagedWorkspace, error) {
	if root == "" || cwd == "" {
		return ManagedWorkspace{}, ErrNoState
	}
	resolvedRoot := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		resolvedRoot = r
	}
	resolvedCwd := cwd
	if c, err := filepath.EvalSymlinks(cwd); err == nil {
		resolvedCwd = c
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedCwd)
	if err != nil {
		return ManagedWorkspace{}, ErrNoState
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return ManagedWorkspace{}, ErrNoState
	}
	slug, _, _ := strings.Cut(rel, string(filepath.Separator))
	if slug == "" {
		return ManagedWorkspace{}, ErrNoState
	}
	wsDir := filepath.Join(root, slug)
	st, err := ReadState(wsDir)
	if errors.Is(err, ErrNoState) {
		return ManagedWorkspace{}, ErrNoState
	}
	if err != nil {
		// Corrupt manifest or permission error — distinct from
		// "no thicket workspace here". Surface so the caller can
		// warn rather than silently falling through to the wizard.
		return ManagedWorkspace{}, fmt.Errorf("workspace %s: %w", slug, err)
	}
	return ManagedWorkspace{Slug: slug, Path: wsDir, State: st}, nil
}

// ----- state manifest -----

// ErrNoState is returned by ReadState when the state file is missing.
var ErrNoState = errors.New("no state manifest")

func writeState(p Plan) error {
	st := State{
		TicketID: p.Memory.TicketID,
		Branch:   p.Branch,
		// Sanitize at the persistence boundary: any caller (wizard,
		// legacy --nickname flag, future API) writes through here, so
		// downstream display code can trust the field is already
		// normalized.
		Nickname:  SanitizeNickname(p.Nickname),
		Color:     term.SanitizeHexColor(p.Color),
		CreatedAt: p.Memory.CreatedAt,
		Repos:     make([]StateRepo, 0, len(p.Repos)),
	}
	for _, r := range p.Repos {
		st.Repos = append(st.Repos, StateRepo{
			Name:         r.Name,
			SourcePath:   r.SourcePath,
			WorktreePath: r.WorktreePath,
		})
	}
	return writeStateAtomic(p.WorkspaceDir, st)
}

// writeStateAtomic serializes st into <workspaceDir>/.thicket/state.json
// via a temp-write-then-rename so a crash mid-write can't corrupt the
// manifest. `thicket edit` requires this — appending a repo to an
// already-live workspace must not be able to leave the user with a
// half-written manifest and orphaned worktrees.
func writeStateAtomic(workspaceDir string, st State) error {
	dir := filepath.Join(workspaceDir, ".thicket")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	final := filepath.Join(dir, "state.json")
	tmp, err := os.CreateTemp(dir, "state.*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if anything below fails after CreateTemp.
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, final)
}

// ReadState loads the manifest for a workspace directory.
func ReadState(workspaceDir string) (State, error) {
	b, err := os.ReadFile(filepath.Join(workspaceDir, ".thicket", "state.json"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return State{}, ErrNoState
		}
		return State{}, fmt.Errorf("read state: %w", err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("parse state: %w", err)
	}
	return st, nil
}

// Slug returns the canonical workspace directory name for a ticket.
// Format: "<lowercase-ticket-id>-<slugified-title>". Always carries the
// ticket id so two tickets with the same title (e.g. "freshness") don't
// collide on disk. The branch name is intentionally NOT used here —
// Shortcut and other sources sometimes produce branch names that omit
// the ticket id (e.g. "uri/freshness"), and we don't want the workspace
// folder to inherit that fragility.
func Slug(ticketID, title string) string {
	id := strings.ToLower(strings.TrimSpace(ticketID))
	t := Slugify(title)
	switch {
	case id == "" && t == "":
		return "workspace"
	case id == "":
		return t
	case t == "":
		return id
	}
	return id + "-" + t
}

// Slugify converts free-form text to a lowercase, hyphen-separated
// identifier suitable for filenames and branch names.
func Slugify(s string) string {
	var b strings.Builder
	prev := '-'
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prev = r
		case r == ' ' || r == '-' || r == '_' || r == '/' || r == '\t':
			if prev != '-' {
				b.WriteRune('-')
				prev = '-'
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
