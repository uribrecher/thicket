// Package wizard implements the multi-page Bubble Tea UI for
// `thicket start`. The flow is three pages — Ticket, Repos, Plan —
// rendered as horizontal tabs at the top of the screen. The active
// step is a filled pill (black on yellow), completed steps are green,
// and untouched steps are dim gray. Left/right arrow keys move between
// completed steps; Esc cancels. Enter is deliberately NOT a wizard-level binding — each
// page binds it to its own commit action (Ticket picks a row, Repos
// toggles, Plan triggers Create) so the footer never lies about what
// Enter does.
//
// The wizard owns:
//   - a unified Bubble Tea Model that routes messages to the active page
//   - shared cross-page state (picked ticket, LLM picks cache, chosen repos)
//   - the in-page clone phase (a clone failure drops that repo from
//     the workspace; the rest proceed). workspace.Create itself runs
//     in plain stdout AFTER the wizard exits — bubbletea has torn
//     down its UI by then, so its progress lines render as normal
//     terminal output.
package wizard

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/config"
	"github.com/uribrecher/thicket/internal/detector"
	gitops "github.com/uribrecher/thicket/internal/git"
	"github.com/uribrecher/thicket/internal/ticket"
	"github.com/uribrecher/thicket/internal/tui"
	"github.com/uribrecher/thicket/internal/workspace"
)

// Deps wires the wizard to the rest of thicket without importing
// cmd/thicket — keeps the dependency graph one-way.
type Deps struct {
	Ctx    context.Context
	Cfg    *config.Config
	Src    ticket.Source
	Lister ticket.Lister // may be nil; callers that wired non-listers get an error page
	Repos  []catalog.Repo
	Detect func(ctx context.Context, tk ticket.Ticket, repos []catalog.Repo) ([]detector.RepoMatch, error)
	Git    *gitops.Git
	Flags  Flags

	// FindExistingWorkspace returns the path of an already-managed
	// workspace for the given ticket id, or "" if none exists. The
	// wizard calls it after a ticket is committed; a non-empty result
	// short-circuits the rest of the flow and triggers a "reuse" exit.
	FindExistingWorkspace func(ticketID string) string

	// Preselected, when non-nil, makes the wizard skip the picker on
	// the Ticket page and start on Repos. Used by the args-path of
	// `thicket start <id>` so the user doesn't have to re-pick a
	// ticket they already named on the command line.
	Preselected *ticket.Ticket
}

// Flags is the subset of CLI flags the wizard needs to honor.
type Flags struct {
	Branch string
	DryRun bool
}

// SkipReport records one repo the wizard dropped from the workspace
// because its clone failed. runStart prints these to stderr after the
// wizard exits so the user sees what was skipped.
type SkipReport struct {
	Name   string
	Reason string
}

// Result is what wizard.Run hands back to runStart on success.
type Result struct {
	Ticket  ticket.Ticket
	Plan    workspace.Plan
	Skipped []SkipReport

	// ReuseDir, when non-empty, signals the wizard short-circuited
	// because the ticket already had a managed workspace. The caller
	// should skip Create and launch Claude directly in ReuseDir.
	ReuseDir string
}

// Page is one screen of the wizard.
type Page interface {
	Update(m *Model, msg tea.Msg) (Page, tea.Cmd)
	View(m *Model) string
	Title() string
	Complete() bool
	// Hints returns the page-local key hint string ("↑/↓ navigate ·
	// enter picks", etc.) that the wizard footer prepends to its
	// global hints (←/→/esc). Pages return "" to opt out entirely.
	Hints() string
}

// Model is the unified tea.Model. The wizard runs as a single Bubble
// Tea program; the active page receives forwarded messages plus
// occasional `enter`-state callbacks from the parent.
type Model struct {
	deps   Deps
	pages  [3]Page
	active int

	// Shared cross-page state.
	ticket       ticket.Ticket // last committed ticket
	ticketID     string        // cache key; "" before page 0 commits
	llmCache     map[string][]detector.RepoMatch
	chosen       []catalog.Repo
	cloneInclude map[string]bool

	// Terminal size — bubbletea sends WindowSizeMsg on resize.
	width  int
	height int

	// Terminal state.
	err    error
	done   bool
	result Result
}

// Run shows the wizard. Returns the Result on success, tui.ErrCancelled
// if the user pressed Esc/Ctrl-C, or any other error from the underlying
// Bubble Tea program. A short-circuit reuse exit returns a Result with
// ReuseDir set; runStart inspects that and launches Claude directly.
func Run(deps Deps) (Result, error) {
	if deps.Ctx == nil {
		deps.Ctx = context.Background()
	}
	m := newModel(deps)
	finalModel, err := tea.NewProgram(m).Run()
	if err != nil {
		return Result{}, err
	}
	fm := finalModel.(*Model)
	if errors.Is(fm.err, tui.ErrCancelled) {
		return Result{}, tui.ErrCancelled
	}
	if fm.err != nil {
		return Result{}, fm.err
	}
	return fm.result, nil
}

func newModel(deps Deps) *Model {
	m := &Model{
		deps:         deps,
		llmCache:     make(map[string][]detector.RepoMatch),
		cloneInclude: make(map[string]bool),
	}
	m.pages = [3]Page{
		newTicketPage(),
		newReposPage(),
		newPlanPage(),
	}
	// Preselected-ticket path: seed the Ticket page so it renders
	// read-only summary, and start the wizard on Repos. The user can
	// still go ← to peek at the ticket details.
	if deps.Preselected != nil {
		tp := m.pages[0].(*ticketPage)
		tp.preseed(*deps.Preselected)
		m.ticket = *deps.Preselected
		m.ticketID = deps.Preselected.SourceID
		m.active = 1
	}
	return m
}

// Init kicks off any page-init commands. The Ticket page fires its
// ListAssigned cmd here.
func (m *Model) Init() tea.Cmd {
	return m.pages[m.active].(initCmder).initCmd(m)
}

// initCmder lets the wizard fire each page's startup cmd at the moment
// it becomes active for the first time, without forcing pages to know
// about each other.
type initCmder interface {
	initCmd(m *Model) tea.Cmd
}

// Update routes messages: global keys first (cancel + nav), then
// cross-page state updates the wizard intercepts, then forwarding to
// the active page.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height

	case tea.KeyMsg:
		// Global keys take precedence over page-local handling so the
		// user can always cancel and navigate tabs. Enter is
		// deliberately NOT global: each page binds it to its own
		// commit action (pick / toggle / create) and a wizard-level
		// "enter advances" would either lie about what Enter does on
		// the Repos page (where it toggles) or steal it from pages
		// that need it.
		switch v.String() {
		case "ctrl+c", "esc":
			m.err = tui.ErrCancelled
			return m, tea.Quit
		case "left":
			if m.canGoPrev() {
				return m.gotoPage(m.active - 1)
			}
			return m, nil
		case "right":
			if m.canGoNext() {
				return m.advance()
			}
			return m, nil
		}

	case goNextMsg:
		// Pages can emit goNextMsg to auto-advance once they finish
		// their own commit work (e.g. the Ticket page after Fetch).
		// We intercept it here and route through advance() instead of
		// letting the default fallthrough re-deliver it to the active
		// page — advance() itself re-sends goNextMsg to the page, so
		// double-forwarding would cause the page to see two of them.
		if m.canGoNext() {
			return m.advance()
		}
		return m, nil

	case cancelledMsg:
		m.err = tui.ErrCancelled
		return m, tea.Quit

	case existingWorkspaceMsg:
		m.result.ReuseDir = v.path
		m.result.Ticket = m.ticket
		m.done = true
		return m, tea.Quit

	case ticketCommittedMsg:
		// Cache + chosen invalidation policy: if the user changed
		// tickets (or this is the first commit), wipe downstream
		// state so we don't carry over picks/toggles from the old id.
		if v.tk.SourceID != m.ticketID {
			delete(m.llmCache, m.ticketID)
			m.chosen = nil
			m.cloneInclude = make(map[string]bool)
		}
		m.ticket = v.tk
		m.ticketID = v.tk.SourceID
		// Fall through so the active page sees the message too.

	case picksLoadedMsg:
		if v.err == nil && v.ticketID == m.ticketID {
			m.llmCache[v.ticketID] = v.picks
		}
		// Fall through so the Repos page can render the result.

	case reposCommittedMsg:
		m.chosen = append(m.chosen[:0], v.chosen...)
		// Fall through.

	case createDoneMsg:
		if v.err != nil {
			m.err = v.err
			return m, tea.Quit
		}
		m.result = v.result
		m.result.Ticket = m.ticket
		m.done = true
		return m, tea.Quit
	}

	// Forward to the active page.
	page, cmd := m.pages[m.active].Update(m, msg)
	m.pages[m.active] = page
	return m, cmd
}

// View composes the header, the active page's body, and the footer
// hint bar into the full screen.
func (m *Model) View() string {
	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n\n")
	b.WriteString(m.pages[m.active].View(m))
	b.WriteString("\n")
	b.WriteString(m.renderFooter())
	b.WriteString("\n")
	return b.String()
}

// renderHeader draws the horizontal tab bar. The active step is a
// filled pill (black text on yellow bg); completed steps are green;
// pending steps are dim gray. No underline row, no ✓ glyphs —
// foreground/background contrast does the wayfinding.
func (m *Model) renderHeader() string {
	cells := make([]string, len(m.pages))
	for i, p := range m.pages {
		label := p.Title()
		switch {
		case i == m.active:
			cells[i] = activeTabStyle.Render(label)
		case i < m.active:
			cells[i] = completedTabStyle.Render(label)
		default:
			cells[i] = pendingTabStyle.Render(label)
		}
	}
	return "  " + strings.Join(cells, tabSepStyle.Render(" "))
}

// renderFooter draws a single hint line combining the active page's
// local key hints (↑/↓, enter, space …) with the wizard-level nav
// keys (←/→/esc). One line, dedup-free — the page's bottom-of-View
// hint block is gone so we never repeat ourselves.
func (m *Model) renderFooter() string {
	parts := []string{}
	if pageHints := m.pages[m.active].Hints(); pageHints != "" {
		parts = append(parts, pageHints)
	}
	if m.canGoPrev() {
		parts = append(parts, "← back")
	}
	if m.canGoNext() {
		parts = append(parts, "→ next")
	}
	parts = append(parts, "esc cancel")
	return "  " + hintStyle.Render(strings.Join(parts, " · "))
}

// canGoPrev reports whether ← should be honored. Disabled while the
// Plan page is mid-create so the user can't unwind a half-created
// workspace.
func (m *Model) canGoPrev() bool {
	if m.active == 0 {
		return false
	}
	if pp, ok := m.pages[m.active].(navLocker); ok && pp.locked() {
		return false
	}
	return true
}

// canGoNext reports whether → / enter (on a non-last page) should
// advance.
func (m *Model) canGoNext() bool {
	if m.active >= len(m.pages)-1 {
		return false
	}
	return m.pages[m.active].Complete()
}

// navLocker is implemented by pages that need to block tab nav (e.g.
// the Plan page while a workspace is being created).
type navLocker interface {
	locked() bool
}

// advance moves to the next page and fires its init cmd if it has one.
// Pages also see the message that triggered the advance via their
// Update — the page emits its own commit message in response, which
// the wizard's Update intercepts before bouncing back here.
//
// If the page set m.done synchronously (e.g. Ticket page detecting
// an existing workspace and short-circuiting to "reuse"), we skip
// the active++ + init cmd entirely. Otherwise we'd kick off the next
// page's expensive setup (LLM detect on Repos, plan build on Plan)
// only to throw it away when the program quits.
func (m *Model) advance() (tea.Model, tea.Cmd) {
	// Let the current page collect its commit message before we move on.
	// We achieve this by routing a synthetic goNextMsg to the page;
	// the page returns a cmd that yields the appropriate commit msg.
	page, cmd := m.pages[m.active].Update(m, goNextMsg{})
	m.pages[m.active] = page
	if m.done {
		return m, cmd
	}
	if m.active < len(m.pages)-1 {
		m.active++
		// Fire init cmd for the newly-active page if it has one.
		if ic, ok := m.pages[m.active].(initCmder); ok {
			if initCmd := ic.initCmd(m); initCmd != nil {
				return m, tea.Batch(cmd, initCmd)
			}
		}
	}
	return m, cmd
}

// gotoPage moves back to a previous (completed) page without firing
// its init cmd — going back is a peek, not a re-run.
func (m *Model) gotoPage(idx int) (tea.Model, tea.Cmd) {
	if idx < 0 || idx >= len(m.pages) {
		return m, nil
	}
	m.active = idx
	return m, nil
}

// Render-time helper shared by page bodies: indent each line by `n`
// spaces so the body sits flush under the tab bar at a consistent
// inset. Empty lines stay empty (no trailing spaces).
func indent(s string, n int) string {
	if s == "" {
		return s
	}
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if ln == "" {
			continue
		}
		lines[i] = pad + ln
	}
	return strings.Join(lines, "\n")
}

// fmtErr formats a chain of errors for inline rendering.
func fmtErr(err error) string {
	return fmt.Sprintf("error: %s", err.Error())
}

// renderTicketSummary draws a short header for the picked ticket:
// "<id> — <title>" plus up to 3 description lines, requester, and
// the first 3 labels. Used by the Repos page (where the user needs
// the context to evaluate repo picks) and the Ticket page's
// preselected-mode view (where the summary IS the content).
//
// Returns "" when there is no ticket to summarize so callers can
// skip the surrounding padding.
func renderTicketSummary(tk ticket.Ticket) string {
	if tk.SourceID == "" && tk.Title == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(warnStyle.Render(fmt.Sprintf("%s — %s", tk.SourceID, tk.Title)))
	b.WriteString("\n")
	for _, line := range firstNonEmptyLines(tk.Body, 3) {
		b.WriteString("  " + line + "\n")
	}
	if tk.Requester != "" {
		b.WriteString("  " + hintStyle.Render("requester: "+tk.Requester) + "\n")
	}
	if len(tk.Labels) > 0 {
		shown := tk.Labels
		if len(shown) > 3 {
			shown = shown[:3]
		}
		b.WriteString("  " + hintStyle.Render("labels: "+strings.Join(shown, ", ")) + "\n")
	}
	return b.String()
}
