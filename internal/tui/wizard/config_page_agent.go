package wizard

import (
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

// configAgentPage asks how thicket should talk to Claude:
//   - "cli": shell out to the local `claude` binary (no API key,
//     reuses the user's Claude Code / Enterprise login).
//   - "api": call the Anthropic API directly. When this is picked AND
//     ANTHROPIC_API_KEY isn't already in the env, the nested
//     secretPicker collects the password-manager reference for the
//     key — including the full 1Password account/item/field cascade.
type configAgentPage struct {
	backendIdx int // 0 = cli, 1 = api
	focus      int // 0 = backend list, 1 = secret picker (only when api+no-env)

	claudeOnPath bool
	apiKeyInEnv  bool

	picker *secretPicker
	seeded bool
}

func newConfigAgentPage() *configAgentPage {
	return &configAgentPage{
		picker: newSecretPicker("Anthropic API key", "ANTHROPIC_API_KEY"),
	}
}

func (p *configAgentPage) Title() string { return "Agent" }

func (p *configAgentPage) Hints() string {
	if p.pickerVisible() && p.focus == 1 {
		return p.picker.hints()
	}
	return "↑/↓ pick backend · tab/enter continues"
}

func (p *configAgentPage) Complete() bool {
	if p.backendCurrent() == agentBackendCLI {
		return true
	}
	if p.apiKeyInEnv {
		return true
	}
	return p.picker.validated()
}

func (p *configAgentPage) InitCmd(m *Model) tea.Cmd {
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

func (p *configAgentPage) backendCurrent() string {
	if p.backendIdx == 1 {
		return agentBackendAPI
	}
	return agentBackendCLI
}

func (p *configAgentPage) pickerVisible() bool {
	return p.backendCurrent() == agentBackendAPI && !p.apiKeyInEnv
}

func (p *configAgentPage) Update(m *Model, msg tea.Msg) (Page, tea.Cmd) {
	if _, ok := msg.(GoNextMsg); ok {
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
					return p, func() tea.Msg { return GoNextMsg{} }
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

func (p *configAgentPage) commit(m *Model) {
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

func (p *configAgentPage) View(m *Model) string {
	var b strings.Builder
	b.WriteString(TitleStyle.Render("How should thicket talk to Claude?"))
	b.WriteString("\n\n")

	options := []struct {
		label, desc string
	}{
		{"cli — local `claude` binary", "Reuses your Claude Code / Enterprise auth. No API key needed."},
		{"api — Anthropic API directly", "Calls api.anthropic.com. Needs an API key in your password manager or env."},
	}
	for i, opt := range options {
		marker := "  "
		style := DimStyle
		if i == p.backendIdx {
			if p.focus == 0 {
				marker = CursorStyle.Render("▶ ")
				style = CursorStyle
			} else {
				marker = SelectedTagStyle.Render("● ")
				style = HighlightStyle
			}
		}
		extra := ""
		if i == 0 && !p.claudeOnPath {
			extra = "  " + WarnStyle.Render("(not on PATH)")
		}
		b.WriteString("  " + marker + style.Render(opt.label) + extra + "\n")
		b.WriteString("    " + HintStyle.Render(opt.desc) + "\n")
	}
	b.WriteString("\n")

	switch {
	case p.pickerVisible():
		b.WriteString("  " + HintStyle.Render("(tab into the form below to set the API key reference)") + "\n\n")
		b.WriteString(p.picker.view(m))
	case p.backendCurrent() == agentBackendAPI && p.apiKeyInEnv:
		b.WriteString("  " + SelectedTagStyle.Render("✓ $ANTHROPIC_API_KEY is set — no password-manager reference needed.") + "\n")
	}
	return Indent(b.String(), 2)
}
