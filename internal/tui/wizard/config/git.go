package config

import (
	"github.com/uribrecher/thicket/internal/tui/wizard"

	"fmt"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// gitPage collects the three values that anchor thicket on disk:
//   - repos_root: where source clones live
//   - workspace_root: where new workspaces get created
//   - github_orgs: list of orgs to scan for repos
//
// The first two are plain textinputs. The third has two modes:
//   - "text": a CSV textinput, used as the fallback when `gh api
//     user/orgs` fails, returns nothing, or returns exactly one org
//     (which is auto-filled into the textinput).
//   - "picker": a checkbox multiselect, used when gh returns 2+
//     orgs. Space toggles the cursor row; the underlying textinput
//     is kept in sync so the rest of the page (Complete, commit)
//     doesn't need to care which mode we're in.
type gitPage struct {
	inputs [3]textinput.Model
	focus  int

	// Orgs probe state.
	probeStarted bool  // InitCmd fires the probe only once per wizard run
	probeDone    bool  // success or failure — used to suppress the "checking gh…" hint
	probeErr     error // surfaced as a small dim line beneath the orgs field
	availOrgs    []string
	selOrgs      map[string]bool
	orgCursor    int
}

const (
	gitFieldReposRoot     = 0
	gitFieldWorkspaceRoot = 1
	gitFieldOrgs          = 2
)

// listUserOrgs is the gh-probe seam. Tests can swap it via the
// package-level variable below. Default impl shells out to `gh api
// user/orgs --jq '.[].login'` and splits the newline-separated
// output. Empty result + nil error is the legitimate "user belongs
// to no orgs" case.
var listUserOrgs = func() ([]string, error) {
	out, err := exec.Command("gh", "api", "user/orgs", "--jq", ".[].login").Output()
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	var orgs []string
	for _, line := range strings.Split(raw, "\n") {
		if o := strings.TrimSpace(line); o != "" {
			orgs = append(orgs, o)
		}
	}
	return orgs, nil
}

// orgsPickerActive reports whether the orgs section is showing the
// multiselect (rather than the plain textinput).
func (p *gitPage) orgsPickerActive() bool {
	return len(p.availOrgs) >= 2
}

func newGitPage() *gitPage {
	p := &gitPage{}
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
	p.selOrgs = make(map[string]bool)
	return p
}

// InitCmd seeds each input from the working config the first time
// the page is activated and fires the gh-orgs probe so the orgs
// field can auto-populate. Both happen on first activation only —
// re-entering the page (e.g. via ← from the next page) keeps
// whatever the user has typed / toggled.
func (p *gitPage) InitCmd(m *wizard.Model) tea.Cmd {
	if p.inputs[gitFieldReposRoot].Value() == "" {
		p.inputs[gitFieldReposRoot].SetValue(m.ConfigDeps.Cfg.ReposRoot)
	}
	if p.inputs[gitFieldWorkspaceRoot].Value() == "" {
		p.inputs[gitFieldWorkspaceRoot].SetValue(m.ConfigDeps.Cfg.WorkspaceRoot)
	}
	if p.inputs[gitFieldOrgs].Value() == "" {
		p.inputs[gitFieldOrgs].SetValue(strings.Join(m.ConfigDeps.Cfg.GithubOrgs, ", "))
	}
	p.inputs[p.focus].Focus()
	var cmds []tea.Cmd
	cmds = append(cmds, textinput.Blink)
	if !p.probeStarted {
		p.probeStarted = true
		cmds = append(cmds, loadUserOrgsCmd())
	}
	return tea.Batch(cmds...)
}

// loadUserOrgsCmd runs the gh probe off the bubbletea goroutine and
// emits ConfigOrgsLoadedMsg.
func loadUserOrgsCmd() tea.Cmd {
	return func() tea.Msg {
		orgs, err := listUserOrgs()
		return wizard.ConfigOrgsLoadedMsg{Orgs: orgs, Err: err}
	}
}

func (p *gitPage) Title() string { return "Git" }

func (p *gitPage) Hints() string {
	if p.orgsPickerActive() && p.focus == gitFieldOrgs {
		return "↑/↓ moves cursor · space toggles · tab cycles fields · enter continues"
	}
	return "tab cycles fields · enter continues"
}

// Complete requires all three fields non-empty. The wizard's Validate
// step (post-wizard) will catch malformed paths.
func (p *gitPage) Complete() bool {
	return strings.TrimSpace(p.inputs[gitFieldReposRoot].Value()) != "" &&
		strings.TrimSpace(p.inputs[gitFieldWorkspaceRoot].Value()) != "" &&
		strings.TrimSpace(p.inputs[gitFieldOrgs].Value()) != ""
}

func (p *gitPage) Update(m *wizard.Model, msg tea.Msg) (wizard.Page, tea.Cmd) {
	if v, ok := msg.(wizard.ConfigOrgsLoadedMsg); ok {
		p.onOrgsLoaded(v)
		return p, nil
	}

	if k, ok := msg.(tea.KeyMsg); ok {
		// Picker-mode key handling first: only when the orgs field
		// is focused AND the picker is active do up/down/space
		// affect the picker instead of cycling fields.
		if p.focus == gitFieldOrgs && p.orgsPickerActive() {
			switch k.String() {
			case "up", "k":
				if p.orgCursor > 0 {
					p.orgCursor--
				}
				return p, nil
			case "down", "j":
				if p.orgCursor < len(p.availOrgs)-1 {
					p.orgCursor++
				}
				return p, nil
			case " ":
				name := p.availOrgs[p.orgCursor]
				p.selOrgs[name] = !p.selOrgs[name]
				p.syncOrgsTextFromPicker()
				return p, nil
			}
		}
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
				return p, func() tea.Msg { return wizard.GoNextMsg{} }
			}
			return p, nil
		}
	}

	if _, ok := msg.(wizard.GoNextMsg); ok {
		p.commit(m)
		return p, nil
	}

	// Forward to the focused textinput — but only when we're not
	// rendering the picker for that field (picker mode swallows
	// space/up/down above; printable keys still fall through here
	// and would otherwise land in the now-hidden textinput, which
	// is harmless but pointless).
	if p.focus == gitFieldOrgs && p.orgsPickerActive() {
		return p, nil
	}
	var cmd tea.Cmd
	p.inputs[p.focus], cmd = p.inputs[p.focus].Update(msg)
	return p, cmd
}

// onOrgsLoaded reacts to the gh-probe result. Empty / error: no
// change to the textinput (manual entry path). One org: auto-fill
// the textinput. 2+: flip into picker mode with all orgs checked by
// default, and sync the textinput so Complete() sees the value.
func (p *gitPage) onOrgsLoaded(v wizard.ConfigOrgsLoadedMsg) {
	p.probeDone = true
	p.probeErr = v.Err
	if v.Err != nil || len(v.Orgs) == 0 {
		return
	}
	if len(v.Orgs) == 1 {
		// Only overwrite if the field is empty / unchanged — don't
		// stomp a value the user typed during the probe race.
		if strings.TrimSpace(p.inputs[gitFieldOrgs].Value()) == "" {
			p.inputs[gitFieldOrgs].SetValue(v.Orgs[0])
		}
		return
	}
	// 2+ orgs → picker. Default to all selected. If the textinput
	// already has values (re-running config, or from cfg.GithubOrgs
	// pre-seed), respect that as the initial selection.
	p.availOrgs = append(p.availOrgs[:0], v.Orgs...)
	preseed := splitOrgs(p.inputs[gitFieldOrgs].Value())
	preseedSet := make(map[string]bool, len(preseed))
	for _, o := range preseed {
		preseedSet[o] = true
	}
	for _, o := range v.Orgs {
		if len(preseedSet) > 0 {
			p.selOrgs[o] = preseedSet[o]
		} else {
			p.selOrgs[o] = true
		}
	}
	p.syncOrgsTextFromPicker()
}

// syncOrgsTextFromPicker rewrites the orgs textinput from the
// current checkbox state so Complete() and commit() — both reading
// the textinput value — stay agnostic of which mode we're in.
func (p *gitPage) syncOrgsTextFromPicker() {
	var picked []string
	for _, o := range p.availOrgs {
		if p.selOrgs[o] {
			picked = append(picked, o)
		}
	}
	p.inputs[gitFieldOrgs].SetValue(strings.Join(picked, ", "))
}

func (p *gitPage) cycleFocus(d int) {
	p.inputs[p.focus].Blur()
	p.focus = (p.focus + d + len(p.inputs)) % len(p.inputs)
	p.inputs[p.focus].Focus()
}

// commit writes the current input values back to the working config.
// Called on Enter and on wizard.GoNextMsg so a back/forward dance preserves
// the user's edits.
func (p *gitPage) commit(m *wizard.Model) {
	m.ConfigDeps.Cfg.ReposRoot = strings.TrimSpace(p.inputs[gitFieldReposRoot].Value())
	m.ConfigDeps.Cfg.WorkspaceRoot = strings.TrimSpace(p.inputs[gitFieldWorkspaceRoot].Value())
	m.ConfigDeps.Cfg.GithubOrgs = splitOrgs(p.inputs[gitFieldOrgs].Value())
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

func (p *gitPage) View(m *wizard.Model) string {
	var b strings.Builder
	b.WriteString(wizard.TitleStyle.Render("Where do your repos live?"))
	b.WriteString("\n\n")

	labels := []string{
		"Source clones (repos_root)",
		"Workspaces (workspace_root)",
		"GitHub orgs to scan",
	}
	hints := []string{
		"Where `thicket start` looks for already-cloned repos and clones new ones into.",
		"Where new workspaces (one folder per ticket) get created.",
		"The GitHub orgs whose repos `thicket start` lists as candidates.",
	}
	for i, label := range labels {
		marker := "  "
		if i == p.focus {
			marker = wizard.CursorStyle.Render("▶ ")
		}
		b.WriteString(marker + wizard.SectionStyle.Render(label) + "\n")
		if i == gitFieldOrgs {
			p.renderOrgsField(&b)
		} else {
			b.WriteString("    " + p.inputs[i].View() + "\n")
		}
		b.WriteString("    " + wizard.HintStyle.Render(hints[i]) + "\n\n")
	}

	if !p.Complete() {
		b.WriteString("  " + wizard.HintStyle.Render("(fill in all three to continue)") + "\n")
	} else {
		b.WriteString("  " + wizard.HintStyle.Render(fmt.Sprintf("ready — %d org(s) configured", len(splitOrgs(p.inputs[gitFieldOrgs].Value())))) + "\n")
	}
	_ = m
	return wizard.Indent(b.String(), 2)
}

// renderOrgsField paints whichever of the two modes is active —
// textinput (fallback / single-org auto-fill) or picker (2+ orgs).
// Caller is responsible for the section header and trailing hint
// line.
func (p *gitPage) renderOrgsField(b *strings.Builder) {
	switch {
	case p.orgsPickerActive():
		for i, name := range p.availOrgs {
			cursor := "  "
			if i == p.orgCursor && p.focus == gitFieldOrgs {
				cursor = wizard.CursorStyle.Render("▶ ")
			}
			check := "[ ]"
			style := wizard.DimStyle
			if p.selOrgs[name] {
				check = "[x]"
				style = wizard.HighlightStyle
			}
			b.WriteString("    " + cursor + style.Render(check+" "+name) + "\n")
		}
		b.WriteString("    " + wizard.HintStyle.Render(fmt.Sprintf("auto-detected via `gh api user/orgs` — %d of %d selected",
			p.countSelected(), len(p.availOrgs))) + "\n")
	default:
		b.WriteString("    " + p.inputs[gitFieldOrgs].View() + "\n")
		switch {
		case !p.probeDone:
			b.WriteString("    " + wizard.HintStyle.Render("checking gh for your org memberships…") + "\n")
		case p.probeErr != nil:
			b.WriteString("    " + wizard.HintStyle.Render("(couldn't reach gh — type orgs manually, comma-separated)") + "\n")
		case len(p.inputs[gitFieldOrgs].Value()) > 0 && p.probeDone:
			// One-org auto-fill case (or probe returned 0 and the
			// user has started typing) — keep the surface quiet.
		}
	}
}

func (p *gitPage) countSelected() int {
	n := 0
	for _, ok := range p.selOrgs {
		if ok {
			n++
		}
	}
	return n
}
