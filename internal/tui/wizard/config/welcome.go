package config

import (
	"github.com/uribrecher/thicket/internal/tui/wizard"

	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// welcomePage is the first page of `thicket config` on first run. It
// shows a short intro and is always Complete() — the user just hits
// → to move on. Re-runs of `thicket config` skip this page entirely
// (newModel doesn't add it when FirstRun is false).
type welcomePage struct{}

func newWelcomePage() *welcomePage { return &welcomePage{} }

func (p *welcomePage) Title() string { return "Welcome" }

func (p *welcomePage) Hints() string { return "enter continues" }

func (p *welcomePage) Complete() bool { return true }

func (p *welcomePage) Update(m *wizard.Model, msg tea.Msg) (wizard.Page, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "enter" {
		return p, func() tea.Msg { return wizard.GoNextMsg{} }
	}
	return p, nil
}

func (p *welcomePage) View(m *wizard.Model) string {
	var b strings.Builder
	b.WriteString(wizard.TitleStyle.Render("Welcome to thicket"))
	b.WriteString("\n\n")
	b.WriteString("  First-time setup — let's wire up your workflow.\n")
	b.WriteString("\n")
	b.WriteString("  " + wizard.HintStyle.Render(
		"You can re-run `thicket config` later to tweak any of these settings.") + "\n")
	b.WriteString("  " + wizard.HintStyle.Render(
		"Press → (or enter) to continue, esc to cancel.") + "\n")
	return wizard.Indent(b.String(), 2)
}
