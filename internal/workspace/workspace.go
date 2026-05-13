// Package workspace orchestrates the creation and removal of a thicket
// workspace: a directory holding one git worktree per repo selected for a
// ticket, plus a CLAUDE.local.md seed and a small state manifest used by
// `thicket list` and `thicket rm`.
package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/uribrecher/thicket/internal/git"
	"github.com/uribrecher/thicket/internal/memory"
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
	Repos        []PlanRepo
	Memory       memory.Input
}

// State is the persisted manifest written into <workspace>/.thicket/state.json.
// It lets `thicket rm` clean up worktrees without scanning every repo.
type State struct {
	TicketID  string      `json:"ticket_id"`
	Branch    string      `json:"branch"`
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

	// Write the state manifest
	if err := writeState(p); err != nil {
		rollback()
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// Remove tears down the workspace at workspaceDir by removing every
// worktree listed in its state manifest, then the directory itself.
// force=true tolerates dirty worktrees AND lets the caller delete a
// directory that has no state manifest (i.e. wasn't created by thicket
// or had its manifest deleted) — see safety note below.
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
func (w *Workspace) Remove(workspaceDir string, force bool) error {
	st, err := ReadState(workspaceDir)
	if err != nil {
		if errors.Is(err, ErrNoState) {
			if !force {
				return fmt.Errorf(
					"%w at %s — refusing to delete (use --force to override)",
					ErrNoState, workspaceDir)
			}
			// force=true: explicit operator opt-in for legacy / orphaned
			// directories. Just nuke the directory.
			return os.RemoveAll(workspaceDir)
		}
		return err
	}
	var firstErr error
	for _, r := range st.Repos {
		if err := w.Git.RemoveWorktree(r.SourcePath, r.WorktreePath, force); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("remove worktree %s: %w", r.Name, err)
			}
			// keep going — best effort
		}
	}
	if firstErr != nil {
		// Preserve the workspace dir so the user's uncommitted changes
		// survive. Re-run with --force after triaging.
		return firstErr
	}
	return os.RemoveAll(workspaceDir)
}

// ----- state manifest -----

// ErrNoState is returned by ReadState when the state file is missing.
var ErrNoState = errors.New("no state manifest")

func writeState(p Plan) error {
	st := State{
		TicketID:  p.Memory.TicketID,
		Branch:    p.Branch,
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
	dir := filepath.Join(p.WorkspaceDir, ".thicket")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "state.json"), b, 0o644)
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
