package wizard

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// configSubmitPage shows the full config summary and a [ Confirm ]
// button. Enter on the button emits configDoneMsg{}; the wizard's
// handler stashes the populated config into m.configResult and quits.
// The actual file write happens in cmd/thicket/init.go afterwards.
type configSubmitPage struct{}

func newConfigSubmitPage() *configSubmitPage { return &configSubmitPage{} }

func (p *configSubmitPage) Title() string { return "Submit" }

func (p *configSubmitPage) Hints() string { return "enter confirms · ← review previous pages" }

func (p *configSubmitPage) Complete() bool { return true }

func (p *configSubmitPage) Update(m *Model, msg tea.Msg) (Page, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "enter" {
		return p, func() tea.Msg { return configDoneMsg{} }
	}
	return p, nil
}

func (p *configSubmitPage) View(m *Model) string {
	cfg := m.configDeps.Cfg
	var b strings.Builder
	b.WriteString(titleStyle.Render("Review and confirm"))
	b.WriteString("\n\n")

	b.WriteString("  " + planHeaderStyle.Render("Paths") + "\n")
	b.WriteString(fmt.Sprintf("    repos_root:     %s\n", cfg.ReposRoot))
	b.WriteString(fmt.Sprintf("    workspace_root: %s\n", cfg.WorkspaceRoot))
	b.WriteString("\n")

	b.WriteString("  " + planHeaderStyle.Render("GitHub orgs") + "\n")
	if len(cfg.GithubOrgs) == 0 {
		b.WriteString("    " + warnStyle.Render("(none — required)") + "\n")
	} else {
		for _, o := range cfg.GithubOrgs {
			b.WriteString("    • " + o + "\n")
		}
	}
	b.WriteString("\n")

	b.WriteString("  " + planHeaderStyle.Render("Claude backend") + "\n")
	b.WriteString(fmt.Sprintf("    backend: %s\n", cfg.ClaudeBackend))
	b.WriteString(fmt.Sprintf("    model:   %s\n", cfg.ClaudeModel))
	b.WriteString("\n")

	b.WriteString("  " + planHeaderStyle.Render("Secrets") + "\n")
	if cfg.Passwords.Manager == "" {
		b.WriteString("    " + hintStyle.Render("(none — every secret covered by env vars)") + "\n")
	} else {
		b.WriteString(fmt.Sprintf("    manager: %s\n", cfg.Passwords.Manager))
		if cfg.Passwords.ShortcutTokenRef != "" {
			b.WriteString(fmt.Sprintf("    shortcut token: %s\n", cfg.Passwords.ShortcutTokenRef))
		} else {
			b.WriteString("    shortcut token: " + hintStyle.Render("(env var $SHORTCUT_API_TOKEN)") + "\n")
		}
		if cfg.ClaudeBackend == agentBackendAPI {
			if cfg.Passwords.AnthropicKeyRef != "" {
				b.WriteString(fmt.Sprintf("    anthropic key:  %s\n", cfg.Passwords.AnthropicKeyRef))
			} else {
				b.WriteString("    anthropic key:  " + hintStyle.Render("(env var $ANTHROPIC_API_KEY)") + "\n")
			}
		}
	}
	b.WriteString("\n")

	b.WriteString("  " + createBtnStyle.Render("Confirm and save") + "\n")
	b.WriteString("  " + hintStyle.Render("press enter to write this config to ~/.config/thicket/config.toml") + "\n")
	return indent(b.String(), 2)
}
