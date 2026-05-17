package config

import (
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uribrecher/thicket/internal/tui/wizard"
)

// ticketsPage handles the "where does the Shortcut API token live?"
// question. It opens with a two-option fork so brand-new users
// without a token can short-circuit to the token-generation page in
// their browser instead of being shoved straight into a password-
// manager picker they can't fill in:
//
//   - "no token yet" → opens https://app.shortcut.com/<slug>/settings/
//     account/api-tokens in the user's browser and quits the wizard
//     cleanly (exit 0). cmd/thicket prints a re-run hint after we
//     return.
//   - "already have one" → walks the existing secretPicker (manager →
//     1Password account/item/field cascade, or typed ref for env /
//     bitwarden / pass).
//
// The fork is skipped entirely when the user is re-running config
// against an existing op:// ref — the picker's preseed jumps straight
// to stateValidated and there's no point asking them to re-pick a
// path they've already walked.
type ticketsPage struct {
	step    ticketsStep
	forkIdx int // 0 = generate, 1 = picker
	picker  *secretPicker
	seeded  bool
}

type ticketsStep int

const (
	stepFork     ticketsStep = iota // top-level "have one / don't have one" pick
	stepGenerate                    // chose "don't have one"; show open-browser button
	stepPicker                      // chose "have one"; defer to secretPicker
)

func newTicketsPage() *ticketsPage {
	return &ticketsPage{
		picker: newSecretPicker("Shortcut API token", "SHORTCUT_API_TOKEN"),
	}
}

func (p *ticketsPage) Title() string { return "Tickets" }

func (p *ticketsPage) Hints() string {
	switch p.step {
	case stepFork:
		return "↑/↓ pick option · enter selects"
	case stepGenerate:
		return "enter opens browser · shift+tab back"
	case stepPicker:
		return p.picker.hints()
	}
	return ""
}

// Complete is true only when the user has fully filled in the picker.
// The fork and generate sub-steps never satisfy Complete — the user
// must either walk the picker to validation or press the generate
// button (which bails out of the wizard entirely).
func (p *ticketsPage) Complete() bool {
	return p.step == stepPicker && p.picker.validated()
}

func (p *ticketsPage) InitCmd(m *wizard.Model) tea.Cmd {
	if p.seeded {
		return nil
	}
	p.picker.preseed(m.ConfigDeps.Cfg.Passwords.Manager, m.ConfigDeps.Cfg.Passwords.ShortcutTokenRef)
	if p.picker.state == stateValidated {
		// Re-run path: an existing op:// ref already satisfies us, so
		// skip the fork entirely and land on the picker so the user
		// can ← back / shift+tab re-pick without an extra hop.
		p.picker.chosenAccount = m.ConfigDeps.Cfg.Passwords.ShortcutTokenAccount
		p.step = stepPicker
	}
	p.seeded = true
	return nil
}

func (p *ticketsPage) Update(m *wizard.Model, msg tea.Msg) (wizard.Page, tea.Cmd) {
	if _, ok := msg.(wizard.GoNextMsg); ok {
		p.commit(m)
		return p, nil
	}

	k, isKey := msg.(tea.KeyMsg)

	switch p.step {
	case stepFork:
		if !isKey {
			return p, nil
		}
		switch k.String() {
		case "up", "k":
			if p.forkIdx > 0 {
				p.forkIdx--
			}
		case "down", "j":
			if p.forkIdx < 1 {
				p.forkIdx++
			}
		case "enter":
			if p.forkIdx == 0 {
				p.step = stepGenerate
				return p, nil
			}
			p.step = stepPicker
			return p, nil
		}
		return p, nil

	case stepGenerate:
		if !isKey {
			return p, nil
		}
		switch k.String() {
		case "shift+tab":
			p.step = stepFork
			return p, nil
		case "enter":
			openBrowser(shortcutTokensURL(m.ConfigDeps.Cfg.Shortcut.WorkspaceSlug))
			return p, func() tea.Msg { return wizard.ConfigDeferredMsg{} }
		}
		return p, nil

	case stepPicker:
		// shift+tab at the top of the picker's own state machine
		// returns the user to the fork; otherwise let the picker
		// consume it to step its sub-state back.
		if isKey && k.String() == "shift+tab" && p.picker.state == stateManager {
			p.step = stepFork
			return p, nil
		}
		cmd := p.picker.update(m, msg)
		if p.picker.validated() {
			p.commit(m)
		}
		return p, cmd
	}
	return p, nil
}

func (p *ticketsPage) commit(m *wizard.Model) {
	if !p.picker.validated() {
		return
	}
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

	switch p.step {
	case stepFork:
		p.renderFork(&b)
	case stepGenerate:
		p.renderGenerate(&b, m)
	case stepPicker:
		b.WriteString(p.picker.view(m))
	}
	return wizard.Indent(b.String(), 2)
}

// renderFork paints the two-option "have you got a token?" pick.
// Mirrors the agent-page backend list visually (same marker / style
// pair) so the wizard's pages feel cut from one cloth.
func (p *ticketsPage) renderFork(b *strings.Builder) {
	options := []struct {
		label, desc string
	}{
		{"I don't have a Shortcut API token yet",
			"Opens Shortcut's API tokens page in your browser. Save the new token to your password manager (or as $SHORTCUT_API_TOKEN), then re-run `thicket config`."},
		{"I already have one",
			"Tell thicket where it's stored: 1Password, Bitwarden, pass, or env var."},
	}
	for i, opt := range options {
		marker := wizard.CursorStyle.Render("▶ ")
		style := wizard.CursorStyle
		if i != p.forkIdx {
			marker = "  "
			style = wizard.DimStyle
		}
		b.WriteString("  " + marker + style.Render(opt.label) + "\n")
		b.WriteString("    " + wizard.HintStyle.Render(opt.desc) + "\n\n")
	}
}

// renderGenerate paints the "open browser" step. Visually it matches
// the submit-page Confirm button so the user recognizes it as the
// commit action of this sub-step.
func (p *ticketsPage) renderGenerate(b *strings.Builder, m *wizard.Model) {
	url := shortcutTokensURL(m.ConfigDeps.Cfg.Shortcut.WorkspaceSlug)
	b.WriteString("  " + wizard.SectionStyle.Render("Generate a Shortcut API token") + "\n")
	b.WriteString("  " + wizard.HintStyle.Render("We'll open this URL in your default browser:") + "\n")
	b.WriteString("    " + wizard.HighlightStyle.Render(url) + "\n\n")
	b.WriteString("  " + wizard.HintStyle.Render(
		"Once you've created the token, save it to your password manager (or set $SHORTCUT_API_TOKEN), then re-run `thicket config`.") + "\n\n")
	b.WriteString("  " + wizard.CreateBtnStyle.Render("Open Shortcut API tokens page →") + "\n")
}

// shortcutTokensURL returns the API-tokens page URL for the given
// workspace slug. When the slug is empty (first-time setup before
// the user has filled it in), we fall back to the slug-less page —
// Shortcut redirects authenticated users to their workspace from
// there, so the user still ends up where they need to be.
func shortcutTokensURL(slug string) string {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return "https://app.shortcut.com/settings/account/api-tokens"
	}
	return "https://app.shortcut.com/" + slug + "/settings/account/api-tokens"
}

// openBrowser hands url to the platform's "open this in the default
// browser" command. Failures are silent: we've already committed to
// quitting the wizard, and a missing `open` / `xdg-open` is rare
// enough that the re-run hint cmd/thicket prints next is recovery
// enough. .Start() (not .Run()) so the wizard doesn't block waiting
// on the browser to actually launch.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
