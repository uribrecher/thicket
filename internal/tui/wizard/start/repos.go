package start

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/uribrecher/thicket/internal/tui/wizard"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/detector"
)

// maxLLMRows caps the number of LLM-suggestion rows shown at the
// bottom of the Repos page. (The cap on regular fuzzy/empty-query
// rows is wizard.MaxRepoMatches — shared with the edit wizard.)
const maxLLMRows = 8

// matchItem is one row in the unified match list. `selected` puts it
// under the "Selected" header at the top; `llm` flags it as
// LLM-origin (renders a "relevance N%" tag whether selected or not, and
// when unselected groups it under the "Suggested for this ticket"
// header at the bottom).
type matchItem struct {
	name     string
	selected bool
	llm      bool
}

type reposPage struct {
	// Activation tracking. We fire the LLM detect cmd the first time
	// we're activated for a given ticket id (or any time the id
	// changes). loadedForID tracks which id the current state belongs
	// to so we know when to refire.
	loadedForID    string
	loading        bool
	loadErr        error
	loadStartAt    time.Time
	loadFinishedAt time.Time

	// Catalog + LLM picks for the current ticket. Catalog is seeded
	// eagerly in InitCmd so fuzzy search works while the LLM is still
	// in flight — the user shouldn't have to wait 15s before they can
	// start typing.
	repos      []catalog.Repo
	names      []string
	nameSet    map[string]bool
	descByName map[string]string
	picks      map[string]detector.RepoMatch
	pickOrder  []string // LLM-returned order, used for stable rendering

	// Mutable selection state.
	selected      map[string]bool
	selectedOrder []string
	input         textinput.Model
	matches       []matchItem
	cursor        int
	status        string

	// spinner animates the "looking for relevant repos…" status line
	// while the LLM call is in flight. Charm's bubbles/spinner runs
	// its own tick loop (~80ms cadence) and we just embed its
	// rendered glyph in the status string.
	spinner spinner.Model
}

func newReposPage() *reposPage {
	ti := textinput.New()
	ti.Placeholder = "type to filter the catalog"
	ti.Focus()
	ti.CharLimit = 80
	// textinput.New() defaults Width to 0, and at Width=0 the
	// placeholderView in bubbles only renders the first character of
	// the placeholder (the rest is short-circuited at line 716 of
	// textinput.go). Setting an explicit Width that comfortably
	// exceeds the placeholder length makes the whole hint render.
	ti.Width = 60
	ti.Prompt = "› "
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	return &reposPage{
		input:    ti,
		selected: make(map[string]bool),
		spinner:  sp,
	}
}

func (p *reposPage) Title() string { return "Repos" }

func (p *reposPage) Hints() string { return "↑/↓ navigate · enter toggles" }

func (p *reposPage) Complete() bool { return len(p.selectedOrder) > 0 }

// InitCmd seeds the catalog synchronously (so search is immediately
// usable) and fires the LLM detect cmd if we don't already have picks
// cached for this ticket.
func (p *reposPage) InitCmd(m *wizard.Model) tea.Cmd {
	if m.TicketID == "" {
		return nil
	}
	if p.loadedForID == m.TicketID {
		return nil
	}
	p.resetForNewTicket()
	p.seedCatalog(m)
	p.loadedForID = m.TicketID
	p.recompute()

	// Fire the summarize cmd unconditionally (when wired + uncached) —
	// even if LLM picks are cached, the user might have wiped the
	// summary cache or arrived via a different path. Cheap to skip
	// when already present.
	cmds := []tea.Cmd{}
	if m.Deps.Summarize != nil {
		if _, ok := m.SummaryCache[m.TicketID]; !ok {
			cmds = append(cmds, summarizeCmd(m))
		}
	}
	if m.Deps.Nickname != nil {
		if _, ok := m.NicknameCache[m.TicketID]; !ok {
			cmds = append(cmds, nicknameCmd(m))
		}
	}

	if cached, ok := m.LLMCache[m.TicketID]; ok {
		p.setLLMPicks(cached)
		if len(cmds) == 0 {
			return nil
		}
		return tea.Batch(cmds...)
	}
	p.loading = true
	p.loadErr = nil
	p.loadStartAt = time.Now()
	p.loadFinishedAt = time.Time{}
	cmds = append(cmds, p.spinner.Tick, detectCmd(m))
	return tea.Batch(cmds...)
}

// resetForNewTicket clears the page-local state so the next ticket's
// load starts from a clean slate. Called from InitCmd when ticketID
// changes — keeps stale state from leaking across tickets.
func (p *reposPage) resetForNewTicket() {
	p.repos = nil
	p.names = p.names[:0]
	p.nameSet = nil
	p.descByName = nil
	p.picks = nil
	p.pickOrder = p.pickOrder[:0]
	p.selected = make(map[string]bool)
	p.selectedOrder = p.selectedOrder[:0]
	p.matches = p.matches[:0]
	p.cursor = 0
	p.status = ""
	p.input.SetValue("")
}

// seedCatalog populates name lookup tables from m.Deps.Repos and
// restores any selection the user already had (from a prior Plan-page
// trip + ← back). LLM picks are populated separately via setLLMPicks.
func (p *reposPage) seedCatalog(m *wizard.Model) {
	p.repos = m.Deps.Repos
	p.names = make([]string, 0, len(p.repos))
	p.nameSet = make(map[string]bool, len(p.repos))
	p.descByName = make(map[string]string, len(p.repos))
	for _, r := range p.repos {
		p.names = append(p.names, r.Name)
		p.nameSet[r.Name] = true
		p.descByName[r.Name] = r.Description
	}
	// If the wizard already has a selection for this ticket (user
	// went forward to Plan and came back), restore it.
	for _, r := range m.Chosen {
		if p.nameSet[r.Name] && !p.selected[r.Name] {
			p.selected[r.Name] = true
			p.selectedOrder = append(p.selectedOrder, r.Name)
		}
	}
}

// setLLMPicks records the LLM's suggestions for rendering at the
// bottom of the match list. It does NOT auto-select anything — the
// user makes the call manually. Picks are sorted by confidence
// descending so the strongest suggestions show first regardless of
// what order the model emitted. Setting these completes the load
// (loadFinishedAt) so the status line dims to gray.
func (p *reposPage) setLLMPicks(picks []detector.RepoMatch) {
	// Stable sort by descending confidence — keeps a deterministic
	// order for equally-confident picks (the LLM's original order
	// wins the tie).
	sorted := make([]detector.RepoMatch, len(picks))
	copy(sorted, picks)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Confidence > sorted[j].Confidence
	})

	p.picks = make(map[string]detector.RepoMatch, len(sorted))
	p.pickOrder = p.pickOrder[:0]
	for _, pk := range sorted {
		if _, dup := p.picks[pk.Name]; dup {
			continue
		}
		p.picks[pk.Name] = pk
		p.pickOrder = append(p.pickOrder, pk.Name)
	}
	if p.loadFinishedAt.IsZero() {
		p.loadFinishedAt = time.Now()
	}
	p.recompute()
}

func detectCmd(m *wizard.Model) tea.Cmd {
	tk := m.Ticket
	id := m.TicketID
	return func() tea.Msg {
		ctx := m.Deps.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		picks, err := m.Deps.Detect(ctx, tk, m.Deps.Repos)
		return wizard.PicksLoadedMsg{TicketID: id, Picks: picks, Err: err}
	}
}

// summarizeCmd runs the (optional) Summarize closure for the current
// ticket. Failures aren't fatal — the renderer silently falls back to
// the first-N-lines view.
func summarizeCmd(m *wizard.Model) tea.Cmd {
	tk := m.Ticket
	id := m.TicketID
	return func() tea.Msg {
		ctx := m.Deps.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		lines, err := m.Deps.Summarize(ctx, tk)
		return wizard.SummarizedMsg{TicketID: id, Lines: lines, Err: err}
	}
}

// nicknameCmd runs the (optional) Nickname closure for the current
// ticket. Failures aren't fatal — the Plan page's input just stays
// empty and the user can type their own (or leave it blank).
func nicknameCmd(m *wizard.Model) tea.Cmd {
	tk := m.Ticket
	id := m.TicketID
	return func() tea.Msg {
		ctx := m.Deps.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		nn, err := m.Deps.Nickname(ctx, tk)
		return wizard.NicknameSuggestedMsg{TicketID: id, Nickname: nn, Err: err}
	}
}

func (p *reposPage) Update(m *wizard.Model, msg tea.Msg) (wizard.Page, tea.Cmd) {
	switch v := msg.(type) {
	case wizard.PicksLoadedMsg:
		if v.TicketID != m.TicketID {
			return p, nil // stale msg from a previous ticket
		}
		p.loading = false
		if v.Err != nil {
			p.loadErr = v.Err
			return p, nil
		}
		p.setLLMPicks(v.Picks)
		return p, nil

	case spinner.TickMsg:
		// Keep ticking only while the LLM is in flight. Once the
		// status line dims to "found N repos in Xs", we don't need
		// to keep re-rendering at 80ms.
		if !p.loading {
			return p, nil
		}
		var cmd tea.Cmd
		p.spinner, cmd = p.spinner.Update(msg)
		return p, cmd

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
		// Update m.Chosen synchronously here — same reason as the
		// Ticket page's commit: the wizard's advance() fires the
		// Plan page's InitCmd IMMEDIATELY after this returns, and
		// InitCmd reads m.Chosen to decide whether to rebuild the
		// plan. If we only emitted wizard.ReposCommittedMsg as a deferred
		// cmd, InitCmd would see the OLD m.Chosen and either keep
		// the stale plan (if the count happened to match) or rebuild
		// against outdated repos.
		m.Chosen = append(m.Chosen[:0], chosen...)
		return p, func() tea.Msg { return wizard.ReposCommittedMsg{Chosen: chosen} }

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
			if p.cursor < len(p.matches) {
				toggled := p.matches[p.cursor].name
				p.toggle(toggled)
				p.input.SetValue("")
				p.recompute()
				// Follow the toggled item to its new position so the
				// cursor doesn't lurch back to row 0 on every action.
				// If the item dropped out of the visible list entirely
				// (capped Suggested overflow), the prior cap+0 clamp
				// in recompute keeps us in a valid range.
				for i, it := range p.matches {
					if it.name == toggled {
						p.cursor = i
						break
					}
				}
			} else if strings.TrimSpace(p.input.Value()) != "" {
				p.status = fmt.Sprintf("no match for %q", p.input.Value())
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

// recompute rebuilds the unified match list as three contiguous
// groups: Selected (top), Available fuzzy matches (middle, only
// when the query is non-empty), and Suggested LLM picks (bottom).
// Every repo appears in exactly one group — selected items are not
// re-rendered inside the Suggested block — so the user never sees
// the same row twice.
func (p *reposPage) recompute() {
	q := strings.TrimSpace(p.input.Value())
	p.matches = p.matches[:0]

	// Group 1: Selected. Always shown when non-empty, independent of
	// the typed query — filtering it would feel like items vanished.
	for _, n := range p.selectedOrder {
		_, isLLM := p.picks[n]
		p.matches = append(p.matches, matchItem{name: n, selected: true, llm: isLLM})
	}

	// Group 2: Available fuzzy matches. Empty query → skip (today's
	// "empty query echoes the selection" is redundant now that
	// Selected is right above). Non-empty query → fuzzy results
	// re-ranked (see wizard.RankFuzzy) so contiguous substring matches beat
	// scattered ones, excluding both selected and LLM picks.
	if q != "" {
		count := 0
		for _, mm := range wizard.RankFuzzy(q, p.names) {
			if p.selected[mm.Str] {
				continue
			}
			if _, isLLM := p.picks[mm.Str]; isLLM {
				continue
			}
			p.matches = append(p.matches, matchItem{name: mm.Str})
			count++
			if count >= wizard.MaxRepoMatches {
				break
			}
		}
	}

	// Group 3: Suggested (unselected LLM picks). Shown regardless of
	// query so the user can always see the LLM's recommendations.
	count := 0
	for _, n := range p.pickOrder {
		if !p.nameSet[n] {
			continue // LLM hallucination — name not in catalog
		}
		if p.selected[n] {
			continue // already rendered in Selected group above
		}
		p.matches = append(p.matches, matchItem{name: n, llm: true})
		count++
		if count >= maxLLMRows {
			break
		}
	}

	if p.cursor >= len(p.matches) {
		p.cursor = 0
	}
}

func (p *reposPage) View(m *wizard.Model) string {
	var b strings.Builder

	// wizard.Page title.
	b.WriteString(wizard.TitleStyle.Render("Select repos that are relevant for this ticket"))
	b.WriteString("\n\n")

	// Ticket summary directly under the title.
	if s := wizard.RenderTicketSummary(m.Ticket, m.SummaryCache[m.TicketID]); s != "" {
		b.WriteString(s)
		b.WriteString("\n")
	}

	// Search input. The unified match list below has Selected,
	// Available, and Suggested groups all in one place so each repo
	// appears exactly once — toggling on or off is just ↑/↓ to the
	// row and Enter.
	b.WriteString("  " + p.input.View())
	b.WriteString("\n\n")
	b.WriteString(p.renderMatches())

	if p.status != "" {
		b.WriteString("\n  " + wizard.WarnStyle.Render(p.status) + "\n")
	}

	// Two-line gap, then the LLM status (spinner while in flight,
	// dim summary when done). Sits below the match list so the
	// search remains the primary visual focus while the LLM works
	// in parallel.
	b.WriteString("\n\n")
	b.WriteString(p.renderLLMStatus(m))
	return wizard.Indent(b.String(), 2)
}

// renderLLMStatus draws the spinner + label while the LLM is in
// flight, dims to gray on success, surfaces a red error on failure,
// and renders empty when nothing has been kicked off yet (cache-hit
// on a same-session ticket).
func (p *reposPage) renderLLMStatus(m *wizard.Model) string {
	switch {
	case p.loading:
		secs := int(time.Since(p.loadStartAt).Seconds())
		return "  " + p.spinner.View() + " " +
			wizard.DimStyle.Render(fmt.Sprintf("looking for relevant repos… (%ds)", secs))
	case p.loadErr != nil:
		return "  " + wizard.ErrStyle.Render("✗ LLM detection failed: "+p.loadErr.Error())
	case !p.loadStartAt.IsZero() && !p.loadFinishedAt.IsZero():
		dur := p.loadFinishedAt.Sub(p.loadStartAt).Seconds()
		return "  " + wizard.DimStyle.Render(fmt.Sprintf("● found %d relevant repo(s) in %.1fs",
			len(p.picks), dur))
	default:
		return ""
	}
}

// groupOf classifies a matchItem into one of the three rendering
// groups so renderMatches can emit a header line at each boundary.
type matchGroup int

const (
	groupSelected matchGroup = iota
	groupAvailable
	groupSuggested
)

func (it matchItem) group() matchGroup {
	switch {
	case it.selected:
		return groupSelected
	case it.llm:
		return groupSuggested
	default:
		return groupAvailable
	}
}

// renderMatches walks the unified match list and emits a section
// header whenever the group changes. Every repo appears in exactly
// one group:
//   - Selected (top): ✓ marker, LLM tag if it came from a pick
//   - Available (middle, query-only): plain fuzzy matches
//   - Suggested (bottom): LLM picks the user hasn't toggled yet
//
// Cursor highlighting is uniform across groups — navigation doesn't
// care about boundaries.
func (p *reposPage) renderMatches() string {
	if len(p.matches) == 0 {
		q := strings.TrimSpace(p.input.Value())
		if q != "" {
			return "    " + wizard.DimStyle.Render(fmt.Sprintf("no match for %q", q)) + "\n"
		}
		// Empty list + empty query: the textinput's own placeholder
		// ("type to filter the catalog") is already visible above,
		// so don't duplicate the hint here.
		return ""
	}

	const nameW = 38
	const descW = 70
	var b strings.Builder
	var prevGroup matchGroup = -1
	for i, it := range p.matches {
		if g := it.group(); g != prevGroup {
			if prevGroup != -1 {
				b.WriteString("\n")
			}
			b.WriteString("  " + wizard.SectionStyle.Render(headerFor(g, p)) + "\n")
			prevGroup = g
		}

		var marker, name string
		if i == p.cursor {
			marker = wizard.CursorStyle.Render("▶")
			name = wizard.HighlightStyle.Render(wizard.PadRight(it.name, nameW))
		} else {
			marker = " "
			name = wizard.PadRight(it.name, nameW)
		}

		// ✓ only on selected rows; plain space otherwise.
		check := " "
		if it.selected {
			check = wizard.SelectedTagStyle.Render("✓")
		}

		// Meta column: LLM tag + reason whenever the row originated
		// from a pick (preserves provenance on selected LLM rows),
		// otherwise the catalog description.
		var meta string
		if it.llm {
			if pk, ok := p.picks[it.name]; ok {
				meta = wizard.RelevanceTagStyle.Render(fmt.Sprintf("relevance %.0f%% ", pk.Confidence*100)) +
					wizard.DimStyle.Render(wizard.Truncate(pk.Reason, descW-12))
			}
		} else if d := p.descByName[it.name]; d != "" {
			meta = wizard.DimStyle.Render(wizard.Truncate(d, descW))
		}
		b.WriteString(fmt.Sprintf("    %s %s %s %s\n", marker, check, name, meta))
	}
	return b.String()
}

// headerFor returns the section header text for a group. Selected
// header carries the count so the user has a running total without
// the old standalone Selected block.
func headerFor(g matchGroup, p *reposPage) string {
	switch g {
	case groupSelected:
		return fmt.Sprintf("Selected (%d)", len(p.selectedOrder))
	case groupAvailable:
		return "Available"
	case groupSuggested:
		return "Suggested for this ticket"
	}
	return ""
}

// wizard.RankFuzzy wraps fuzzy.Find with a custom two-tier ranking so the
// results match user intuition: anything containing the query as a
// substring comes first (sorted by where the substring lands —
// earlier wins), and only THEN does the library's score-based
// ranking kick in for scattered matches.
//
// Why this exists: `sahilm/fuzzy` scores firstCharMatchBonus (+10)
// for matching at index 0, which means a scattered match starting
// at position 0 ("s-e-t-u-p" plucked out of "sentra-user-ops" →
// score 35) outranks a tight contiguous run starting later ("setup"
// inside "sentra-setup-service" → score 25, because the 7-char
// leading penalty caps at -15). Users typing "setup" overwhelmingly
// expect "*setup*" hits at the top.
