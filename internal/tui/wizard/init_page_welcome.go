package wizard

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// initWelcomePage is the first page of `thicket init` on first run. It
// shows a short intro and is always Complete() — the user just hits
// → to move on. Re-runs of `thicket init` skip this page entirely
// (newInitModel doesn't add it when FirstRun is false).
type initWelcomePage struct{}

func newInitWelcomePage() *initWelcomePage { return &initWelcomePage{} }

func (p *initWelcomePage) Title() string { return "Welcome" }

func (p *initWelcomePage) Hints() string { return "enter continues" }

func (p *initWelcomePage) Complete() bool { return true }

func (p *initWelcomePage) Update(m *Model, msg tea.Msg) (Page, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "enter" {
		return p, func() tea.Msg { return goNextMsg{} }
	}
	return p, nil
}

func (p *initWelcomePage) View(m *Model) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Welcome to thicket"))
	b.WriteString("\n\n")
	b.WriteString("  First-time setup — let's wire up your workflow.\n")
	b.WriteString("\n")
	b.WriteString("  " + hintStyle.Render(
		"You can re-run `thicket init` later to tweak any of these settings.") + "\n")
	b.WriteString("  " + hintStyle.Render(
		"Press → (or enter) to continue, esc to cancel.") + "\n")
	return indent(b.String(), 2)
}
