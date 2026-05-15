package wizard

import (
	"context"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/config"
	gitops "github.com/uribrecher/thicket/internal/git"
	"github.com/uribrecher/thicket/internal/workspace"
)

// EditDeps mirrors Deps but for the `thicket edit` flow. The wizard
// here doesn't need a ticket.Source — it operates on an existing
// workspace (which already carries TicketID + Branch in its manifest)
// and adds new worktrees / clones to it. The ticket re-fetch for
// CLAUDE.local.md regen happens AFTER the wizard exits, in runEdit,
// so the wizard model stays source-agnostic.
type EditDeps struct {
	Ctx   context.Context
	Cfg   *config.Config
	Repos []catalog.Repo
	Git   *gitops.Git

	// PreselectedWorkspace, when non-nil, makes the wizard skip the
	// Workspace picker page and start on Repos. Used by the args
	// path `thicket edit <slug>` so the user doesn't have to re-pick
	// a workspace they already named on the command line.
	PreselectedWorkspace *workspace.ManagedWorkspace
}

// EditResult is what edit.Run returns on success.
type EditResult struct {
	// Workspace is the workspace the user picked / preselected. The
	// caller pairs it with AddPlan + a ticket Source to do the post-
	// wizard workspace.Add call.
	Workspace workspace.ManagedWorkspace

	// AddPlan is ready-to-execute: SourcePath / WorktreePath /
	// BranchExists all reflect the post-clone state. NewRepos is the
	// final delta (after the user's checkbox toggles in the submit
	// page).
	AddPlan workspace.AddPlan

	// Skipped reports repos the user toggled off in the submit page
	// or that failed to clone in-wizard. The caller surfaces these
	// on stderr after the wizard exits.
	Skipped []SkipReport
}
