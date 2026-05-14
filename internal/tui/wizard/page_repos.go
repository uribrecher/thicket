package wizard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/detector"
)

const (
	maxRepoMatches = 8 // cap on regular (fuzzy or empty-query) match rows
	maxLLMRows     = 8 // cap on LLM-suggestion rows shown at the bottom
)

// matchItem is one row in the live match list. The `llm` bit tells the
// renderer to put it under the "LLM suggestions" divider and tag it
// with the LLM confidence + reason.
type matchItem struct {
	name string
	llm  bool
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
	// eagerly in initCmd so fuzzy search works while the LLM is still
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
	ti.Placeholder = "type to fuzzy-search the catalog"
	ti.Focus()
	ti.CharLimit = 80
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

func (p *reposPage) Complete() bool { return len(p.selectedOrder) > 0 }

// initCmd seeds the catalog synchronously (so search is immediately
// usable) and fires the LLM detect cmd if we don't already have picks
// cached for this ticket.
func (p *reposPage) initCmd(m *Model) tea.Cmd {
	if m.ticketID == "" {
		return nil
	}
	if p.loadedForID == m.ticketID {
		return nil
	}
	p.resetForNewTicket()
	p.seedCatalog(m)
	p.loadedForID = m.ticketID
	p.recompute()

	if cached, ok := m.llmCache[m.ticketID]; ok {
		p.setLLMPicks(cached)
		return nil
	}
	p.loading = true
	p.loadErr = nil
	p.loadStartAt = time.Now()
	p.loadFinishedAt = time.Time{}
	return tea.Batch(p.spinner.Tick, detectCmd(m))
}

// resetForNewTicket clears the page-local state so the next ticket's
// load starts from a clean slate. Called from initCmd when ticketID
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

// seedCatalog populates name lookup tables from m.deps.Repos and
// restores any selection the user already had (from a prior Plan-page
// trip + ← back). LLM picks are populated separately via setLLMPicks.
func (p *reposPage) seedCatalog(m *Model) {
	p.repos = m.deps.Repos
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
	for _, r := range m.chosen {
		if p.nameSet[r.Name] && !p.selected[r.Name] {
			p.selected[r.Name] = true
			p.selectedOrder = append(p.selectedOrder, r.Name)
		}
	}
}

// setLLMPicks records the LLM's suggestions for rendering at the
// bottom of the match list. It does NOT auto-select anything — the
// user makes the call manually. Setting these completes the load
// (loadFinishedAt) so the status line dims to gray.
func (p *reposPage) setLLMPicks(picks []detector.RepoMatch) {
	p.picks = make(map[string]detector.RepoMatch, len(picks))
	p.pickOrder = p.pickOrder[:0]
	for _, pk := range picks {
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

func detectCmd(m *Model) tea.Cmd {
	tk := m.ticket
	id := m.ticketID
	return func() tea.Msg {
		ctx := m.deps.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		picks, err := m.deps.Detect(ctx, tk, m.deps.Repos)
		return picksLoadedMsg{ticketID: id, picks: picks, err: err}
	}
}

func (p *reposPage) Update(m *Model, msg tea.Msg) (Page, tea.Cmd) {
	switch v := msg.(type) {
	case picksLoadedMsg:
		if v.ticketID != m.ticketID {
			return p, nil // stale msg from a previous ticket
		}
		p.loading = false
		if v.err != nil {
			p.loadErr = v.err
			return p, nil
		}
		p.setLLMPicks(v.picks)
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

	case goNextMsg:
		if !p.Complete() {
			return p, nil
		}
		chosen := make([]catalog.Repo, 0, len(p.selectedOrder))
		for _, r := range p.repos {
			if p.selected[r.Name] {
				chosen = append(chosen, r)
			}
		}
		// Update m.chosen synchronously here — same reason as the
		// Ticket page's commit: the wizard's advance() fires the
		// Plan page's initCmd IMMEDIATELY after this returns, and
		// initCmd reads m.chosen to decide whether to rebuild the
		// plan. If we only emitted reposCommittedMsg as a deferred
		// cmd, initCmd would see the OLD m.chosen and either keep
		// the stale plan (if the count happened to match) or rebuild
		// against outdated repos.
		m.chosen = append(m.chosen[:0], chosen...)
		return p, func() tea.Msg { return reposCommittedMsg{chosen: chosen} }

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
				p.toggle(p.matches[p.cursor].name)
				p.input.SetValue("")
				p.status = ""
				p.recompute()
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
		p.selectedOrder = removeFromSlice(p.selectedOrder, name)
		p.status = "− dropped " + name
		return
	}
	p.selected[name] = true
	p.selectedOrder = append(p.selectedOrder, name)
	p.status = "+ added " + name
}

// recompute rebuilds the match list:
//
//  1. Regular section (top): when the query is empty, this is the
//     user's current non-LLM selection (so they can drop items they
//     don't want). When the query is non-empty, it's the fuzzy
//     matches against catalog names, excluding LLM picks (which
//     always have their own dedicated section below).
//  2. LLM section (bottom): every LLM-suggested name present in the
//     catalog, in the order the LLM returned them. Shown even when
//     the regular section is empty.
func (p *reposPage) recompute() {
	q := strings.TrimSpace(p.input.Value())
	p.matches = p.matches[:0]

	// Regular matches.
	switch {
	case q == "":
		for _, n := range p.selectedOrder {
			if _, isLLM := p.picks[n]; isLLM {
				continue // LLM picks render below; don't duplicate
			}
			p.matches = append(p.matches, matchItem{name: n})
		}
	default:
		count := 0
		for _, mm := range fuzzy.Find(q, p.names) {
			if _, isLLM := p.picks[mm.Str]; isLLM {
				continue
			}
			p.matches = append(p.matches, matchItem{name: mm.Str})
			count++
			if count >= maxRepoMatches {
				break
			}
		}
	}

	// LLM-suggested matches at the bottom — always shown when the
	// catalog has them, regardless of query, so the user can always
	// see what the LLM recommended.
	count := 0
	for _, n := range p.pickOrder {
		if !p.nameSet[n] {
			continue // LLM hallucination — name not in catalog
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

func (p *reposPage) View(m *Model) string {
	var b strings.Builder

	// Page title.
	b.WriteString(titleStyle.Render("Select repos that are relevant for this ticket"))
	b.WriteString("\n\n")

	// Ticket summary directly under the title.
	if s := renderTicketSummary(m.ticket); s != "" {
		b.WriteString(s)
		b.WriteString("\n")
	}

	// Selected section: only when non-empty, above the search so the
	// user can see their cumulative state at a glance.
	if len(p.selectedOrder) > 0 {
		b.WriteString("  " + sectionStyle.Render(fmt.Sprintf("Selected (%d)", len(p.selectedOrder))))
		b.WriteString("\n")
		b.WriteString(p.renderSelected())
		b.WriteString("\n")
	}

	// Search section: input first, then the match list.
	b.WriteString("  " + sectionStyle.Render("Search"))
	b.WriteString("\n")
	b.WriteString("  " + p.input.View())
	b.WriteString("\n")
	b.WriteString(p.renderMatches())

	if p.status != "" {
		b.WriteString("\n  " + warnStyle.Render(p.status) + "\n")
	}

	// Two-line gap, then the LLM status (red pulse / dim done / red
	// error). Sits below the match list so the search remains the
	// primary visual focus while the LLM works in parallel.
	b.WriteString("\n\n")
	b.WriteString(p.renderLLMStatus(m))

	b.WriteString("\n")
	b.WriteString("  " + hintStyle.Render("↑/↓ navigate · enter toggles · type to filter"))
	return indent(b.String(), 2)
}

// renderLLMStatus draws the spinner + label while the LLM is in
// flight, dims to gray on success, surfaces a red error on failure,
// and renders empty when nothing has been kicked off yet (cache-hit
// on a same-session ticket).
func (p *reposPage) renderLLMStatus(m *Model) string {
	switch {
	case p.loading:
		secs := int(time.Since(p.loadStartAt).Seconds())
		return "  " + p.spinner.View() + " " +
			dimStyle.Render(fmt.Sprintf("looking for relevant repos… (%ds)", secs))
	case p.loadErr != nil:
		return "  " + errStyle.Render("✗ LLM detection failed: "+p.loadErr.Error())
	case !p.loadStartAt.IsZero() && !p.loadFinishedAt.IsZero():
		dur := p.loadFinishedAt.Sub(p.loadStartAt).Seconds()
		return "  " + dimStyle.Render(fmt.Sprintf("● found %d relevant repo(s) in %.1fs",
			len(p.picks), dur))
	default:
		return ""
	}
}

func (p *reposPage) renderSelected() string {
	const nameW = 38
	const reasonW = 70
	var b strings.Builder
	for _, n := range p.selectedOrder {
		mark := selectedTagStyle.Render("✓")
		name := padRight(n, nameW)
		var reason string
		if pk, ok := p.picks[n]; ok && pk.Reason != "" {
			reason = llmTagStyle.Render(fmt.Sprintf("LLM %.0f%% ", pk.Confidence*100)) +
				truncate(pk.Reason, reasonW-12)
		} else if d := p.descByName[n]; d != "" {
			reason = dimStyle.Render(truncate(d, reasonW))
		}
		b.WriteString(fmt.Sprintf("    %s %s %s\n", mark, name, reason))
	}
	return b.String()
}

// renderMatches walks the unified match list, inserting a dim
// "─── LLM suggestions ───" divider before the first LLM-flagged
// item. Cursor highlighting and (selected) tagging work uniformly
// across both sections.
func (p *reposPage) renderMatches() string {
	if len(p.matches) == 0 {
		// Empty list: distinguish "still loading" from "no matches for query".
		q := strings.TrimSpace(p.input.Value())
		if q != "" {
			return "    " + dimStyle.Render(fmt.Sprintf("no match for %q", q)) + "\n"
		}
		return "    " + dimStyle.Render("type to filter the catalog") + "\n"
	}

	const nameW = 38
	const descW = 70
	var b strings.Builder
	dividerEmitted := false
	for i, it := range p.matches {
		if it.llm && !dividerEmitted {
			b.WriteString("    " + dimStyle.Render("─── Suggested for this ticket ───") + "\n")
			dividerEmitted = true
		}
		var marker, name string
		if i == p.cursor {
			marker = cursorStyle.Render("▶")
			name = highlightStyle.Render(padRight(it.name, nameW))
		} else {
			marker = " "
			name = padRight(it.name, nameW)
		}
		tail := ""
		if p.selected[it.name] {
			tail = selectedTagStyle.Render(" (selected)")
		}
		var meta string
		if it.llm {
			if pk, ok := p.picks[it.name]; ok {
				meta = llmTagStyle.Render(fmt.Sprintf("LLM %.0f%% ", pk.Confidence*100)) +
					dimStyle.Render(truncate(pk.Reason, descW-12))
			}
		} else if d := p.descByName[it.name]; d != "" {
			meta = dimStyle.Render(truncate(d, descW))
		}
		b.WriteString(fmt.Sprintf("    %s %s %s%s\n", marker, name, meta, tail))
	}
	return b.String()
}

// removeFromSlice mirrors the helper in internal/tui — duplicated so
// the wizard package stays self-contained.
func removeFromSlice(s []string, v string) []string {
	out := s[:0]
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
