package wizard

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sahilm/fuzzy"

	"github.com/uribrecher/thicket/internal/workspace"
)

const editWorkspaceVisibleRows = 12

// editWorkspaceRow is one prepared row in the workspace picker.
type editWorkspaceRow struct {
	ws     workspace.ManagedWorkspace
	filter string
}

type editWorkspacePage struct {
	loading bool
	loadErr error

	rows     []editWorkspaceRow
	haystack []string

	input   textinput.Model
	matches []int
	cursor  int

	// committedID is the slug of the workspace the user picked on
	// this page. Setting it both flips Complete() to true and lets
	// the wizard's commit handler stash the full ManagedWorkspace
	// on the shared Model.
	committed *workspace.ManagedWorkspace
}

func newEditWorkspacePage() *editWorkspacePage {
	ti := textinput.New()
	ti.Placeholder = "type to filter…"
	ti.Focus()
	ti.CharLimit = 80
	ti.Width = 60
	ti.Prompt = "› "
	return &editWorkspacePage{input: ti, loading: true}
}

// preseed flips the page into preselected mode (used when the caller
// passed `thicket edit <slug>`). No list fetch, no picker — just a
// read-only summary the user can peek at via ←.
func (p *editWorkspacePage) preseed(ws workspace.ManagedWorkspace) {
	p.loading = false
	p.rows = []editWorkspaceRow{} // non-nil so InitCmd's guard treats us as "loaded"
	p.committed = &ws
}

func (p *editWorkspacePage) Title() string { return "Workspace" }

func (p *editWorkspacePage) Hints() string { return "↑/↓ navigate · enter picks" }

func (p *editWorkspacePage) Complete() bool { return p.committed != nil }

// InitCmd fires the ListManaged call on first activation. Sync under
// the hood (file-system scan), but routed through a cmd so the page
// doesn't block render setup.
func (p *editWorkspacePage) InitCmd(m *Model) tea.Cmd {
	if p.rows != nil || p.loadErr != nil {
		return nil
	}
	root := m.editDeps.Cfg.WorkspaceRoot
	return func() tea.Msg {
		ws, _, err := workspace.ListManaged(root)
		return WorkspacesLoadedMsg{workspaces: ws, err: err}
	}
}

func (p *editWorkspacePage) Update(m *Model, msg tea.Msg) (Page, tea.Cmd) {
	switch v := msg.(type) {
	case WorkspacesLoadedMsg:
		p.loading = false
		if v.err != nil {
			p.loadErr = v.err
			return p, nil
		}
		p.rows = make([]editWorkspaceRow, len(v.workspaces))
		p.haystack = make([]string, len(v.workspaces))
		for i, ws := range v.workspaces {
			f := ws.Slug + " " + ws.State.TicketID + " " + ws.State.Branch
			p.rows[i] = editWorkspaceRow{ws: ws, filter: f}
			p.haystack[i] = f
		}
		p.recompute()
		return p, nil

	case GoNextMsg:
		if p.committed == nil {
			return p, nil
		}
		// Update shared state SYNCHRONOUSLY here — wizard.advance()
		// fires the Repos page's InitCmd immediately after this
		// returns, and that InitCmd reads m.selectedWorkspace to
		// build the locked-rows view. Deferring via a cmd would
		// leave Repos staring at a nil workspace until the user
		// went ← back and forward again (which was the
		// reproducible "empty Repos page on first visit" bug).
		m.selectedWorkspace = p.committed
		// Still emit WorkspaceCommittedMsg for observers / future
		// listeners — the wizard's handler is a no-op once state
		// is already current.
		return p, func() tea.Msg { return WorkspaceCommittedMsg{ws: p.committed} }

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
			// Mirror the Ticket page's auto-advance: emit GoNextMsg
			// after committing so the user doesn't have to press
			// Enter a second time.
			p.committed = &row.ws
			return p, func() tea.Msg { return GoNextMsg{} }
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

func (p *editWorkspacePage) recompute() {
	q := strings.TrimSpace(p.input.Value())
	p.matches = p.matches[:0]
	if q == "" {
		for i := range p.rows {
			if i >= editWorkspaceVisibleRows {
				break
			}
			p.matches = append(p.matches, i)
		}
	} else {
		fm := fuzzy.Find(q, p.haystack)
		for i, mm := range fm {
			if i >= editWorkspaceVisibleRows {
				break
			}
			p.matches = append(p.matches, mm.Index)
		}
	}
	if p.cursor >= len(p.matches) {
		p.cursor = 0
	}
}

func (p *editWorkspacePage) View(m *Model) string {
	var b strings.Builder
	b.WriteString(TitleStyle.Render("Pick a workspace to edit"))
	b.WriteString("\n\n")

	if p.loading {
		b.WriteString(HintStyle.Render("  loading workspaces…\n"))
		return Indent(b.String(), 2)
	}
	if p.loadErr != nil {
		b.WriteString(ErrStyle.Render("  " + FmtErr(p.loadErr) + "\n"))
		return Indent(b.String(), 2)
	}
	if len(p.rows) == 0 {
		if p.committed != nil {
			// Preselected mode — just the summary.
			b.WriteString(renderEditWorkspaceSummary(*p.committed))
			b.WriteString("\n  " + HintStyle.Render(
				"workspace was supplied on the command line — → to continue") + "\n")
			return Indent(b.String(), 2)
		}
		b.WriteString(HintStyle.Render(
			"  no managed workspaces found (run `thicket start` first)\n"))
		return Indent(b.String(), 2)
	}

	b.WriteString("  " + p.input.View() + "\n")
	q := strings.TrimSpace(p.input.Value())
	switch {
	case q == "":
		b.WriteString("  " + HintStyle.Render(
			fmt.Sprintf("showing first %d of %d", len(p.matches), len(p.rows))))
	case len(p.matches) == 0:
		b.WriteString("  " + HintStyle.Render(fmt.Sprintf("no match for %q", q)))
	default:
		b.WriteString("  " + HintStyle.Render(fmt.Sprintf("%d match(es)", len(p.matches))))
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
		b.WriteString(SectionStyle.Render(PadRight(col.t, col.w)))
		b.WriteString("  ")
	}
	b.WriteString("\n   ")
	for _, w := range []int{slugW, idW, branchW, whenW, reposW} {
		b.WriteString(HintStyle.Render(strings.Repeat("─", w)))
		b.WriteString("  ")
	}
	b.WriteString("\n")

	for vi, ri := range p.matches {
		row := p.rows[ri]
		marker := " "
		style := DimStyle
		if vi == p.cursor {
			marker = CursorStyle.Render("▶")
			style = CursorStyle
		}
		when := row.ws.State.CreatedAt.Local().Format("2006-01-02 15:04")
		b.WriteString(marker + "  ")
		b.WriteString(style.Render(PadRight(Truncate(row.ws.Slug, slugW), slugW)))
		b.WriteString("  ")
		b.WriteString(style.Render(PadRight(Truncate(row.ws.State.TicketID, idW), idW)))
		b.WriteString("  ")
		b.WriteString(style.Render(PadRight(Truncate(row.ws.State.Branch, branchW), branchW)))
		b.WriteString("  ")
		b.WriteString(style.Render(PadRight(when, whenW)))
		b.WriteString("  ")
		b.WriteString(style.Render(PadRight(fmt.Sprintf("%d", len(row.ws.State.Repos)), reposW)))
		b.WriteString("\n")
	}
	_ = m // unused for now; kept to match the Page interface
	return Indent(b.String(), 2)
}

// renderEditWorkspaceSummary renders the picked-workspace header that
// the Repos page also uses to give context. Kept here next to the
// page that owns the workspace-picker semantics.
func renderEditWorkspaceSummary(ws workspace.ManagedWorkspace) string {
	var b strings.Builder
	b.WriteString(WarnStyle.Render(fmt.Sprintf("%s — %s", ws.Slug, ws.State.TicketID)))
	b.WriteString("\n")
	b.WriteString("  " + HintStyle.Render(fmt.Sprintf("branch: %s", ws.State.Branch)) + "\n")
	b.WriteString("  " + HintStyle.Render(fmt.Sprintf("worktrees: %d", len(ws.State.Repos))) + "\n")
	return b.String()
}
