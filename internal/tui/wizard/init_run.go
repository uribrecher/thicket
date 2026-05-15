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

// InitDeps wires the init wizard to the rest of thicket. Same one-way
// dependency pattern as Deps / EditDeps — the wizard never imports
// cmd/thicket.
type InitDeps struct {
	Ctx context.Context

	// Cfg is the *working* config the wizard mutates as the user
	// answers each page. On first run callers seed it with
	// config.Default(); on re-run they seed it with the loaded config
	// so existing values pre-fill each field.
	Cfg *config.Config

	// FirstRun controls whether the Welcome page is included.
	FirstRun bool
}

// InitResult is what RunInit hands back. The actual file write happens
// post-wizard in cmd/thicket/init.go — same separation as
// workspace.Create after Run and workspace.Add after RunEdit.
type InitResult struct {
	// Cfg is the populated config the caller should validate + save.
	// nil when the user cancelled.
	Cfg *config.Config

	// Confirmed is true only when the user reached the Submit page and
	// hit Confirm. If false (e.g. Esc), the caller should not save.
	Confirmed bool
}

// RunInit shows the init wizard. Returns the InitResult on success,
// tui.ErrCancelled if the user pressed Esc / Ctrl-C, or any error
// from the underlying Bubble Tea program.
func RunInit(deps InitDeps) (InitResult, error) {
	if deps.Ctx == nil {
		deps.Ctx = context.Background()
	}
	if deps.Cfg == nil {
		return InitResult{}, errors.New("InitDeps.Cfg is required")
	}
	m := newInitModel(deps)
	finalModel, err := tea.NewProgram(m).Run()
	if err != nil {
		return InitResult{}, err
	}
	fm := finalModel.(*Model)
	if errors.Is(fm.err, tui.ErrCancelled) {
		return InitResult{}, tui.ErrCancelled
	}
	if fm.err != nil {
		return InitResult{}, fm.err
	}
	return fm.initResult, nil
}

// newInitModel assembles the page list for the init flow. Pages are
// added conditionally so the tab bar reflects exactly what the user
// will see — env-covered secrets and re-runs (where the welcome note
// would just be noise) drop their pages entirely.
func newInitModel(deps InitDeps) *Model {
	m := &Model{
		initMode:        true,
		initDeps:        deps,
		initOpItemCache: make(map[string][]secrets.OnePasswordItem),
	}
	var pages []Page
	if deps.FirstRun {
		pages = append(pages, newInitWelcomePage())
	}
	pages = append(pages, newInitGitPage())
	if os.Getenv("SHORTCUT_API_TOKEN") == "" {
		pages = append(pages, newInitTicketsPage())
	}
	pages = append(pages, newInitAgentPage())
	pages = append(pages, newInitSubmitPage())
	m.pages = pages
	return m
}
