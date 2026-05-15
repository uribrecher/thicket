// Repo-selection bubbletea view: search box + live fuzzy matches with
// LLM picks pre-loaded into the selection. Empty-query matches show
// the current selection so Enter can drop entries the user can't
// remember the exact name of.
package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/detector"
)

const maxMatches = 8

// ----- styles -----

var (
	headerStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	SectionStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	HighlightStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
	selectedTag    = lipgloss.NewStyle().Foreground(lipgloss.Color("76"))
	llmTag         = lipgloss.NewStyle().Foreground(lipgloss.Color("99"))
	DimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	WarnStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	CursorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
)

// ----- model -----

type pickerModel struct {
	// Static inputs
	repos      []catalog.Repo
	names      []string // catalog order; fuzzy.Find re-ranks per query
	nameSet    map[string]bool
	descByName map[string]string
	picks      map[string]detector.RepoMatch
	pickOrder  []string // order LLM returned the picks in, for default selection

	// Mutable state
	selected      map[string]bool
	selectedOrder []string
	input         textinput.Model
	matches       []string
	cursor        int
	status        string
	finished      bool
	cancelled     bool
}

func newPickerModel(cat []catalog.Repo, picks []detector.RepoMatch) pickerModel {
	ti := textinput.New()
	ti.Placeholder = "type to fuzzy-search · ↑/↓ navigate · enter toggle · tab finish"
	ti.Focus()
	ti.CharLimit = 80
	ti.Prompt = "› "

	m := pickerModel{
		repos:      cat,
		names:      make([]string, 0, len(cat)),
		nameSet:    make(map[string]bool, len(cat)),
		descByName: make(map[string]string, len(cat)),
		picks:      make(map[string]detector.RepoMatch, len(picks)),
		selected:   map[string]bool{},
		input:      ti,
	}
	for _, r := range cat {
		m.names = append(m.names, r.Name)
		m.nameSet[r.Name] = true
		m.descByName[r.Name] = r.Description
	}
	for _, p := range picks {
		m.picks[p.Name] = p
		m.pickOrder = append(m.pickOrder, p.Name)
		if m.nameSet[p.Name] && !m.selected[p.Name] {
			m.selected[p.Name] = true
			m.selectedOrder = append(m.selectedOrder, p.Name)
		}
	}
	m.recomputeMatches()
	return m
}

func (pickerModel) Init() tea.Cmd { return textinput.Blink }

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down":
			if m.cursor < len(m.matches)-1 {
				m.cursor++
			}
			return m, nil
		case "enter":
			// Enter always toggles the row under the cursor — both in
			// the selection view (empty input, listing currently
			// selected repos) and in the search view (typed input,
			// listing fuzzy matches). The previous "empty input +
			// Enter = finish" overload meant the user could move the
			// cursor onto a selected repo, press Enter expecting to
			// drop it, and instead silently finish with that repo
			// still in the workspace — a real footgun. Use Tab to
			// finish instead.
			if m.cursor < len(m.matches) {
				m.toggle(m.matches[m.cursor])
				m.input.SetValue("")
				m.status = ""
				m.recomputeMatches()
			} else if strings.TrimSpace(m.input.Value()) != "" {
				m.status = fmt.Sprintf("no match for %q", m.input.Value())
			}
			return m, nil
		case "tab":
			// Tab finishes with the current selection. Needs at least
			// one repo, otherwise we'd just produce a "no repos
			// selected" error one screen later — better to keep the
			// user in the picker.
			if len(m.selectedOrder) == 0 {
				m.status = "select at least one repo before finishing (or esc to cancel)"
				return m, nil
			}
			m.finished = true
			return m, tea.Quit
		}
	}
	// Only re-run the fuzzy matcher when the input text actually
	// changed — arrow keys / focus events route through textinput too
	// but leave the query alone.
	prev := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != prev {
		m.recomputeMatches()
	}
	m.status = ""
	return m, cmd
}

func (m *pickerModel) toggle(name string) {
	if m.selected[name] {
		delete(m.selected, name)
		m.selectedOrder = removeFromSlice(m.selectedOrder, name)
		m.status = "− dropped " + name
		return
	}
	m.selected[name] = true
	m.selectedOrder = append(m.selectedOrder, name)
	m.status = "+ added " + name
}

// recomputeMatches refreshes the visible match list from the current
// input value. With an empty query we show the current selection so
// the user can navigate to one and Enter to drop. With a query we run
// the fuzzy matcher.
func (m *pickerModel) recomputeMatches() {
	q := strings.TrimSpace(m.input.Value())
	m.matches = m.matches[:0]
	if q == "" {
		m.matches = append(m.matches, m.selectedOrder...)
	} else {
		for i, mm := range fuzzy.Find(q, m.names) {
			if i >= maxMatches {
				break
			}
			m.matches = append(m.matches, mm.Str)
		}
	}
	if m.cursor >= len(m.matches) {
		m.cursor = 0
	}
}

// ----- view -----

func (m pickerModel) View() string {
	var b strings.Builder

	b.WriteString(headerStyle.Render("Select repos for this workspace"))
	b.WriteString("\n\n")

	// Selection section
	b.WriteString(SectionStyle.Render(fmt.Sprintf("Selected (%d)", len(m.selectedOrder))))
	b.WriteString("\n")
	if len(m.selectedOrder) == 0 {
		b.WriteString("  " + DimStyle.Render("none yet — type below to add"))
		b.WriteString("\n")
	} else {
		b.WriteString(m.renderSelectedTable())
	}
	b.WriteString("\n")

	// Input + matches
	b.WriteString(SectionStyle.Render("Search"))
	b.WriteString("\n")
	b.WriteString("  " + m.input.View())
	b.WriteString("\n")
	if len(m.matches) > 0 {
		isSelectionView := strings.TrimSpace(m.input.Value()) == ""
		if isSelectionView {
			b.WriteString("  " + DimStyle.Render("(showing current selection — enter drops the highlighted row, tab finishes)"))
		} else {
			b.WriteString("  " + DimStyle.Render(fmt.Sprintf("%d match(es) — enter to toggle", len(m.matches))))
		}
		b.WriteString("\n")
		b.WriteString(m.renderMatchTable())
	}

	if m.status != "" {
		b.WriteString("\n")
		b.WriteString("  " + WarnStyle.Render(m.status))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(DimStyle.Render("  ↑/↓ navigate · enter toggle · tab finish · esc cancel"))
	b.WriteString("\n")
	return b.String()
}

// renderSelectedTable lays out the current selection as a two-column
// view: name on the left, why-it's-here on the right.
func (m pickerModel) renderSelectedTable() string {
	const nameW = 38
	const reasonW = 70
	var b strings.Builder
	for _, n := range m.selectedOrder {
		mark := selectedTag.Render("✓")
		name := PadRight(n, nameW)
		var reason string
		if p, ok := m.picks[n]; ok && p.Reason != "" {
			reason = llmTag.Render(fmt.Sprintf("LLM %.0f%% ", p.Confidence*100)) +
				Truncate(p.Reason, reasonW-12)
		} else if d := m.descByName[n]; d != "" {
			reason = DimStyle.Render(Truncate(d, reasonW))
		}
		b.WriteString(fmt.Sprintf("  %s %s %s\n", mark, name, reason))
	}
	return b.String()
}

// renderMatchTable shows live matches with a cursor marker, repo
// description, and a "(selected)" tag if already chosen.
func (m pickerModel) renderMatchTable() string {
	const nameW = 38
	const descW = 70
	var b strings.Builder
	for i, n := range m.matches {
		var marker, name string
		if i == m.cursor {
			marker = CursorStyle.Render("▶")
			name = HighlightStyle.Render(PadRight(n, nameW))
		} else {
			marker = " "
			name = PadRight(n, nameW)
		}
		tail := ""
		if m.selected[n] {
			tail = selectedTag.Render(" (selected)")
		}
		desc := ""
		if d := m.descByName[n]; d != "" {
			desc = DimStyle.Render(Truncate(d, descW))
		}
		b.WriteString(fmt.Sprintf("  %s %s %s%s\n", marker, name, desc, tail))
	}
	return b.String()
}

// ----- public entry point used by HuhSelector -----

func runPicker(cat []catalog.Repo, picks []detector.RepoMatch) ([]string, error) {
	final, err := tea.NewProgram(newPickerModel(cat, picks)).Run()
	if err != nil {
		return nil, err
	}
	m := final.(pickerModel)
	if m.cancelled {
		return nil, ErrCancelled
	}
	if !m.finished {
		return nil, ErrCancelled
	}
	if len(m.selectedOrder) == 0 {
		return nil, errors.New("no repos selected — nothing to do")
	}
	// Return in catalog order for downstream predictability.
	out := make([]string, 0, len(m.selectedOrder))
	for _, r := range cat {
		if m.selected[r.Name] {
			out = append(out, r.Name)
		}
	}
	return out, nil
}
