package wizard

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// initTicketsPage asks where the Shortcut API token lives. It's only
// included in the page list when SHORTCUT_API_TOKEN is unset at init
// time (see newInitModel). When it *is* set, the env var wins at
// runtime and no PM reference is needed, so the page is omitted
// entirely rather than asking the user to fill in a slot that will
// never be read.
//
// The page is a thin wrapper around secretPicker. The picker owns
// the full manager → (account → item → field | typed ref) state
// machine; the page just calls into it and writes the resulting
// (manager, ref [, account]) tuple back to the working config when
// the picker reaches `validated`.
type initTicketsPage struct {
	picker *secretPicker
	seeded bool
}

func newInitTicketsPage() *initTicketsPage {
	return &initTicketsPage{
		picker: newSecretPicker("Shortcut API token", "SHORTCUT_API_TOKEN"),
	}
}

func (p *initTicketsPage) Title() string { return "Tickets" }

func (p *initTicketsPage) Hints() string { return p.picker.hints() }

// Complete is true once the picker has a validated (manager, ref)
// pair. Validation is live: mgr.Get for non-env managers, shape-
// check for env, and "user actually walked through the field picker"
// for 1Password.
func (p *initTicketsPage) Complete() bool { return p.picker.validated() }

func (p *initTicketsPage) initCmd(m *Model) tea.Cmd {
	if !p.seeded {
		p.picker.preseed(m.initDeps.Cfg.Passwords.Manager, m.initDeps.Cfg.Passwords.ShortcutTokenRef)
		// If preseed jumped us into stateValidated (e.g. re-running
		// init with an existing op:// ref), also re-hydrate the
		// account UUID from the saved config so future renders can
		// show the account label.
		if p.picker.state == stateValidated {
			p.picker.chosenAccount = m.initDeps.Cfg.Passwords.ShortcutTokenAccount
		}
		p.seeded = true
	}
	return nil
}

func (p *initTicketsPage) Update(m *Model, msg tea.Msg) (Page, tea.Cmd) {
	if _, ok := msg.(goNextMsg); ok {
		p.commit(m)
		return p, nil
	}
	cmd := p.picker.update(m, msg)
	if p.picker.validated() {
		p.commit(m)
	}
	return p, cmd
}

func (p *initTicketsPage) commit(m *Model) {
	cfg := m.initDeps.Cfg
	if mgr := p.picker.finalManager(); mgr != "" {
		cfg.Passwords.Manager = mgr
	}
	if ref := p.picker.finalRef(); ref != "" {
		cfg.Passwords.ShortcutTokenRef = ref
	}
	cfg.Passwords.ShortcutTokenAccount = p.picker.finalAccount()
}

func (p *initTicketsPage) View(m *Model) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Where is your Shortcut API token?"))
	b.WriteString("\n\n")
	b.WriteString("  " + hintStyle.Render(
		"$SHORTCUT_API_TOKEN isn't set — thicket needs a password-manager reference so it can fetch the token at runtime.") + "\n\n")
	b.WriteString(p.picker.view(m))
	return indent(b.String(), 2)
}
