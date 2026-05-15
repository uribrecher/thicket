package config

import (
	"github.com/uribrecher/thicket/internal/tui/wizard"

	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// ticketsPage asks where the Shortcut API token lives. It's only
// included in the page list when SHORTCUT_API_TOKEN is unset at init
// time (see newConfigModel). When it *is* set, the env var wins at
// runtime and no PM reference is needed, so the page is omitted
// entirely rather than asking the user to fill in a slot that will
// never be read.
//
// The page is a thin wrapper around secretPicker. The picker owns
// the full manager → (account → item → field | typed ref) state
// machine; the page just calls into it and writes the resulting
// (manager, ref [, account]) tuple back to the working config when
// the picker reaches `validated`.
type ticketsPage struct {
	picker *secretPicker
	seeded bool
}

func newTicketsPage() *ticketsPage {
	return &ticketsPage{
		picker: newSecretPicker("Shortcut API token", "SHORTCUT_API_TOKEN"),
	}
}

func (p *ticketsPage) Title() string { return "Tickets" }

func (p *ticketsPage) Hints() string { return p.picker.hints() }

// Complete is true once the picker has a validated (manager, ref)
// pair. Validation is live: mgr.Get for non-env managers, shape-
// check for env, and "user actually walked through the field picker"
// for 1Password.
func (p *ticketsPage) Complete() bool { return p.picker.validated() }

func (p *ticketsPage) InitCmd(m *wizard.Model) tea.Cmd {
	if !p.seeded {
		p.picker.preseed(m.ConfigDeps.Cfg.Passwords.Manager, m.ConfigDeps.Cfg.Passwords.ShortcutTokenRef)
		// If preseed jumped us into stateValidated (e.g. re-running
		// init with an existing op:// ref), also re-hydrate the
		// account UUID from the saved config so future renders can
		// show the account label.
		if p.picker.state == stateValidated {
			p.picker.chosenAccount = m.ConfigDeps.Cfg.Passwords.ShortcutTokenAccount
		}
		p.seeded = true
	}
	return nil
}

func (p *ticketsPage) Update(m *wizard.Model, msg tea.Msg) (wizard.Page, tea.Cmd) {
	if _, ok := msg.(wizard.GoNextMsg); ok {
		p.commit(m)
		return p, nil
	}
	cmd := p.picker.update(m, msg)
	if p.picker.validated() {
		p.commit(m)
	}
	return p, cmd
}

func (p *ticketsPage) commit(m *wizard.Model) {
	cfg := m.ConfigDeps.Cfg
	if mgr := p.picker.finalManager(); mgr != "" {
		cfg.Passwords.Manager = mgr
	}
	if ref := p.picker.finalRef(); ref != "" {
		cfg.Passwords.ShortcutTokenRef = ref
	}
	cfg.Passwords.ShortcutTokenAccount = p.picker.finalAccount()
}

func (p *ticketsPage) View(m *wizard.Model) string {
	var b strings.Builder
	b.WriteString(wizard.TitleStyle.Render("Where is your Shortcut API token?"))
	b.WriteString("\n\n")
	b.WriteString("  " + wizard.HintStyle.Render(
		"$SHORTCUT_API_TOKEN isn't set — thicket needs a password-manager reference so it can fetch the token at runtime.") + "\n\n")
	b.WriteString(p.picker.view(m))
	return wizard.Indent(b.String(), 2)
}
