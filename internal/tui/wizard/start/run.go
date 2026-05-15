// Package start implements the `thicket start` flow: a 3-page Bubble
// Tea wizard (Ticket → Repos → Plan) that lets the user pick a ticket,
// pick the repos to attach to the workspace, and review + create the
// resulting workspace + worktrees.
package start

import (
	"context"
	"errors"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uribrecher/thicket/internal/detector"
	"github.com/uribrecher/thicket/internal/tui"
	"github.com/uribrecher/thicket/internal/tui/wizard"
)

// Run shows the wizard. Returns the wizard.Result on success,
// tui.ErrCancelled if the user pressed Esc/Ctrl-C, or any other
// error from the underlying Bubble Tea program. A short-circuit
// reuse exit returns a wizard.Result with ReuseDir set; runStart inspects
// that and launches Claude directly.
func Run(deps wizard.Deps) (wizard.Result, error) {
	if deps.Ctx == nil {
		deps.Ctx = context.Background()
	}
	m := newModel(deps)
	finalModel, err := tea.NewProgram(m).Run()
	if err != nil {
		return wizard.Result{}, err
	}
	fm := finalModel.(*wizard.Model)
	if errors.Is(fm.Err, tui.ErrCancelled) {
		return wizard.Result{}, tui.ErrCancelled
	}
	if fm.Err != nil {
		return wizard.Result{}, fm.Err
	}
	return fm.Result, nil
}

func newModel(deps wizard.Deps) *wizard.Model {
	m := &wizard.Model{
		Deps:          deps,
		LLMCache:      make(map[string][]detector.RepoMatch),
		SummaryCache:  make(map[string][]string),
		NicknameCache: make(map[string]detector.NicknameSuggestion),
		CloneInclude:  make(map[string]bool),
	}
	m.Pages = []wizard.Page{
		newTicketPage(),
		newReposPage(),
		newPlanPage(),
	}
	// Preselected-ticket path: seed the Ticket page so it renders a
	// read-only summary, and start the wizard on Repos. The user can
	// still go ← to peek at the ticket details.
	if deps.Preselected != nil {
		tp := m.Pages[0].(*ticketPage)
		tp.preseed(*deps.Preselected)
		m.Ticket = *deps.Preselected
		m.TicketID = deps.Preselected.SourceID
		m.Active = 1
	}
	return m
}
