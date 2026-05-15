package wizard

import (
	"context"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/config"
	"github.com/uribrecher/thicket/internal/detector"
	gitops "github.com/uribrecher/thicket/internal/git"
	"github.com/uribrecher/thicket/internal/ticket"
	"github.com/uribrecher/thicket/internal/workspace"
)

// Deps wires the start wizard to the rest of thicket without
// importing cmd/thicket — keeps the dependency graph one-way.
type Deps struct {
	Ctx    context.Context
	Cfg    *config.Config
	Src    ticket.Source
	Lister ticket.Lister // may be nil; callers that wired non-listers get an error page
	Repos  []catalog.Repo
	Detect func(ctx context.Context, tk ticket.Ticket, repos []catalog.Repo) ([]detector.RepoMatch, error)
	// Summarize, when set, returns up to detector.SummaryLines short
	// summary lines for the picked ticket. May be nil — the wizard
	// falls back to the first non-empty lines of the description so
	// the panel always renders something useful.
	Summarize func(ctx context.Context, tk ticket.Ticket) ([]string, error)
	// Nickname, when set, suggests a short (≤20-char, emoji-friendly)
	// label for the picked ticket. Used to pre-fill the editable
	// nickname input on the Plan page. May be nil — the input then
	// starts empty and the user can type their own (or leave blank).
	Nickname func(ctx context.Context, tk ticket.Ticket) (string, error)
	Git      *gitops.Git
	Flags    Flags

	// FindExistingWorkspace returns the already-managed workspace
	// for the given ticket id, or nil if none exists. The wizard
	// calls it after a ticket is committed; a non-nil result
	// short-circuits the rest of the flow and triggers a "reuse"
	// exit. Callers also use the returned ManagedWorkspace to
	// display the workspace's nickname / slug in the Ticket picker.
	FindExistingWorkspace func(ticketID string) *workspace.ManagedWorkspace

	// Preselected, when non-nil, makes the wizard skip the picker on
	// the Ticket page and start on Repos. Used by the args-path of
	// `thicket start <id>` so the user doesn't have to re-pick a
	// ticket they already named on the command line.
	Preselected *ticket.Ticket
}

// Flags is the subset of CLI flags the wizard needs to honor.
type Flags struct {
	Branch string
	DryRun bool
}

// SkipReport records one repo the wizard dropped from the workspace
// because its clone failed. runStart prints these to stderr after
// the wizard exits so the user sees what was skipped.
type SkipReport struct {
	Name   string
	Reason string
}

// Result is what start.Run hands back to runStart on success.
type Result struct {
	Ticket  ticket.Ticket
	Plan    workspace.Plan
	Skipped []SkipReport

	// ReuseDir, when non-empty, signals the wizard short-circuited
	// because the ticket already had a managed workspace. The caller
	// should skip Create and launch Claude directly in ReuseDir.
	ReuseDir string
}
