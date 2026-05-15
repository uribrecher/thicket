package wizard

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sahilm/fuzzy"

	"github.com/uribrecher/thicket/internal/ticket"
)

const ticketVisibleRows = 12

// ticketRow is one prepared row from a Ticket result.
type ticketRow struct {
	tk        ticket.Ticket
	filter    string
	workspace string // existing workspace slug, if any
}

type ticketPage struct {
	// Async load state.
	loading bool
	loadErr error
	startAt time.Time

	// Once tickets land, these populate.
	rows     []ticketRow
	haystack []string

	// Filter / cursor.
	input   textinput.Model
	matches []int
	cursor  int

	// Per-row Fetch state — Fetch is fired when user presses Enter; the
	// result is stored on the page so going back & forward shows the
	// summary without re-fetching.
	fetching     bool
	fetchedTk    ticket.Ticket
	fetchedID    string // ticket id we last fetched
	fetchErr     error
	fetchStartAt time.Time

	// Cached existence check for the last fetched ticket — non-empty
	// means a managed workspace already exists; the wizard short-
	// circuits when the user advances.
	existingDir string
}

func newTicketPage() *ticketPage {
	ti := textinput.New()
	ti.Placeholder = "type to filter…"
	ti.Focus()
	ti.CharLimit = 80
	// See page_repos.go: bubbles textinput's placeholder is
	// truncated to its first character when Width = 0.
	ti.Width = 60
	ti.Prompt = "› "
	return &ticketPage{input: ti, loading: true, startAt: time.Now()}
}

// preseed flips the page into "preselected" mode: no list fetch, no
// picker — just the ticket summary the user can peek at via ←. Used
// by `thicket start <id>` where the args path already named the
// ticket so the picker would be busywork.
func (p *ticketPage) preseed(tk ticket.Ticket) {
	p.loading = false
	p.rows = []ticketRow{} // non-nil so InitCmd's guard treats us as "loaded"
	p.fetchedTk = tk
	p.fetchedID = tk.SourceID
}

func (p *ticketPage) Title() string { return "Ticket" }

func (p *ticketPage) Hints() string { return "↑/↓ navigate · enter picks" }

// Complete is true once we have a fetched ticket — the page has all
// the info downstream needs.
func (p *ticketPage) Complete() bool { return p.fetchedID != "" && p.fetchErr == nil }

// InitCmd fires the ListAssigned call on first activation.
func (p *ticketPage) InitCmd(m *Model) tea.Cmd {
	if p.rows != nil || p.loadErr != nil {
		return nil // already loaded — going-back/forward shouldn't re-fetch
	}
	return tea.Batch(p.tickCmd(), listTicketsCmd(m))
}

func listTicketsCmd(m *Model) tea.Cmd {
	return func() tea.Msg {
		if m.deps.Lister == nil {
			return TicketsLoadedMsg{err: errors.New("ticket source does not support listing — pass a ticket id explicitly")}
		}
		tks, err := m.deps.Lister.ListAssigned(m.deps.Ctx)
		return TicketsLoadedMsg{tickets: tks, err: err}
	}
}

func fetchTicketCmd(m *Model, id ticket.ID) tea.Cmd {
	return func() tea.Msg {
		tk, err := m.deps.Src.Fetch(id)
		return TicketFetchedMsg{tk: tk, err: err}
	}
}

// tickCmd schedules the next 1-second tick to refresh elapsed-time
// counters. Returns nil when no async work is in flight.
func (p *ticketPage) tickCmd() tea.Cmd {
	if !p.loading && !p.fetching {
		return nil
	}
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return TickMsg(t) })
}

func (p *ticketPage) Update(m *Model, msg tea.Msg) (Page, tea.Cmd) {
	switch v := msg.(type) {
	case TicketsLoadedMsg:
		p.loading = false
		if v.err != nil {
			p.loadErr = v.err
			return p, nil
		}
		p.rows = make([]ticketRow, len(v.tickets))
		p.haystack = make([]string, len(v.tickets))
		// Annotate with existing-workspace dir names so the user can
		// spot in-flight work at a glance. The cell value comes from
		// the actual workspace directory name (filepath.Base(path)),
		// not from Slug(tk.SourceID, tk.Title) — a renamed ticket
		// keeps its original workspace dir on disk and we want the
		// column to match what `thicket rm` / `ls` would show.
		for i, tk := range v.tickets {
			ws := ""
			if m.deps.FindExistingWorkspace != nil {
				if path := m.deps.FindExistingWorkspace(tk.SourceID); path != "" {
					ws = filepath.Base(path)
				}
			}
			p.rows[i] = ticketRow{
				tk:        tk,
				filter:    tk.SourceID + " " + tk.State + " " + tk.Title + " " + ws,
				workspace: ws,
			}
			p.haystack[i] = p.rows[i].filter
		}
		p.recompute()
		return p, nil

	case TicketFetchedMsg:
		p.fetching = false
		if v.err != nil {
			p.fetchErr = v.err
			return p, nil
		}
		p.fetchedTk = v.tk
		p.fetchedID = v.tk.SourceID
		// Probe for an existing workspace now so advancing can short-
		// circuit without an extra round-trip.
		if m.deps.FindExistingWorkspace != nil {
			p.existingDir = m.deps.FindExistingWorkspace(v.tk.SourceID)
		}
		// Auto-advance: the user already committed by pressing Enter
		// on a row. Forcing them to press Enter again to step past a
		// completed page is dead weight, so emit GoNextMsg and let
		// the wizard's advance flow handle it.
		return p, func() tea.Msg { return GoNextMsg{} }

	case TickMsg:
		return p, p.tickCmd()

	case GoNextMsg:
		// User is advancing past Ticket. We must update the model's
		// shared ticket state SYNCHRONOUSLY here — `wizard.advance()`
		// fires the next page's `InitCmd` immediately after this
		// returns, and that InitCmd reads `m.ticketID` to decide
		// whether to fire the LLM detect call. If we deferred the
		// state update via a cmd (TicketCommittedMsg), the Repos
		// page's InitCmd would see an empty ticketID and short-circuit
		// to "nothing to load" — which is exactly the bug that
		// silently broke the Repos page.
		if !p.Complete() {
			return p, nil
		}
		tk := p.fetchedTk
		if tk.SourceID != m.ticketID {
			delete(m.llmCache, m.ticketID)
			delete(m.summaryCache, m.ticketID)
			m.chosen = nil
			m.cloneInclude = make(map[string]bool)
		}
		m.ticket = tk
		m.ticketID = tk.SourceID
		if p.existingDir != "" {
			// Reuse path: set the final result synchronously and ask
			// the program to quit. advance() inspects m.done before
			// bumping `active`, so we won't fire the Repos page's LLM
			// detect cmd just to throw it away when the quit lands.
			m.result.ReuseDir = p.existingDir
			m.result.Ticket = tk
			m.done = true
			return p, tea.Quit
		}
		// Still emit TicketCommittedMsg for observers (tests, future
		// listeners). Wizard's handler is a no-op once state is
		// already current, so this stays safe.
		return p, func() tea.Msg { return TicketCommittedMsg{tk: tk} }

	case tea.KeyMsg:
		// The wizard's global handler eats "esc", "ctrl+c", "left",
		// and "right". Enter is page-local — we use it here to pick
		// a row (Fetch + auto-advance).
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
			// Fetch the row under the cursor. This is what locks in a
			// ticket on this page — the resulting TicketFetchedMsg
			// arms Complete() so the next Enter / → advances.
			if p.cursor >= len(p.matches) {
				return p, nil
			}
			row := p.rows[p.matches[p.cursor]]
			// If we're already fetched for this row, no-op (avoids
			// re-Fetching when the user toggles back to the picker).
			if p.fetchedID == row.tk.SourceID && p.fetchErr == nil {
				return p, nil
			}
			p.fetching = true
			p.fetchErr = nil
			p.fetchedID = ""
			p.fetchStartAt = time.Now()
			id, err := m.deps.Src.Parse(row.tk.SourceID)
			if err != nil {
				p.fetching = false
				p.fetchErr = err
				return p, nil
			}
			return p, tea.Batch(p.tickCmd(), fetchTicketCmd(m, id))
		}
	}

	// Forward to text input for filter changes.
	prev := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != prev {
		p.recompute()
	}
	return p, cmd
}

func (p *ticketPage) recompute() {
	q := strings.TrimSpace(p.input.Value())
	p.matches = p.matches[:0]
	if q == "" {
		for i := range p.rows {
			if i >= ticketVisibleRows {
				break
			}
			p.matches = append(p.matches, i)
		}
	} else {
		fm := fuzzy.Find(q, p.haystack)
		for i, mm := range fm {
			if i >= ticketVisibleRows {
				break
			}
			p.matches = append(p.matches, mm.Index)
		}
	}
	if p.cursor >= len(p.matches) {
		p.cursor = 0
	}
}

func (p *ticketPage) View(m *Model) string {
	var b strings.Builder

	b.WriteString(TitleStyle.Render("Pick a ticket to start a workspace for"))
	b.WriteString("\n\n")

	if p.loading {
		secs := int(time.Since(p.startAt).Seconds())
		b.WriteString(HintStyle.Render(fmt.Sprintf("  fetching your open assigned tickets… %ds", secs)))
		b.WriteString("\n")
		return Indent(b.String(), 2)
	}
	if p.loadErr != nil {
		b.WriteString(ErrStyle.Render("  " + FmtErr(p.loadErr)))
		b.WriteString("\n")
		return Indent(b.String(), 2)
	}
	if len(p.rows) == 0 {
		// Preselected-ticket mode: no list to render, just the summary.
		if p.fetchedID != "" {
			b.WriteString(RenderTicketSummary(p.fetchedTk, m.summaryCache[m.ticketID]))
			b.WriteString("\n  " + HintStyle.Render(
				"ticket was supplied on the command line — → to continue") + "\n")
			return Indent(b.String(), 2)
		}
		b.WriteString(HintStyle.Render("  no open assigned tickets found"))
		b.WriteString("\n")
		return Indent(b.String(), 2)
	}

	// Search box + status. The textinput's placeholder ("type to
	// filter…") already conveys the affordance, so this line just
	// summarizes the result set — no second "type to filter" hint.
	b.WriteString("  " + p.input.View())
	b.WriteString("\n")
	q := strings.TrimSpace(p.input.Value())
	switch {
	case q == "":
		b.WriteString("  " + HintStyle.Render(fmt.Sprintf("showing first %d of %d",
			len(p.matches), len(p.rows))))
	case len(p.matches) == 0:
		b.WriteString("  " + HintStyle.Render(fmt.Sprintf("no match for %q", q)))
	default:
		b.WriteString("  " + HintStyle.Render(fmt.Sprintf("%d match(es)", len(p.matches))))
	}
	b.WriteString("\n\n")

	// Table.
	const (
		idW    = 10
		stateW = 18
		titleW = 50
		wsW    = 36
	)
	b.WriteString("   ")
	for _, col := range []struct {
		t string
		w int
	}{{"Ticket", idW}, {"State", stateW}, {"Title", titleW}, {"Workspace", wsW}} {
		b.WriteString(SectionStyle.Render(PadRight(col.t, col.w)))
		b.WriteString("  ")
	}
	b.WriteString("\n   ")
	for _, w := range []int{idW, stateW, titleW, wsW} {
		b.WriteString(HintStyle.Render(strings.Repeat("─", w)))
		b.WriteString("  ")
	}
	b.WriteString("\n")

	for vi, ri := range p.matches {
		row := p.rows[ri]
		marker := " "
		style := DimStyle // unfocused rows; PendingTabStyle has padding which would break column alignment
		if vi == p.cursor {
			marker = CursorStyle.Render("▶")
			style = CursorStyle
		}
		b.WriteString(marker + "  ")
		b.WriteString(style.Render(PadRight(Truncate(row.tk.SourceID, idW), idW)))
		b.WriteString("  ")
		b.WriteString(style.Render(PadRight(Truncate(row.tk.State, stateW), stateW)))
		b.WriteString("  ")
		b.WriteString(style.Render(PadRight(Truncate(row.tk.Title, titleW), titleW)))
		b.WriteString("  ")
		b.WriteString(style.Render(PadRight(Truncate(row.workspace, wsW), wsW)))
		b.WriteString("\n")
	}

	// Brief fetching / error status below the table. The full
	// summary (description / requester / labels) lives on the Repos
	// page now — by the time it's worth reading, the user has moved
	// on from picking and is reviewing repos.
	if p.fetching {
		secs := int(time.Since(p.fetchStartAt).Seconds())
		b.WriteString("\n")
		b.WriteString("  " + HintStyle.Render(fmt.Sprintf("loading ticket details… %ds", secs)))
		b.WriteString("\n")
	} else if p.fetchErr != nil {
		b.WriteString("\n")
		b.WriteString("  " + ErrStyle.Render(FmtErr(p.fetchErr)))
		b.WriteString("\n")
	}
	return Indent(b.String(), 2)
}
