package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/uribrecher/thicket/internal/config"
	gitops "github.com/uribrecher/thicket/internal/git"
	"github.com/uribrecher/thicket/internal/ticket"
	"github.com/uribrecher/thicket/internal/tui"
	"github.com/uribrecher/thicket/internal/tui/wizard"
	"github.com/uribrecher/thicket/internal/workspace"
)

func runEdit(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfigOrPointAtInit()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	// MVP: edit requires a TTY. Bubble Tea drives the wizard, and we
	// don't have a legacy line-oriented fallback flow for edit (yet).
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("`thicket edit` requires an interactive terminal — no scripted/--no-interactive mode yet")
	}

	src, err := buildTicketSource(cmd.Context(), cfg)
	if err != nil {
		return err
	}

	repos, err := loadCatalog(cfg, errOut)
	if err != nil {
		return err
	}

	// Args-path: `thicket edit <slug>` preselects the workspace and
	// skips the picker. Validation mirrors `thicket rm <slug>`.
	var preselected *workspace.ManagedWorkspace
	if len(args) == 1 {
		if err := validateSlug(args[0]); err != nil {
			return err
		}
		ws, lookupErr := findWorkspaceBySlug(cfg, args[0])
		if lookupErr != nil {
			return lookupErr
		}
		if ws == nil {
			return fmt.Errorf("no managed workspace found with slug %q (run `thicket list` to see what's available)", args[0])
		}
		preselected = ws
	}

	deps := wizard.EditDeps{
		Ctx:                  cmd.Context(),
		Cfg:                  cfg,
		Repos:                repos,
		Git:                  gitops.New(),
		PreselectedWorkspace: preselected,
	}
	res, err := wizard.RunEdit(deps)
	if err != nil {
		if errors.Is(err, tui.ErrCancelled) {
			fmt.Fprintln(out, "cancelled.")
			return nil
		}
		return err
	}

	// Surface any skipped/failed clones from the wizard.
	for _, s := range res.Skipped {
		fmt.Fprintf(errOut, "skipped %s: %s\n", s.Name, s.Reason)
	}
	if len(res.AddPlan.NewRepos) == 0 {
		fmt.Fprintln(out, "nothing to add.")
		return nil
	}

	// Refresh the ticket so the regenerated CLAUDE.local.md header
	// reflects current title / state / body / requester. A failed
	// fetch is non-fatal — we fall back to a header built from
	// state.json + leave ticket-specific fields empty.
	res.AddPlan = enrichAddPlanWithTicket(cmd.Context(), src, res, errOut)

	// workspace.Add runs in plain stdout — the wizard already exited.
	res.AddPlan.Progress = out
	w := workspace.New(gitops.New())
	addResult, addErr := w.Add(res.AddPlan)
	if addErr != nil {
		return addErr
	}
	for _, s := range addResult.Skipped {
		fmt.Fprintf(errOut, "skipped %s: %s\n", s.Name, s.Reason)
	}
	fmt.Fprintf(out, "\nadded %d worktree(s) to %s\n",
		len(addResult.Added), res.Workspace.Path)
	return nil
}

// findWorkspaceBySlug scans the managed workspaces and returns the one
// matching slug exactly, or nil if not found. Returns an error only on
// a fatal ListManaged failure (e.g. unreadable workspace_root).
func findWorkspaceBySlug(cfg *config.Config, slug string) (*workspace.ManagedWorkspace, error) {
	wsList, _, err := workspace.ListManaged(cfg.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	for i := range wsList {
		if wsList[i].Slug == slug {
			return &wsList[i], nil
		}
	}
	return nil, nil
}

// enrichAddPlanWithTicket re-fetches the ticket so the regenerated
// CLAUDE.local.md header carries fresh title / body / state / owner /
// requester / labels. On any fetch failure we surface a warning and
// return the AddPlan unchanged — the regen still works (using just
// TicketID + Branch from state.json), the header is just less rich.
func enrichAddPlanWithTicket(ctx context.Context, src ticket.Source, res wizard.EditResult, errOut io.Writer) workspace.AddPlan {
	plan := res.AddPlan
	id, parseErr := src.Parse(plan.Memory.TicketID)
	if parseErr != nil {
		fmt.Fprintf(errOut, "warning: could not parse ticket id %q: %v\n", plan.Memory.TicketID, parseErr)
		return plan
	}
	tk, fetchErr := src.Fetch(id)
	if fetchErr != nil {
		fmt.Fprintf(errOut, "warning: could not refresh ticket %s: %v\n", plan.Memory.TicketID, fetchErr)
		return plan
	}
	_ = ctx
	plan.Memory.Title = tk.Title
	plan.Memory.URL = tk.URL
	plan.Memory.State = tk.State
	plan.Memory.Owner = tk.Owner
	plan.Memory.Body = tk.Body
	return plan
}
