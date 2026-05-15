package config

import (
	"github.com/uribrecher/thicket/internal/tui/wizard"

	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// submitPage shows the full config summary and a [ Confirm ]
// button. Enter on the button emits wizard.ConfigDoneMsg{}; the wizard's
// handler stashes the populated config into m.ConfigResult and quits.
// The actual file write happens in cmd/thicket/init.go afterwards.
type submitPage struct{}

func newSubmitPage() *submitPage { return &submitPage{} }

func (p *submitPage) Title() string { return "Submit" }

func (p *submitPage) Hints() string { return "enter confirms · ← review previous pages" }

func (p *submitPage) Complete() bool { return true }

func (p *submitPage) Update(m *wizard.Model, msg tea.Msg) (wizard.Page, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "enter" {
		return p, func() tea.Msg { return wizard.ConfigDoneMsg{} }
	}
	return p, nil
}

func (p *submitPage) View(m *wizard.Model) string {
	cfg := m.ConfigDeps.Cfg
	var b strings.Builder
	b.WriteString(wizard.TitleStyle.Render("Review and confirm"))
	b.WriteString("\n\n")

	b.WriteString("  " + wizard.PlanHeaderStyle.Render("Paths") + "\n")
	b.WriteString(fmt.Sprintf("    repos_root:     %s\n", cfg.ReposRoot))
	b.WriteString(fmt.Sprintf("    workspace_root: %s\n", cfg.WorkspaceRoot))
	b.WriteString("\n")

	b.WriteString("  " + wizard.PlanHeaderStyle.Render("GitHub orgs") + "\n")
	if len(cfg.GithubOrgs) == 0 {
		b.WriteString("    " + wizard.WarnStyle.Render("(none — required)") + "\n")
	} else {
		for _, o := range cfg.GithubOrgs {
			b.WriteString("    • " + o + "\n")
		}
	}
	b.WriteString("\n")

	b.WriteString("  " + wizard.PlanHeaderStyle.Render("Claude backend") + "\n")
	b.WriteString(fmt.Sprintf("    backend: %s\n", cfg.ClaudeBackend))
	b.WriteString(fmt.Sprintf("    model:   %s\n", cfg.ClaudeModel))
	b.WriteString("\n")

	b.WriteString("  " + wizard.PlanHeaderStyle.Render("Secrets") + "\n")
	if cfg.Passwords.Manager == "" {
		b.WriteString("    " + wizard.HintStyle.Render("(none — every secret covered by env vars)") + "\n")
	} else {
		b.WriteString(fmt.Sprintf("    Manager: %s\n", cfg.Passwords.Manager))
		if cfg.Passwords.ShortcutTokenRef != "" {
			b.WriteString(fmt.Sprintf("    shortcut token: %s\n", cfg.Passwords.ShortcutTokenRef))
		} else {
			b.WriteString("    shortcut token: " + wizard.HintStyle.Render("(env var $SHORTCUT_API_TOKEN)") + "\n")
		}
		if cfg.ClaudeBackend == agentBackendAPI {
			if cfg.Passwords.AnthropicKeyRef != "" {
				b.WriteString(fmt.Sprintf("    anthropic key:  %s\n", cfg.Passwords.AnthropicKeyRef))
			} else {
				b.WriteString("    anthropic key:  " + wizard.HintStyle.Render("(env var $ANTHROPIC_API_KEY)") + "\n")
			}
		}
	}
	b.WriteString("\n")

	b.WriteString("  " + wizard.CreateBtnStyle.Render("Confirm and save") + "\n")
	b.WriteString("  " + wizard.HintStyle.Render("press enter to write this config to ~/.config/thicket/config.toml") + "\n")
	return wizard.Indent(b.String(), 2)
}
