package wizard

import (
	"context"
	"errors"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uribrecher/thicket/internal/config"
	"github.com/uribrecher/thicket/internal/secrets"
	"github.com/uribrecher/thicket/internal/tui"
)

// ConfigDeps wires the config wizard to the rest of thicket. Same one-way
// dependency pattern as Deps / EditDeps — the wizard never imports
// cmd/thicket.
type ConfigDeps struct {
	Ctx context.Context

	// Cfg is the *working* config the wizard mutates as the user
	// answers each page. On first run callers seed it with
	// config.Default(); on re-run they seed it with the loaded config
	// so existing values pre-fill each field.
	Cfg *config.Config

	// FirstRun controls whether the Welcome page is included.
	FirstRun bool
}

// ConfigResult is what RunConfig hands back. The actual file write happens
// post-wizard in cmd/thicket/init.go — same separation as
// workspace.Create after Run and workspace.Add after RunEdit.
type ConfigResult struct {
	// Cfg is the populated config the caller should validate + save.
	// nil when the user cancelled.
	Cfg *config.Config

	// Confirmed is true only when the user reached the Submit page and
	// hit Confirm. If false (e.g. Esc), the caller should not save.
	Confirmed bool
}

// RunConfig shows the config wizard. Returns the ConfigResult on success,
// tui.ErrCancelled if the user pressed Esc / Ctrl-C, or any error
// from the underlying Bubble Tea program.
func RunConfig(deps ConfigDeps) (ConfigResult, error) {
	if deps.Ctx == nil {
		deps.Ctx = context.Background()
	}
	if deps.Cfg == nil {
		return ConfigResult{}, errors.New("ConfigDeps.Cfg is required")
	}
	m := newConfigModel(deps)
	finalModel, err := tea.NewProgram(m).Run()
	if err != nil {
		return ConfigResult{}, err
	}
	fm := finalModel.(*Model)
	if errors.Is(fm.Err, tui.ErrCancelled) {
		return ConfigResult{}, tui.ErrCancelled
	}
	if fm.Err != nil {
		return ConfigResult{}, fm.Err
	}
	return fm.ConfigResult, nil
}

// newConfigModel assembles the page list for the config flow. Pages are
// added conditionally so the tab bar reflects exactly what the user
// will see — env-covered secrets and re-runs (where the welcome note
// would just be noise) drop their pages entirely.
func newConfigModel(deps ConfigDeps) *Model {
	m := &Model{
		ConfigMode:        true,
		ConfigDeps:        deps,
		ConfigOpItemCache: make(map[string][]secrets.OnePasswordItem),
	}
	var pages []Page
	if deps.FirstRun {
		pages = append(pages, newConfigWelcomePage())
	}
	pages = append(pages, newConfigGitPage())
	if os.Getenv("SHORTCUT_API_TOKEN") == "" {
		pages = append(pages, newConfigTicketsPage())
	}
	pages = append(pages, newConfigAgentPage())
	pages = append(pages, newConfigSubmitPage())
	m.Pages = pages
	return m
}
