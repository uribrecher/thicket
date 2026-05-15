// Package config implements the `thicket config` flow: a 3- to
// 5-page Bubble Tea wizard (Welcome? → Git → Tickets? → Agent →
// Submit) that walks the user through writing
// ~/.config/thicket/config.toml. Welcome appears only on first run
// (no existing config); Tickets appears only when
// $SHORTCUT_API_TOKEN is unset.
package config

import (
	"context"
	"errors"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uribrecher/thicket/internal/secrets"
	"github.com/uribrecher/thicket/internal/tui"
	"github.com/uribrecher/thicket/internal/tui/wizard"
)

// Run shows the config wizard. Returns the wizard.ConfigResult on success,
// tui.ErrCancelled if the user pressed Esc / Ctrl-C, or any error
// from the underlying Bubble Tea program.
func Run(deps wizard.ConfigDeps) (wizard.ConfigResult, error) {
	if deps.Ctx == nil {
		deps.Ctx = context.Background()
	}
	if deps.Cfg == nil {
		return wizard.ConfigResult{}, errors.New("wizard.ConfigDeps.Cfg is required")
	}
	m := newModel(deps)
	finalModel, err := tea.NewProgram(m).Run()
	if err != nil {
		return wizard.ConfigResult{}, err
	}
	fm := finalModel.(*wizard.Model)
	if errors.Is(fm.Err, tui.ErrCancelled) {
		return wizard.ConfigResult{}, tui.ErrCancelled
	}
	if fm.Err != nil {
		return wizard.ConfigResult{}, fm.Err
	}
	return fm.ConfigResult, nil
}

// newModel assembles the page list for the config flow. Pages are
// added conditionally so the tab bar reflects exactly what the user
// will see — env-covered secrets and re-runs (where the Welcome note
// would just be noise) drop their pages entirely.
func newModel(deps wizard.ConfigDeps) *wizard.Model {
	m := &wizard.Model{
		ConfigMode:        true,
		ConfigDeps:        deps,
		ConfigOpItemCache: make(map[string][]secrets.OnePasswordItem),
	}
	var pages []wizard.Page
	if deps.FirstRun {
		pages = append(pages, newWelcomePage())
	}
	pages = append(pages, newGitPage())
	if os.Getenv("SHORTCUT_API_TOKEN") == "" {
		pages = append(pages, newTicketsPage())
	}
	pages = append(pages, newAgentPage())
	pages = append(pages, newSubmitPage())
	m.Pages = pages
	return m
}
