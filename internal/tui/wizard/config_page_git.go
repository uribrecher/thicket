package wizard

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// configGitPage collects the three values that anchor thicket on disk:
//   - repos_root: where source clones live
//   - workspace_root: where new workspaces get created
//   - github_orgs: comma-separated list of orgs to scan for repos
//
// We use plain textinputs (one per field) with tab to cycle focus.
// github_orgs is a simple CSV input rather than the multiselect the
// old huh-based init had — fewer moving parts in the bubbletea page
// and the user can edit the list later by re-running init.
type configGitPage struct {
	inputs [3]textinput.Model
	focus  int
}

const (
	gitFieldReposRoot     = 0
	gitFieldWorkspaceRoot = 1
	gitFieldOrgs          = 2
)

func newConfigGitPage() *configGitPage {
	p := &configGitPage{}
	for i := range p.inputs {
		ti := textinput.New()
		ti.CharLimit = 200
		ti.Width = 60
		ti.Prompt = "› "
		p.inputs[i] = ti
	}
	p.inputs[gitFieldReposRoot].Placeholder = "~/code"
	p.inputs[gitFieldWorkspaceRoot].Placeholder = "~/tasks"
	p.inputs[gitFieldOrgs].Placeholder = "my-org, other-org"
	return p
}

// initCmd seeds each input from the working config the first time the
// page is activated. We do this in initCmd (not the constructor) so
// late-bound config edits made by earlier pages still apply.
func (p *configGitPage) initCmd(m *Model) tea.Cmd {
	if p.inputs[gitFieldReposRoot].Value() == "" {
		p.inputs[gitFieldReposRoot].SetValue(m.configDeps.Cfg.ReposRoot)
	}
	if p.inputs[gitFieldWorkspaceRoot].Value() == "" {
		p.inputs[gitFieldWorkspaceRoot].SetValue(m.configDeps.Cfg.WorkspaceRoot)
	}
	if p.inputs[gitFieldOrgs].Value() == "" {
		p.inputs[gitFieldOrgs].SetValue(strings.Join(m.configDeps.Cfg.GithubOrgs, ", "))
	}
	p.inputs[p.focus].Focus()
	return textinput.Blink
}

func (p *configGitPage) Title() string { return "Git" }

func (p *configGitPage) Hints() string { return "tab cycles fields · enter continues" }

// Complete requires all three fields non-empty. The wizard's Validate
// step (post-wizard) will catch malformed paths.
func (p *configGitPage) Complete() bool {
	return strings.TrimSpace(p.inputs[gitFieldReposRoot].Value()) != "" &&
		strings.TrimSpace(p.inputs[gitFieldWorkspaceRoot].Value()) != "" &&
		strings.TrimSpace(p.inputs[gitFieldOrgs].Value()) != ""
}

func (p *configGitPage) Update(m *Model, msg tea.Msg) (Page, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "tab", "down":
			p.cycleFocus(1)
			return p, textinput.Blink
		case "shift+tab", "up":
			p.cycleFocus(-1)
			return p, textinput.Blink
		case "enter":
			p.commit(m)
			if p.Complete() {
				return p, func() tea.Msg { return goNextMsg{} }
			}
			return p, nil
		}
	}

	if _, ok := msg.(goNextMsg); ok {
		p.commit(m)
		return p, nil
	}

	var cmd tea.Cmd
	p.inputs[p.focus], cmd = p.inputs[p.focus].Update(msg)
	return p, cmd
}

func (p *configGitPage) cycleFocus(d int) {
	p.inputs[p.focus].Blur()
	p.focus = (p.focus + d + len(p.inputs)) % len(p.inputs)
	p.inputs[p.focus].Focus()
}

// commit writes the current input values back to the working config.
// Called on Enter and on goNextMsg so a back/forward dance preserves
// the user's edits.
func (p *configGitPage) commit(m *Model) {
	m.configDeps.Cfg.ReposRoot = strings.TrimSpace(p.inputs[gitFieldReposRoot].Value())
	m.configDeps.Cfg.WorkspaceRoot = strings.TrimSpace(p.inputs[gitFieldWorkspaceRoot].Value())
	m.configDeps.Cfg.GithubOrgs = splitOrgs(p.inputs[gitFieldOrgs].Value())
}

func splitOrgs(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func (p *configGitPage) View(m *Model) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Where do your repos live?"))
	b.WriteString("\n\n")

	labels := []string{
		"Source clones (repos_root)",
		"Workspaces (workspace_root)",
		"GitHub orgs to scan (comma-separated)",
	}
	hints := []string{
		"Where `thicket start` looks for already-cloned repos and clones new ones into.",
		"Where new workspaces (one folder per ticket) get created.",
		"The GitHub orgs whose repos `thicket start` lists as candidates.",
	}
	for i, label := range labels {
		marker := "  "
		if i == p.focus {
			marker = cursorStyle.Render("▶ ")
		}
		b.WriteString(marker + sectionStyle.Render(label) + "\n")
		b.WriteString("    " + p.inputs[i].View() + "\n")
		b.WriteString("    " + hintStyle.Render(hints[i]) + "\n\n")
	}

	if !p.Complete() {
		b.WriteString("  " + hintStyle.Render("(fill in all three to continue)") + "\n")
	} else {
		b.WriteString("  " + hintStyle.Render(fmt.Sprintf("ready — %d org(s) configured", len(splitOrgs(p.inputs[gitFieldOrgs].Value())))) + "\n")
	}
	_ = m
	return indent(b.String(), 2)
}
