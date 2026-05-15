package edit

import (
	"github.com/uribrecher/thicket/internal/tui/wizard"

	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sahilm/fuzzy"

	"github.com/uribrecher/thicket/internal/workspace"
)

const workspaceVisibleRows = 12

// workspaceRow is one prepared row in the workspace picker.
type workspaceRow struct {
	ws     workspace.ManagedWorkspace
	filter string
}

type workspacePage struct {
	loading bool
	loadErr error

	rows     []workspaceRow
	haystack []string

	input   textinput.Model
	matches []int
	cursor  int

	// committedID is the slug of the workspace the user picked on
	// this page. Setting it both flips Complete() to true and lets
	// the wizard's commit handler stash the full ManagedWorkspace
	// on the shared wizard.Model.
	committed *workspace.ManagedWorkspace
}

func newWorkspacePage() *workspacePage {
	ti := textinput.New()
	ti.Placeholder = "type to filter…"
	ti.Focus()
	ti.CharLimit = 80
	ti.Width = 60
	ti.Prompt = "› "
	return &workspacePage{input: ti, loading: true}
}

// preseed flips the page into preselected mode (used when the caller
// passed `thicket edit <slug>`). No list fetch, no picker — just a
// read-only summary the user can peek at via ←.
func (p *workspacePage) preseed(ws workspace.ManagedWorkspace) {
	p.loading = false
	p.rows = []workspaceRow{} // non-nil so InitCmd's guard treats us as "loaded"
	p.committed = &ws
}

func (p *workspacePage) Title() string { return "Workspace" }

func (p *workspacePage) Hints() string { return "↑/↓ navigate · enter picks" }

func (p *workspacePage) Complete() bool { return p.committed != nil }

// InitCmd fires the ListManaged call on first activation. Sync under
// the hood (file-system scan), but routed through a cmd so the page
// doesn't block render setup.
func (p *workspacePage) InitCmd(m *wizard.Model) tea.Cmd {
	if p.rows != nil || p.loadErr != nil {
		return nil
	}
	root := m.EditDeps.Cfg.WorkspaceRoot
	return func() tea.Msg {
		ws, _, err := workspace.ListManaged(root)
		return wizard.WorkspacesLoadedMsg{Workspaces: ws, Err: err}
	}
}

func (p *workspacePage) Update(m *wizard.Model, msg tea.Msg) (wizard.Page, tea.Cmd) {
	switch v := msg.(type) {
	case wizard.WorkspacesLoadedMsg:
		p.loading = false
		if v.Err != nil {
			p.loadErr = v.Err
			return p, nil
		}
		p.rows = make([]workspaceRow, len(v.Workspaces))
		p.haystack = make([]string, len(v.Workspaces))
		for i, ws := range v.Workspaces {
			f := ws.Slug + " " + ws.State.TicketID + " " + ws.State.Branch
			p.rows[i] = workspaceRow{ws: ws, filter: f}
			p.haystack[i] = f
		}
		p.recompute()
		return p, nil

	case wizard.GoNextMsg:
		if p.committed == nil {
			return p, nil
		}
		// Update shared state SYNCHRONOUSLY here — wizard.advance()
		// fires the Repos page's InitCmd immediately after this
		// returns, and that InitCmd reads m.SelectedWorkspace to
		// build the locked-rows view. Deferring via a cmd would
		// leave Repos staring at a nil workspace until the user
		// went ← back and forward again (which was the
		// reproducible "empty Repos page on first visit" bug).
		m.SelectedWorkspace = p.committed
		// Still emit wizard.WorkspaceCommittedMsg for observers / future
		// listeners — the wizard's handler is a no-op once state
		// is already current.
		return p, func() tea.Msg { return wizard.WorkspaceCommittedMsg{Ws: p.committed} }

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
				return p, nil
			}
			row := p.rows[p.matches[p.cursor]]
			// Mirror the Ticket page's auto-advance: emit wizard.GoNextMsg
			// after committing so the user doesn't have to press
			// Enter a second time.
			p.committed = &row.ws
			return p, func() tea.Msg { return wizard.GoNextMsg{} }
		}
	}

	prev := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != prev {
		p.recompute()
	}
	return p, cmd
}

func (p *workspacePage) recompute() {
	q := strings.TrimSpace(p.input.Value())
	p.matches = p.matches[:0]
	if q == "" {
		for i := range p.rows {
			if i >= workspaceVisibleRows {
				break
			}
			p.matches = append(p.matches, i)
		}
	} else {
		fm := fuzzy.Find(q, p.haystack)
		for i, mm := range fm {
			if i >= workspaceVisibleRows {
				break
			}
			p.matches = append(p.matches, mm.Index)
		}
	}
	if p.cursor >= len(p.matches) {
		p.cursor = 0
	}
}

func (p *workspacePage) View(m *wizard.Model) string {
	var b strings.Builder
	b.WriteString(wizard.TitleStyle.Render("Pick a workspace to edit"))
	b.WriteString("\n\n")

	if p.loading {
		b.WriteString(wizard.HintStyle.Render("  loading workspaces…\n"))
		return wizard.Indent(b.String(), 2)
	}
	if p.loadErr != nil {
		b.WriteString(wizard.ErrStyle.Render("  " + wizard.FmtErr(p.loadErr) + "\n"))
		return wizard.Indent(b.String(), 2)
	}
	if len(p.rows) == 0 {
		if p.committed != nil {
			// Preselected mode — just the summary.
			b.WriteString(renderWorkspaceSummary(*p.committed))
			b.WriteString("\n  " + wizard.HintStyle.Render(
				"workspace was supplied on the command line — → to continue") + "\n")
			return wizard.Indent(b.String(), 2)
		}
		b.WriteString(wizard.HintStyle.Render(
			"  no managed workspaces found (run `thicket start` first)\n"))
		return wizard.Indent(b.String(), 2)
	}

	b.WriteString("  " + p.input.View() + "\n")
	q := strings.TrimSpace(p.input.Value())
	switch {
	case q == "":
		b.WriteString("  " + wizard.HintStyle.Render(
			fmt.Sprintf("showing first %d of %d", len(p.matches), len(p.rows))))
	case len(p.matches) == 0:
		b.WriteString("  " + wizard.HintStyle.Render(fmt.Sprintf("no match for %q", q)))
	default:
		b.WriteString("  " + wizard.HintStyle.Render(fmt.Sprintf("%d match(es)", len(p.matches))))
	}
	b.WriteString("\n\n")

	const (
		slugW   = 36
		idW     = 10
		branchW = 24
		whenW   = 16
		reposW  = 5
	)
	b.WriteString("   ")
	for _, col := range []struct {
		t string
		w int
	}{{"Slug", slugW}, {"Ticket", idW}, {"Branch", branchW}, {"Created", whenW}, {"Repos", reposW}} {
		b.WriteString(wizard.SectionStyle.Render(wizard.PadRight(col.t, col.w)))
		b.WriteString("  ")
	}
	b.WriteString("\n   ")
	for _, w := range []int{slugW, idW, branchW, whenW, reposW} {
		b.WriteString(wizard.HintStyle.Render(strings.Repeat("─", w)))
		b.WriteString("  ")
	}
	b.WriteString("\n")

	for vi, ri := range p.matches {
		row := p.rows[ri]
		marker := " "
		style := wizard.DimStyle
		if vi == p.cursor {
			marker = wizard.CursorStyle.Render("▶")
			style = wizard.CursorStyle
		}
		when := row.ws.State.CreatedAt.Local().Format("2006-01-02 15:04")
		b.WriteString(marker + "  ")
		b.WriteString(style.Render(wizard.PadRight(wizard.Truncate(row.ws.Slug, slugW), slugW)))
		b.WriteString("  ")
		b.WriteString(style.Render(wizard.PadRight(wizard.Truncate(row.ws.State.TicketID, idW), idW)))
		b.WriteString("  ")
		b.WriteString(style.Render(wizard.PadRight(wizard.Truncate(row.ws.State.Branch, branchW), branchW)))
		b.WriteString("  ")
		b.WriteString(style.Render(wizard.PadRight(when, whenW)))
		b.WriteString("  ")
		b.WriteString(style.Render(wizard.PadRight(fmt.Sprintf("%d", len(row.ws.State.Repos)), reposW)))
		b.WriteString("\n")
	}
	_ = m // unused for now; kept to match the wizard.Page interface
	return wizard.Indent(b.String(), 2)
}

// renderWorkspaceSummary renders the picked-workspace header that
// the Repos page also uses to give context. Kept here next to the
// page that owns the workspace-picker semantics.
func renderWorkspaceSummary(ws workspace.ManagedWorkspace) string {
	var b strings.Builder
	b.WriteString(wizard.WarnStyle.Render(fmt.Sprintf("%s — %s", ws.Slug, ws.State.TicketID)))
	b.WriteString("\n")
	b.WriteString("  " + wizard.HintStyle.Render(fmt.Sprintf("branch: %s", ws.State.Branch)) + "\n")
	b.WriteString("  " + wizard.HintStyle.Render(fmt.Sprintf("worktrees: %d", len(ws.State.Repos))) + "\n")
	return b.String()
}
