// Package wizard hosts the shared Bubble Tea shell that the three
// per-flow wizards — start, edit, config — all reuse. The shell
// owns the Model, the Page interface, the tab/footer rendering,
// global key routing (esc/ctrl+c cancel, ←/→ between completed
// steps), and the cross-flow message types.
//
// The actual flows live one directory deeper:
//
//	wizard/start/  — Ticket → Repos → Plan
//	wizard/edit/   — Workspace → Repos → Submit
//	wizard/config/ — Welcome? → Git → Tickets? → Agent → Submit
//
// Each sub-package contributes its own pages + Run entry point and
// constructs a *wizard.Model with its mode-specific fields set
// (Deps / EditDeps / ConfigDeps + the appropriate result bucket).
//
// Enter is deliberately NOT a wizard-level binding — each page
// binds it to its own commit action (e.g. Ticket picks a row,
// Repos toggles, Plan triggers Create) so the footer never lies
// about what Enter does.
package wizard

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/detector"
	"github.com/uribrecher/thicket/internal/secrets"
	"github.com/uribrecher/thicket/internal/ticket"
	"github.com/uribrecher/thicket/internal/tui"
	"github.com/uribrecher/thicket/internal/workspace"
)

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
	Deps   Deps
	Pages  []Page
	Active int

	// Shared cross-page state.
	Ticket        ticket.Ticket // last committed ticket
	TicketID      string        // cache key; "" before page 0 commits
	LLMCache      map[string][]detector.RepoMatch
	SummaryCache  map[string][]string                    // ticketID → LLM-generated summary lines
	NicknameCache map[string]detector.NicknameSuggestion // ticketID → nickname + color suggestion
	Chosen        []catalog.Repo
	CloneInclude  map[string]bool

	// Terminal size — bubbletea sends WindowSizeMsg on resize.
	Width  int
	Height int

	// Terminal state.
	Err    error
	Done   bool
	Result Result

	// ----- edit-flow state -----
	// EditMode flips the wizard into "thicket edit" plumbing. The
	// start-flow fields above are then unused (and EditDeps replaces
	// Deps for the page callbacks that need it). Pages can dispatch
	// on this flag, though most edit pages live in their own files
	// and never look at it.
	EditMode          bool
	EditDeps          EditDeps
	SelectedWorkspace *workspace.ManagedWorkspace
	Additions         []catalog.Repo // repos the user picked to add
	EditResult        EditResult

	// ----- config-flow state -----
	// ConfigMode flips the wizard into "thicket config" plumbing. The
	// start/edit fields above are then unused — the config pages mutate
	// ConfigDeps.Cfg directly as the user fills in each page. On
	// Submit-confirm the wizard hands the populated Cfg back as
	// ConfigResult.Cfg; the post-wizard runConfig does the validate + save.
	ConfigMode   bool
	ConfigDeps   ConfigDeps
	ConfigResult ConfigResult

	// ConfigOpAccounts caches `op account list` once per wizard session
	// so toggling between Tickets and Agent pages doesn't fire a
	// second `op` call. nil before first load.
	ConfigOpAccounts []secrets.OnePasswordAccount
	// ConfigOpItemCache memoizes per-account `op item list` results so
	// the user only pays one biometric prompt per account across all
	// config pages.
	ConfigOpItemCache map[string][]secrets.OnePasswordItem

	// ConfigOpHintDismissed records whether the user has dismissed the
	// macOS "grant iTerm App Management" hint that the secret picker
	// shows after the first 1Password walk-through. Lives on the
	// Model (not the picker) so dismissing it on the Tickets page
	// doesn't make it re-appear on the Agent page.
	ConfigOpHintDismissed bool
}

// Init kicks off any page-init commands. The Ticket page fires its
// ListAssigned cmd here. Pages that don't need a startup cmd simply
// don't implement InitCmder.
func (m *Model) Init() tea.Cmd {
	if ic, ok := m.Pages[m.Active].(InitCmder); ok {
		return ic.InitCmd(m)
	}
	return nil
}

// InitCmder lets the wizard fire each page's startup cmd at the moment
// it becomes active for the first time, without forcing pages to know
// about each other.
type InitCmder interface {
	InitCmd(m *Model) tea.Cmd
}

// Update routes messages: global keys first (cancel + nav), then
// cross-page state updates the wizard intercepts, then forwarding to
// the active page.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.Width, m.Height = v.Width, v.Height

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
			m.Err = tui.ErrCancelled
			return m, tea.Quit
		case "left":
			if m.canGoPrev() {
				return m.gotoPage(m.Active - 1)
			}
			return m, nil
		case "right":
			if m.canGoNext() {
				return m.advance()
			}
			return m, nil
		}

	case GoNextMsg:
		// Pages can emit GoNextMsg to auto-advance once they finish
		// their own commit work (e.g. the Ticket page after Fetch).
		// We intercept it here and route through advance() instead of
		// letting the default fallthrough re-deliver it to the active
		// page — advance() itself re-sends GoNextMsg to the page, so
		// double-forwarding would cause the page to see two of them.
		if m.canGoNext() {
			return m.advance()
		}
		return m, nil

	case CancelledMsg:
		m.Err = tui.ErrCancelled
		return m, tea.Quit

	case ExistingWorkspaceMsg:
		m.Result.ReuseDir = v.Path
		m.Result.Ticket = m.Ticket
		m.Done = true
		return m, tea.Quit

	case TicketCommittedMsg:
		// Cache + chosen invalidation policy: if the user changed
		// tickets (or this is the first commit), wipe downstream
		// state so we don't carry over picks/toggles from the old id.
		if v.Tk.SourceID != m.TicketID {
			delete(m.LLMCache, m.TicketID)
			delete(m.SummaryCache, m.TicketID)
			delete(m.NicknameCache, m.TicketID)
			m.Chosen = nil
			m.CloneInclude = make(map[string]bool)
		}
		m.Ticket = v.Tk
		m.TicketID = v.Tk.SourceID
		// Fall through so the active page sees the message too.

	case PicksLoadedMsg:
		if v.Err == nil && v.TicketID == m.TicketID {
			m.LLMCache[v.TicketID] = v.Picks
		}
		// Fall through so the Repos page can render the result.

	case SummarizedMsg:
		// Cache wins-once-set. Summary failures are silent: the
		// renderer falls back to the dumb first-N-lines view, so we
		// just drop the message. Returning here keeps the active page
		// out of summarized-state plumbing entirely.
		if v.Err == nil && v.TicketID == m.TicketID && len(v.Lines) > 0 {
			m.SummaryCache[v.TicketID] = v.Lines
		}
		return m, nil

	case NicknameSuggestedMsg:
		// Cache wins-once-set, same shape as SummarizedMsg. We fall
		// through (not return) so the active page — usually the Plan
		// page — can react: pre-fill its editable input from the new
		// suggestion if the user hasn't typed yet. Accept the
		// suggestion as long as EITHER field is usable — a
		// color-only response is still actionable (we'll tint the
		// tab even if the user types their own nickname).
		if v.Err == nil && v.TicketID == m.TicketID &&
			(v.Suggestion.Nickname != "" || v.Suggestion.Color != "") {
			if m.NicknameCache == nil {
				m.NicknameCache = make(map[string]detector.NicknameSuggestion)
			}
			m.NicknameCache[v.TicketID] = v.Suggestion
		}
		// Fall through.

	case ReposCommittedMsg:
		m.Chosen = append(m.Chosen[:0], v.Chosen...)
		// Fall through.

	case CreateDoneMsg:
		if v.Err != nil {
			m.Err = v.Err
			return m, tea.Quit
		}
		m.Result = v.Result
		m.Result.Ticket = m.Ticket
		m.Done = true
		return m, tea.Quit

	case WorkspaceCommittedMsg:
		m.SelectedWorkspace = v.Ws
		// Fall through so the active page sees the message too.

	case AdditionsCommittedMsg:
		m.Additions = append(m.Additions[:0], v.Additions...)
		// Fall through.

	case EditDoneMsg:
		if v.Err != nil {
			m.Err = v.Err
			return m, tea.Quit
		}
		m.EditResult = v.Result
		if m.SelectedWorkspace != nil {
			m.EditResult.Workspace = *m.SelectedWorkspace
		}
		m.Done = true
		return m, tea.Quit

	case ConfigDoneMsg:
		if v.Err != nil {
			m.Err = v.Err
			return m, tea.Quit
		}
		m.ConfigResult.Cfg = m.ConfigDeps.Cfg
		m.ConfigResult.Confirmed = true
		m.Done = true
		return m, tea.Quit
	}

	// Forward to the active page.
	page, cmd := m.Pages[m.Active].Update(m, msg)
	m.Pages[m.Active] = page
	return m, cmd
}

// View composes the header, the active page's body, and the footer
// hint bar into the full screen.
func (m *Model) View() string {
	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n\n")
	b.WriteString(m.Pages[m.Active].View(m))
	b.WriteString("\n")
	b.WriteString(m.renderFooter())
	b.WriteString("\n")
	return b.String()
}

// renderHeader draws the horizontal tab bar. The active step is a
// filled pill (black text on bright pink bg); completed steps are green;
// pending steps are dim gray. No underline row, no ✓ glyphs —
// foreground/background contrast does the wayfinding.
func (m *Model) renderHeader() string {
	cells := make([]string, len(m.Pages))
	for i, p := range m.Pages {
		label := p.Title()
		switch {
		case i == m.Active:
			cells[i] = ActiveTabStyle.Render(label)
		case i < m.Active:
			cells[i] = CompletedTabStyle.Render(label)
		default:
			cells[i] = PendingTabStyle.Render(label)
		}
	}
	return "  " + strings.Join(cells, TabSepStyle.Render(" "))
}

// renderFooter draws a single hint line combining the active page's
// local key hints (↑/↓, enter, space …) with the wizard-level nav
// keys (←/→/esc). One line, dedup-free — the page's bottom-of-View
// hint block is gone so we never repeat ourselves.
func (m *Model) renderFooter() string {
	parts := []string{}
	if pageHints := m.Pages[m.Active].Hints(); pageHints != "" {
		parts = append(parts, pageHints)
	}
	if m.canGoPrev() {
		parts = append(parts, "← back")
	}
	if m.canGoNext() {
		parts = append(parts, "→ next")
	}
	parts = append(parts, "esc cancel")
	return "  " + HintStyle.Render(strings.Join(parts, " · "))
}

// canGoPrev reports whether ← should be honored. Disabled while the
// Plan page is mid-create so the user can't unwind a half-created
// workspace.
func (m *Model) canGoPrev() bool {
	if m.Active == 0 {
		return false
	}
	if pp, ok := m.Pages[m.Active].(NavLocker); ok && pp.Locked() {
		return false
	}
	return true
}

// canGoNext reports whether → / enter (on a non-last page) should
// advance.
func (m *Model) canGoNext() bool {
	if m.Active >= len(m.Pages)-1 {
		return false
	}
	return m.Pages[m.Active].Complete()
}

// NavLocker is implemented by pages that need to block tab nav (e.g.
// the Plan page while a workspace is being created).
type NavLocker interface {
	Locked() bool
}

// advance moves to the next page and fires its init cmd if it has one.
// Pages also see the message that triggered the advance via their
// Update — the page emits its own commit message in response, which
// the wizard's Update intercepts before bouncing back here.
//
// If the page set m.Done synchronously (e.g. Ticket page detecting
// an existing workspace and short-circuiting to "reuse"), we skip
// the active++ + init cmd entirely. Otherwise we'd kick off the next
// page's expensive setup (LLM detect on Repos, plan build on Plan)
// only to throw it away when the program quits.
func (m *Model) advance() (tea.Model, tea.Cmd) {
	// Let the current page collect its commit message before we move on.
	// We achieve this by routing a synthetic GoNextMsg to the page;
	// the page returns a cmd that yields the appropriate commit msg.
	page, cmd := m.Pages[m.Active].Update(m, GoNextMsg{})
	m.Pages[m.Active] = page
	if m.Done {
		return m, cmd
	}
	if m.Active < len(m.Pages)-1 {
		m.Active++
		// Fire init cmd for the newly-active page if it has one.
		if ic, ok := m.Pages[m.Active].(InitCmder); ok {
			if InitCmd := ic.InitCmd(m); InitCmd != nil {
				return m, tea.Batch(cmd, InitCmd)
			}
		}
	}
	return m, cmd
}

// gotoPage moves back to a previous (completed) page without firing
// its init cmd — going back is a peek, not a re-run.
func (m *Model) gotoPage(idx int) (tea.Model, tea.Cmd) {
	if idx < 0 || idx >= len(m.Pages) {
		return m, nil
	}
	m.Active = idx
	return m, nil
}
