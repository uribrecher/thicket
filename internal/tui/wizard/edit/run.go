// Package edit implements the `thicket edit` flow: a 3-page Bubble
// Tea wizard (Workspace → Repos → Submit) that attaches new
// worktrees / clones to an already-created workspace. Repo removal
// is out of scope here — for that, `thicket rm` + `thicket start`
// is the supported path.
package edit

import (
	"context"
	"errors"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uribrecher/thicket/internal/tui"
	"github.com/uribrecher/thicket/internal/tui/wizard"
)

// Run shows the edit wizard. Returns the wizard.EditResult on success,
// tui.ErrCancelled on Esc/Ctrl-C, or any underlying tea error.
func Run(deps wizard.EditDeps) (wizard.EditResult, error) {
	if deps.Ctx == nil {
		deps.Ctx = context.Background()
	}
	m := newModel(deps)
	finalModel, err := tea.NewProgram(m).Run()
	if err != nil {
		return wizard.EditResult{}, err
	}
	fm := finalModel.(*wizard.Model)
	if errors.Is(fm.Err, tui.ErrCancelled) {
		return wizard.EditResult{}, tui.ErrCancelled
	}
	if fm.Err != nil {
		return wizard.EditResult{}, fm.Err
	}
	return fm.EditResult, nil
}

// newModel constructs the wizard.Model for the edit flow. Shares wizard.Model
// machinery with the start flow (tab rendering, key routing,
// advance/gotoPage) but with a different page triple and
// EditMode-only state fields. The start-only fields (Ticket,
// LLMCache, SummaryCache, …) stay zero-valued and are never read
// in this flow.
func newModel(deps wizard.EditDeps) *wizard.Model {
	m := &wizard.Model{
		EditMode:     true,
		EditDeps:     deps,
		CloneInclude: make(map[string]bool),
	}
	m.Pages = []wizard.Page{
		newWorkspacePage(),
		newReposPage(),
		newSubmitPage(),
	}
	if deps.PreselectedWorkspace != nil {
		// Same shape as the start flow's preselected-path: seed the
		// first page so it renders a read-only summary and start the
		// wizard on Repos.
		wp := m.Pages[0].(*workspacePage)
		wp.preseed(*deps.PreselectedWorkspace)
		m.SelectedWorkspace = deps.PreselectedWorkspace
		m.Active = 1
	}
	return m
}
