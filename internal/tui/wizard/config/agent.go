package config

import (
	"github.com/uribrecher/thicket/internal/tui/wizard"

	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// agent backend identifiers.
const (
	agentBackendCLI = "cli"
	agentBackendAPI = "api"
)

// agentPage asks how thicket should talk to Claude:
//   - "cli": shell out to the local `claude` binary (no API key,
//     reuses the user's Claude Code / Enterprise login).
//   - "api": call the Anthropic API directly. When this is picked AND
//     ANTHROPIC_API_KEY isn't already in the env, the nested
//     secretPicker collects the password-manager reference for the
//     key — including the full 1Password account/item/field cascade.
type agentPage struct {
	backendIdx int // 0 = cli, 1 = api
	focus      int // 0 = backend list, 1 = secret picker (only when api+no-env)

	claudeOnPath bool
	apiKeyInEnv  bool

	picker *secretPicker
	seeded bool
}

func newAgentPage() *agentPage {
	return &agentPage{
		picker: newSecretPicker("Anthropic API key", "ANTHROPIC_API_KEY"),
	}
}

func (p *agentPage) Title() string { return "Agent" }

func (p *agentPage) Hints() string {
	if p.pickerVisible() && p.focus == 1 {
		return p.picker.hints()
	}
	return "↑/↓ pick backend · tab/enter continues"
}

func (p *agentPage) Complete() bool {
	if p.backendCurrent() == agentBackendCLI {
		return true
	}
	if p.apiKeyInEnv {
		return true
	}
	return p.picker.validated()
}

func (p *agentPage) InitCmd(m *wizard.Model) tea.Cmd {
	if !p.seeded {
		_, err := exec.LookPath("claude")
		p.claudeOnPath = err == nil
		switch m.ConfigDeps.Cfg.ClaudeBackend {
		case agentBackendAPI:
			p.backendIdx = 1
		case agentBackendCLI:
			p.backendIdx = 0
		default:
			if p.claudeOnPath {
				p.backendIdx = 0
			} else {
				p.backendIdx = 1
			}
		}
		p.apiKeyInEnv = os.Getenv("ANTHROPIC_API_KEY") != ""
		p.picker.preseed(m.ConfigDeps.Cfg.Passwords.Manager, m.ConfigDeps.Cfg.Passwords.AnthropicKeyRef)
		if p.picker.state == stateValidated {
			p.picker.chosenAccount = m.ConfigDeps.Cfg.Passwords.AnthropicKeyAccount
		}
		p.seeded = true
	}
	return nil
}

func (p *agentPage) backendCurrent() string {
	if p.backendIdx == 1 {
		return agentBackendAPI
	}
	return agentBackendCLI
}

func (p *agentPage) pickerVisible() bool {
	return p.backendCurrent() == agentBackendAPI && !p.apiKeyInEnv
}

func (p *agentPage) Update(m *wizard.Model, msg tea.Msg) (wizard.Page, tea.Cmd) {
	if _, ok := msg.(wizard.GoNextMsg); ok {
		p.commit(m)
		return p, nil
	}

	// Backend-list focus: handle the two-option select up here so the
	// arrow keys don't leak into the picker's own state machine.
	if p.focus == 0 {
		if k, ok := msg.(tea.KeyMsg); ok {
			switch k.String() {
			case "up", "k":
				if p.backendIdx > 0 {
					p.backendIdx--
				}
				return p, nil
			case "down", "j":
				if p.backendIdx < 1 {
					p.backendIdx++
				}
				return p, nil
			case "tab", "enter":
				if p.pickerVisible() {
					p.focus = 1
					p.commit(m)
					return p, nil
				}
				p.commit(m)
				if p.Complete() {
					return p, func() tea.Msg { return wizard.GoNextMsg{} }
				}
				return p, nil
			}
		}
		return p, nil
	}

	// focus == 1: drive the nested secret picker.
	// shift+tab returns to the backend list only when the picker is
	// at its top state (stateManager). Otherwise we let the picker
	// consume shift+tab to step its own state back.
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "shift+tab" && p.picker.state == stateManager {
		p.focus = 0
		return p, nil
	}
	cmd := p.picker.update(m, msg)
	if p.picker.validated() {
		p.commit(m)
	}
	return p, cmd
}

func (p *agentPage) commit(m *wizard.Model) {
	cfg := m.ConfigDeps.Cfg
	cfg.ClaudeBackend = p.backendCurrent()
	if p.pickerVisible() && p.picker.validated() {
		cfg.Passwords.Manager = p.picker.finalManager()
		cfg.Passwords.AnthropicKeyRef = p.picker.finalRef()
		cfg.Passwords.AnthropicKeyAccount = p.picker.finalAccount()
		return
	}
	if p.backendCurrent() == agentBackendCLI {
		// Clear stale anthropic refs — CLI mode never needs them.
		cfg.Passwords.AnthropicKeyRef = ""
		cfg.Passwords.AnthropicKeyAccount = ""
	}
}

func (p *agentPage) View(m *wizard.Model) string {
	var b strings.Builder
	b.WriteString(wizard.TitleStyle.Render("How should thicket talk to Claude?"))
	b.WriteString("\n\n")

	options := []struct {
		label, desc string
	}{
		{"cli — local `claude` binary", "Reuses your Claude Code / Enterprise auth. No API key needed."},
		{"api — Anthropic API directly", "Calls api.anthropic.com. Needs an API key in your password manager or env."},
	}
	for i, opt := range options {
		marker := "  "
		style := wizard.DimStyle
		if i == p.backendIdx {
			if p.focus == 0 {
				marker = wizard.CursorStyle.Render("▶ ")
				style = wizard.CursorStyle
			} else {
				marker = wizard.SelectedTagStyle.Render("● ")
				style = wizard.HighlightStyle
			}
		}
		extra := ""
		if i == 0 && !p.claudeOnPath {
			extra = "  " + wizard.WarnStyle.Render("(not on PATH)")
		}
		b.WriteString("  " + marker + style.Render(opt.label) + extra + "\n")
		b.WriteString("    " + wizard.HintStyle.Render(opt.desc) + "\n")
	}
	b.WriteString("\n")

	switch {
	case p.pickerVisible():
		b.WriteString("  " + wizard.HintStyle.Render("(tab into the form below to set the API key reference)") + "\n\n")
		b.WriteString(p.picker.view(m))
	case p.backendCurrent() == agentBackendAPI && p.apiKeyInEnv:
		b.WriteString("  " + wizard.SelectedTagStyle.Render("✓ $ANTHROPIC_API_KEY is set — no password-manager reference needed.") + "\n")
	}
	return wizard.Indent(b.String(), 2)
}
