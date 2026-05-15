package edit

import (
	"github.com/uribrecher/thicket/internal/tui/wizard"

	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/uribrecher/thicket/internal/catalog"
)

// matchItem is one row in the navigable match list. Locked
// rows (already-in-workspace) are NOT part of this list — they're
// rendered as a static info block above the search input so the
// cursor never has to step through them.
type matchItem struct {
	name     string
	selected bool // user has picked this as an addition
}

type reposPage struct {
	loadedForID string // slug of the workspace this state belongs to

	// Static seeded data.
	repos      []catalog.Repo
	names      []string
	nameSet    map[string]bool
	descByName map[string]string
	locked     map[string]bool // names already in the workspace

	// Mutable selection (the additions).
	selected      map[string]bool
	selectedOrder []string

	input   textinput.Model
	matches []matchItem
	cursor  int
	status  string
}

func newReposPage() *reposPage {
	ti := textinput.New()
	ti.Placeholder = "type to filter the catalog"
	ti.Focus()
	ti.CharLimit = 80
	ti.Width = 60
	ti.Prompt = "› "
	return &reposPage{
		input:    ti,
		selected: make(map[string]bool),
	}
}

func (p *reposPage) Title() string { return "Repos" }

func (p *reposPage) Hints() string {
	return "↑/↓ navigate · enter toggles"
}

func (p *reposPage) Complete() bool { return len(p.selectedOrder) > 0 }

func (p *reposPage) InitCmd(m *wizard.Model) tea.Cmd {
	if m.SelectedWorkspace == nil {
		return nil
	}
	slug := m.SelectedWorkspace.Slug
	if p.loadedForID == slug {
		return nil
	}
	p.resetFor(m)
	p.loadedForID = slug
	p.recompute()
	return nil
}

func (p *reposPage) resetFor(m *wizard.Model) {
	p.repos = m.EditDeps.Repos
	p.names = make([]string, 0, len(p.repos))
	p.nameSet = make(map[string]bool, len(p.repos))
	p.descByName = make(map[string]string, len(p.repos))
	for _, r := range p.repos {
		p.names = append(p.names, r.Name)
		p.nameSet[r.Name] = true
		p.descByName[r.Name] = r.Description
	}
	p.locked = make(map[string]bool)
	for _, r := range m.SelectedWorkspace.State.Repos {
		p.locked[r.Name] = true
	}
	// Carry forward additions if the user navigated back & forward
	// (m.Additions is the wizard-shared store).
	p.selected = make(map[string]bool, len(m.Additions))
	p.selectedOrder = p.selectedOrder[:0]
	for _, r := range m.Additions {
		if !p.locked[r.Name] && p.nameSet[r.Name] {
			p.selected[r.Name] = true
			p.selectedOrder = append(p.selectedOrder, r.Name)
		}
	}
	p.matches = p.matches[:0]
	p.cursor = 0
	p.status = ""
	p.input.SetValue("")
}

func (p *reposPage) Update(m *wizard.Model, msg tea.Msg) (wizard.Page, tea.Cmd) {
	switch v := msg.(type) {
	case wizard.GoNextMsg:
		if !p.Complete() {
			return p, nil
		}
		chosen := make([]catalog.Repo, 0, len(p.selectedOrder))
		for _, r := range p.repos {
			if p.selected[r.Name] {
				chosen = append(chosen, r)
			}
		}
		// Update m.Additions synchronously — same reason as the start
		// flow's Repos page: the wizard's advance() fires the Submit
		// page's InitCmd immediately after this returns, and that
		// InitCmd reads m.Additions.
		m.Additions = append(m.Additions[:0], chosen...)
		return p, func() tea.Msg { return wizard.AdditionsCommittedMsg{Additions: chosen} }

	case tea.KeyMsg:
		switch v.String() {
		case "up":
			if p.cursor > 0 {
				p.cursor--
			}
			return p, nil
		case "down":
			if p.cursor < len(p.matches)-1 {
				p.cursor++
			}
			return p, nil
		case "enter":
			if p.cursor >= len(p.matches) {
				if q := strings.TrimSpace(p.input.Value()); q != "" {
					p.status = fmt.Sprintf("no match for %q", q)
				}
				return p, nil
			}
			it := p.matches[p.cursor]
			p.toggle(it.name)
			p.input.SetValue("")
			p.recompute()
			for i, mm := range p.matches {
				if mm.name == it.name {
					p.cursor = i
					break
				}
			}
			return p, nil
		}
	}

	prev := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != prev {
		p.recompute()
	}
	p.status = ""
	return p, cmd
}

func (p *reposPage) toggle(name string) {
	if p.selected[name] {
		delete(p.selected, name)
		p.selectedOrder = wizard.RemoveFromSlice(p.selectedOrder, name)
		p.status = "− dropped " + name
		return
	}
	p.selected[name] = true
	p.selectedOrder = append(p.selectedOrder, name)
	p.status = "+ added " + name
}

// recompute rebuilds the NAVIGABLE list — Selected additions (top)
// followed by fuzzy-Available results when the user is typing. The
// already-in-workspace ("locked") block is informational only; it
// renders as a static header above the search input in View() and
// is deliberately excluded from p.matches so the cursor never has to
// arrow past it to reach an actual choice.
func (p *reposPage) recompute() {
	p.matches = p.matches[:0]
	// Selected (additions). Preserve insertion order.
	for _, n := range p.selectedOrder {
		p.matches = append(p.matches, matchItem{name: n, selected: true})
	}
	// Available fuzzy matches when querying.
	q := strings.TrimSpace(p.input.Value())
	if q != "" {
		count := 0
		for _, mm := range wizard.RankFuzzy(q, p.names) {
			if p.locked[mm.Str] || p.selected[mm.Str] {
				continue
			}
			p.matches = append(p.matches, matchItem{name: mm.Str})
			count++
			if count >= wizard.MaxRepoMatches {
				break
			}
		}
	}
	if p.cursor >= len(p.matches) {
		p.cursor = 0
	}
}

func (p *reposPage) View(m *wizard.Model) string {
	var b strings.Builder
	b.WriteString(wizard.TitleStyle.Render("Add repos to this workspace"))
	b.WriteString("\n\n")

	if m.SelectedWorkspace != nil {
		b.WriteString(renderWorkspaceSummary(*m.SelectedWorkspace))
		b.WriteString("\n")
	}

	// Locked block first — informational, NOT navigable. The cursor
	// arrows operate only on the match list below the search.
	if len(p.locked) > 0 {
		b.WriteString("  " + wizard.SectionStyle.Render(fmt.Sprintf("Already in workspace (%d)", len(p.locked))) + "\n")
		lockedNames := make([]string, 0, len(p.locked))
		for n := range p.locked {
			lockedNames = append(lockedNames, n)
		}
		sort.Strings(lockedNames)
		for _, n := range lockedNames {
			b.WriteString("      " + wizard.DimStyle.Render("· "+n) + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("  " + p.input.View() + "\n\n")
	b.WriteString(p.renderMatches())

	if p.status != "" {
		b.WriteString("\n  " + wizard.WarnStyle.Render(p.status) + "\n")
	}
	return wizard.Indent(b.String(), 2)
}

func (p *reposPage) renderMatches() string {
	if len(p.matches) == 0 {
		return ""
	}
	const nameW = 38
	const descW = 70
	var b strings.Builder
	prevGroup := -1
	for i, it := range p.matches {
		g := 0
		if it.selected {
			g = 0
		} else {
			g = 1
		}
		if g != prevGroup {
			if prevGroup != -1 {
				b.WriteString("\n")
			}
			label := "Available"
			if g == 0 {
				label = fmt.Sprintf("Adding (%d)", len(p.selectedOrder))
			}
			b.WriteString("  " + wizard.SectionStyle.Render(label) + "\n")
			prevGroup = g
		}

		marker, name := " ", wizard.PadRight(it.name, nameW)
		if i == p.cursor {
			marker = wizard.CursorStyle.Render("▶")
			name = wizard.HighlightStyle.Render(wizard.PadRight(it.name, nameW))
		}

		check := " "
		if it.selected {
			check = wizard.SelectedTagStyle.Render("✓")
		}

		var meta string
		if d := p.descByName[it.name]; d != "" {
			meta = wizard.DimStyle.Render(wizard.Truncate(d, descW))
		}
		b.WriteString(fmt.Sprintf("    %s %s %s %s\n", marker, check, name, meta))
	}
	return b.String()
}
