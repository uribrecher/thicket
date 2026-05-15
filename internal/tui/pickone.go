// Tableized single-row picker with fuzzy filter. Title + search input
// + tabular match list; ↑/↓ to navigate, Enter to pick.
package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"
)

// Column describes one table column.
type Column struct {
	Title string
	Width int // visible width; cells longer than this are truncated
}

// Row is one entry in the picker's table.
type Row struct {
	// Key is returned to the caller when this row is picked.
	Key string
	// Cells provides the values rendered in each Column, in order.
	Cells []string
	// Filter is the text used for fuzzy matching. Empty falls back to
	// the cells joined by spaces.
	Filter string
}

// PickOneOption configures PickOne. Empty / zero-value fields use
// PickOne's defaults.
type PickOneOption struct {
	// InitialQuery pre-fills the search input so the picker opens
	// already filtered. Useful for commands like `thicket rm <slug>`
	// where the user typed something the caller couldn't match exactly.
	InitialQuery string
}

// PickOne shows the picker and returns the Key of the chosen row, or
// "" with ErrCancelled if the user pressed Esc / Ctrl-C.
func PickOne(title string, columns []Column, rows []Row, opts ...PickOneOption) (string, error) {
	var o PickOneOption
	if len(opts) > 0 {
		o = opts[0]
	}
	m := newPickOneModel(title, columns, rows)
	if o.InitialQuery != "" {
		m.input.SetValue(o.InitialQuery)
		m.recompute()
	}
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return "", err
	}
	pm := final.(pickOneModel)
	if pm.cancelled {
		return "", ErrCancelled
	}
	if pm.picked == "" {
		return "", errors.New("no row picked")
	}
	return pm.picked, nil
}

// ----- model -----

const pickOneVisibleRows = 12

type pickOneModel struct {
	title   string
	columns []Column
	rows    []Row

	// Pre-built fuzzy haystack: same order as rows.
	haystack []string

	input   textinput.Model
	matches []int // indexes into rows
	cursor  int   // index into matches

	picked    string
	cancelled bool
}

func newPickOneModel(title string, columns []Column, rows []Row) pickOneModel {
	ti := textinput.New()
	ti.Placeholder = "type to filter…"
	ti.Focus()
	ti.CharLimit = 80
	// textinput.New() defaults Width to 0; at Width=0, bubbles'
	// placeholderView only renders the first character of the
	// placeholder (so "type to filter…" collapses to "t"). The
	// edit/start wizard text inputs carry the same workaround.
	ti.Width = 60
	ti.Prompt = "› "

	m := pickOneModel{
		title:    title,
		columns:  columns,
		rows:     rows,
		haystack: make([]string, len(rows)),
		input:    ti,
	}
	for i, r := range rows {
		if r.Filter != "" {
			m.haystack[i] = r.Filter
		} else {
			m.haystack[i] = strings.Join(r.Cells, " ")
		}
	}
	m.recompute()
	return m
}

func (pickOneModel) Init() tea.Cmd { return textinput.Blink }

func (m pickOneModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			if m.cursor < len(m.matches) {
				m.picked = m.rows[m.matches[m.cursor]].Key
				return m, tea.Quit
			}
			return m, nil
		}
	}
	// Only re-run the fuzzy matcher when the input text actually
	// changed — non-text events still route through textinput.Update.
	prev := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != prev {
		m.recompute()
	}
	return m, cmd
}

func (m *pickOneModel) recompute() {
	q := strings.TrimSpace(m.input.Value())
	m.matches = m.matches[:0]
	if q == "" {
		for i := range m.rows {
			if i >= pickOneVisibleRows {
				break
			}
			m.matches = append(m.matches, i)
		}
	} else {
		fm := fuzzy.Find(q, m.haystack)
		for i, mm := range fm {
			if i >= pickOneVisibleRows {
				break
			}
			m.matches = append(m.matches, mm.Index)
		}
	}
	if m.cursor >= len(m.matches) {
		m.cursor = 0
	}
}

// ----- view -----

var (
	pickOneTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	pickOneHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	pickOneRowStyle    = lipgloss.NewStyle()
	pickOneCurStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
	pickOneHintStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Italic(true)
)

func (m pickOneModel) View() string {
	var b strings.Builder

	b.WriteString(pickOneTitleStyle.Render(m.title))
	b.WriteString("\n\n")

	b.WriteString("  " + m.input.View())
	b.WriteString("\n")
	q := strings.TrimSpace(m.input.Value())
	switch {
	case q == "":
		b.WriteString("  " + pickOneHintStyle.Render(fmt.Sprintf("showing first %d of %d — type to filter",
			len(m.matches), len(m.rows))))
	case len(m.matches) == 0:
		b.WriteString("  " + pickOneHintStyle.Render(fmt.Sprintf("no match for %q", q)))
	default:
		b.WriteString("  " + pickOneHintStyle.Render(fmt.Sprintf("%d match(es)", len(m.matches))))
	}
	b.WriteString("\n\n")

	// Header
	b.WriteString("   ")
	for _, c := range m.columns {
		b.WriteString(pickOneHeaderStyle.Render(PadRight(c.Title, c.Width)))
		b.WriteString("  ")
	}
	b.WriteString("\n")
	// Underline row
	b.WriteString("   ")
	for _, c := range m.columns {
		b.WriteString(pickOneHintStyle.Render(strings.Repeat("─", c.Width)))
		b.WriteString("  ")
	}
	b.WriteString("\n")

	// Rows
	for vi, ri := range m.matches {
		row := m.rows[ri]
		marker := " "
		style := pickOneRowStyle
		if vi == m.cursor {
			marker = pickOneCurStyle.Render("▶")
			style = pickOneCurStyle
		}
		b.WriteString(marker + "  ")
		for i, cell := range row.Cells {
			if i >= len(m.columns) {
				break
			}
			b.WriteString(style.Render(PadRight(Truncate(cell, m.columns[i].Width), m.columns[i].Width)))
			b.WriteString("  ")
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString("  " + pickOneHintStyle.Render("↑/↓ navigate · enter pick · esc cancel"))
	b.WriteString("\n")
	return b.String()
}
