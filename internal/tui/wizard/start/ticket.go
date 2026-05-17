package start

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uribrecher/thicket/internal/tui/wizard"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sahilm/fuzzy"

	"github.com/uribrecher/thicket/internal/ticket"
	"github.com/uribrecher/thicket/internal/ticket/rank"
	"github.com/uribrecher/thicket/internal/tui"
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
	offset  int // scroll offset into matches; visible window is [offset, offset+ticketVisibleRows)

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

func (p *ticketPage) Hints() string { return "↑/↓ navigate · pgup/pgdn page · enter picks" }

// Complete is true once we have a fetched ticket — the page has all
// the info downstream needs.
func (p *ticketPage) Complete() bool { return p.fetchedID != "" && p.fetchErr == nil }

// InitCmd fires the ListAssigned call on first activation.
func (p *ticketPage) InitCmd(m *wizard.Model) tea.Cmd {
	if p.rows != nil || p.loadErr != nil {
		return nil // already loaded — going-back/forward shouldn't re-fetch
	}
	return tea.Batch(p.tickCmd(), listTicketsCmd(m))
}

func listTicketsCmd(m *wizard.Model) tea.Cmd {
	return func() tea.Msg {
		if m.Deps.Lister == nil {
			return wizard.TicketsLoadedMsg{Err: errors.New("ticket source does not support listing — pass a ticket id explicitly")}
		}
		tks, err := m.Deps.Lister.ListAssigned(m.Deps.Ctx)
		if err == nil {
			rank.Sort(tks, func(sourceID string) bool {
				return m.Deps.FindExistingWorkspace != nil &&
					m.Deps.FindExistingWorkspace(sourceID) != nil
			})
		}
		return wizard.TicketsLoadedMsg{Tickets: tks, Err: err}
	}
}

func fetchTicketCmd(m *wizard.Model, id ticket.ID) tea.Cmd {
	return func() tea.Msg {
		tk, err := m.Deps.Src.Fetch(id)
		return wizard.TicketFetchedMsg{Tk: tk, Err: err}
	}
}

// tickCmd schedules the next 1-second tick to refresh elapsed-time
// counters. Returns nil when no async work is in flight.
func (p *ticketPage) tickCmd() tea.Cmd {
	if !p.loading && !p.fetching {
		return nil
	}
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return wizard.TickMsg(t) })
}

func (p *ticketPage) Update(m *wizard.Model, msg tea.Msg) (wizard.Page, tea.Cmd) {
	switch v := msg.(type) {
	case wizard.TicketsLoadedMsg:
		p.loading = false
		if v.Err != nil {
			p.loadErr = v.Err
			return p, nil
		}
		p.rows = make([]ticketRow, len(v.Tickets))
		p.haystack = make([]string, len(v.Tickets))
		// Annotate with existing-workspace labels so the user can
		// spot in-flight work at a glance. Prefer the nickname when
		// set (short, friendly); fall back to the slug otherwise.
		for i, tk := range v.Tickets {
			ws := ""
			if m.Deps.FindExistingWorkspace != nil {
				if mws := m.Deps.FindExistingWorkspace(tk.SourceID); mws != nil {
					ws = mws.DisplayName()
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

	case wizard.TicketFetchedMsg:
		p.fetching = false
		if v.Err != nil {
			p.fetchErr = v.Err
			return p, nil
		}
		p.fetchedTk = v.Tk
		p.fetchedID = v.Tk.SourceID
		// Probe for an existing workspace now so advancing can short-
		// circuit without an extra round-trip.
		if m.Deps.FindExistingWorkspace != nil {
			if mws := m.Deps.FindExistingWorkspace(v.Tk.SourceID); mws != nil {
				p.existingDir = mws.Path
			} else {
				p.existingDir = ""
			}
		}
		// Auto-advance: the user already committed by pressing Enter
		// on a row. Forcing them to press Enter again to step past a
		// completed page is dead weight, so emit wizard.GoNextMsg and let
		// the wizard's advance flow handle it.
		return p, func() tea.Msg { return wizard.GoNextMsg{} }

	case wizard.TickMsg:
		return p, p.tickCmd()

	case wizard.GoNextMsg:
		// User is advancing past Ticket. We must update the model's
		// shared ticket state SYNCHRONOUSLY here — `wizard.advance()`
		// fires the next page's `InitCmd` immediately after this
		// returns, and that InitCmd reads `m.TicketID` to decide
		// whether to fire the LLM detect call. If we deferred the
		// state update via a cmd (wizard.TicketCommittedMsg), the Repos
		// page's InitCmd would see an empty ticketID and short-circuit
		// to "nothing to load" — which is exactly the bug that
		// silently broke the Repos page.
		if !p.Complete() {
			return p, nil
		}
		tk := p.fetchedTk
		if tk.SourceID != m.TicketID {
			delete(m.LLMCache, m.TicketID)
			delete(m.SummaryCache, m.TicketID)
			delete(m.NicknameCache, m.TicketID)
			m.Chosen = nil
			m.CloneInclude = make(map[string]bool)
		}
		m.Ticket = tk
		m.TicketID = tk.SourceID
		if p.existingDir != "" {
			// Reuse Path: set the final result synchronously and ask
			// the program to quit. advance() inspects m.Done before
			// bumping `active`, so we won't fire the Repos page's LLM
			// detect cmd just to throw it away when the quit lands.
			m.Result.ReuseDir = p.existingDir
			m.Result.Ticket = tk
			m.Done = true
			return p, tea.Quit
		}
		// Still emit wizard.TicketCommittedMsg for observers (tests, future
		// listeners). Wizard's handler is a no-op once state is
		// already current, so this stays safe.
		return p, func() tea.Msg { return wizard.TicketCommittedMsg{Tk: tk} }

	case tea.KeyMsg:
		// The wizard's global handler eats "esc", "ctrl+c", "left",
		// and "right". Enter is page-local — we use it here to pick
		// a row (Fetch + auto-advance).
		switch v.String() {
		case "up":
			if p.cursor > 0 {
				p.cursor--
				p.clampOffset()
			}
			return p, nil
		case "down":
			if p.cursor < len(p.matches)-1 {
				p.cursor++
				p.clampOffset()
			}
			return p, nil
		case "pgup":
			// Advance the visible window by a full page, then keep the
			// cursor at its prior position within that window. Without
			// moving offset directly, clampOffset only scrolls just
			// enough to keep the cursor visible — which is a partial
			// page when the cursor was not at an edge.
			p.offset -= ticketVisibleRows
			p.cursor -= ticketVisibleRows
			p.clampWindow()
			return p, nil
		case "pgdown":
			p.offset += ticketVisibleRows
			p.cursor += ticketVisibleRows
			p.clampWindow()
			return p, nil
		case "home":
			p.cursor = 0
			p.clampOffset()
			return p, nil
		case "end":
			p.cursor = len(p.matches) - 1
			if p.cursor < 0 {
				p.cursor = 0
			}
			p.clampOffset()
			return p, nil
		case "enter":
			// Fetch the row under the cursor. This is what locks in a
			// ticket on this page — the resulting wizard.TicketFetchedMsg
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
			id, err := m.Deps.Src.Parse(row.tk.SourceID)
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
			p.matches = append(p.matches, i)
		}
	} else {
		fm := fuzzy.Find(q, p.haystack)
		for _, mm := range fm {
			p.matches = append(p.matches, mm.Index)
		}
	}
	if p.cursor >= len(p.matches) {
		p.cursor = 0
	}
	p.clampOffset()
}

// clampOffset adjusts offset so the cursor stays within the visible
// window of ticketVisibleRows rows. Used for cursor-driven motion
// (up/down/home/end) where offset only needs to scroll enough to keep
// the cursor visible.
func (p *ticketPage) clampOffset() {
	if p.cursor < p.offset {
		p.offset = p.cursor
	} else if p.cursor >= p.offset+ticketVisibleRows {
		p.offset = p.cursor - ticketVisibleRows + 1
	}
	if p.offset < 0 {
		p.offset = 0
	}
	maxOffset := len(p.matches) - ticketVisibleRows
	if maxOffset < 0 {
		maxOffset = 0
	}
	if p.offset > maxOffset {
		p.offset = maxOffset
	}
}

// clampWindow clamps offset to a valid range first, then snaps the
// cursor into the resulting window. Used for page-driven motion
// (pgup/pgdn) where the offset is authoritative and the cursor must
// follow — the opposite priority from clampOffset.
func (p *ticketPage) clampWindow() {
	maxOffset := len(p.matches) - ticketVisibleRows
	if maxOffset < 0 {
		maxOffset = 0
	}
	if p.offset < 0 {
		p.offset = 0
	}
	if p.offset > maxOffset {
		p.offset = maxOffset
	}
	end := p.offset + ticketVisibleRows
	if end > len(p.matches) {
		end = len(p.matches)
	}
	if p.cursor < p.offset {
		p.cursor = p.offset
	}
	if p.cursor >= end {
		p.cursor = end - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

func (p *ticketPage) View(m *wizard.Model) string {
	var b strings.Builder

	b.WriteString(wizard.TitleStyle.Render("Pick a ticket to start a workspace for"))
	b.WriteString("\n\n")

	if p.loading {
		secs := int(time.Since(p.startAt).Seconds())
		b.WriteString(wizard.HintStyle.Render(fmt.Sprintf("  fetching your open assigned tickets… %ds", secs)))
		b.WriteString("\n")
		return wizard.Indent(b.String(), 2)
	}
	if p.loadErr != nil {
		b.WriteString(wizard.ErrStyle.Render("  " + wizard.FmtErr(p.loadErr)))
		b.WriteString("\n")
		return wizard.Indent(b.String(), 2)
	}
	if len(p.rows) == 0 {
		// Preselected-ticket mode: no list to render, just the summary.
		if p.fetchedID != "" {
			b.WriteString(wizard.RenderTicketSummary(p.fetchedTk, m.SummaryCache[m.TicketID]))
			b.WriteString("\n  " + wizard.HintStyle.Render(
				"ticket was supplied on the command line — → to continue") + "\n")
			return wizard.Indent(b.String(), 2)
		}
		b.WriteString(wizard.HintStyle.Render("  no open assigned tickets found"))
		b.WriteString("\n")
		return wizard.Indent(b.String(), 2)
	}

	// Search box + status. The textinput's placeholder ("type to
	// filter…") already conveys the affordance, so this line just
	// summarizes the result set — no second "type to filter" hint.
	b.WriteString("  " + p.input.View())
	b.WriteString("\n")
	q := strings.TrimSpace(p.input.Value())
	switch {
	case len(p.matches) == 0 && q != "":
		b.WriteString("  " + wizard.HintStyle.Render(fmt.Sprintf("no match for %q", q)))
	case len(p.matches) == 0:
		b.WriteString("  " + wizard.HintStyle.Render("no tickets"))
	default:
		end := p.offset + ticketVisibleRows
		if end > len(p.matches) {
			end = len(p.matches)
		}
		total := len(p.rows)
		filtered := ""
		if q != "" {
			filtered = fmt.Sprintf(" (filtered from %d)", total)
			total = len(p.matches)
		}
		b.WriteString("  " + wizard.HintStyle.Render(fmt.Sprintf("showing %d–%d of %d%s",
			p.offset+1, end, total, filtered)))
	}
	b.WriteString("\n\n")

	// Table.
	const (
		idW    = 10
		stateW = 18
		titleW = 50
		wsW    = 36
		iterW  = 5
	)
	b.WriteString("   ")
	for _, col := range []struct {
		t string
		w int
	}{{"Ticket", idW}, {"State", stateW}, {"Title", titleW}, {"Workspace", wsW}, {"Iter", iterW}} {
		b.WriteString(wizard.SectionStyle.Render(wizard.PadRight(col.t, col.w)))
		b.WriteString("  ")
	}
	b.WriteString("\n   ")
	for _, w := range []int{idW, stateW, titleW, wsW, iterW} {
		b.WriteString(wizard.HintStyle.Render(strings.Repeat("─", w)))
		b.WriteString("  ")
	}
	b.WriteString("\n")

	end := p.offset + ticketVisibleRows
	if end > len(p.matches) {
		end = len(p.matches)
	}
	for vi := p.offset; vi < end; vi++ {
		ri := p.matches[vi]
		row := p.rows[ri]
		marker := " "
		style := wizard.DimStyle // unfocused rows; wizard.PendingTabStyle has padding which would break column alignment
		if vi == p.cursor {
			marker = wizard.CursorStyle.Render("▶")
			style = wizard.CursorStyle
		}
		b.WriteString(marker + "  ")
		// Hyperlink wraps the styled, padded ticket-id cell so ⌘-click
		// in supporting terminals opens the ticket URL; runewidth-based
		// width math is preserved because the OSC 8 escapes are
		// appended after Truncate/PadRight measured the visible label.
		b.WriteString(tui.Hyperlink(row.tk.URL,
			style.Render(wizard.PadRight(wizard.Truncate(row.tk.SourceID, idW), idW))))
		b.WriteString("  ")
		b.WriteString(style.Render(wizard.PadRight(wizard.Truncate(row.tk.State, stateW), stateW)))
		b.WriteString("  ")
		b.WriteString(style.Render(wizard.PadRight(wizard.Truncate(row.tk.Title, titleW), titleW)))
		b.WriteString("  ")
		b.WriteString(style.Render(wizard.PadRight(wizard.Truncate(row.workspace, wsW), wsW)))
		b.WriteString("  ")
		b.WriteString(style.Render(wizard.PadRight(rank.FormatIterationDistance(row.tk.IterationDistance), iterW)))
		b.WriteString("\n")
	}

	// Brief fetching / error status below the table. The full
	// summary (description / requester / labels) lives on the Repos
	// page now — by the time it's worth reading, the user has moved
	// on from picking and is reviewing repos.
	if p.fetching {
		secs := int(time.Since(p.fetchStartAt).Seconds())
		b.WriteString("\n")
		b.WriteString("  " + wizard.HintStyle.Render(fmt.Sprintf("loading ticket details… %ds", secs)))
		b.WriteString("\n")
	} else if p.fetchErr != nil {
		b.WriteString("\n")
		b.WriteString("  " + wizard.ErrStyle.Render(wizard.FmtErr(p.fetchErr)))
		b.WriteString("\n")
	}
	return wizard.Indent(b.String(), 2)
}
