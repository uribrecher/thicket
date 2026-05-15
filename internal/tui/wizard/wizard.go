// Package wizard implements the multi-page Bubble Tea UI for
// `thicket start`. The flow is three pages — Ticket, Repos, Plan —
// rendered as horizontal tabs at the top of the screen. The active
// step is a filled pill (black on bright pink), completed steps are green,
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
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/config"
	"github.com/uribrecher/thicket/internal/detector"
	gitops "github.com/uribrecher/thicket/internal/git"
	"github.com/uribrecher/thicket/internal/secrets"
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
	// Summarize, when set, returns up to detector.SummaryLines short
	// summary lines for the picked ticket. May be nil — the wizard
	// falls back to the first non-empty lines of the description so
	// the panel always renders something useful.
	Summarize func(ctx context.Context, tk ticket.Ticket) ([]string, error)
	Git       *gitops.Git
	Flags     Flags

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
	Deps   Deps
	Pages  []Page
	Active int

	// Shared cross-page state.
	Ticket       ticket.Ticket // last committed ticket
	TicketID     string        // cache key; "" before page 0 commits
	LLMCache     map[string][]detector.RepoMatch
	SummaryCache map[string][]string // ticketID → LLM-generated summary lines
	Chosen       []catalog.Repo
	CloneInclude map[string]bool

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
	if errors.Is(fm.Err, tui.ErrCancelled) {
		return Result{}, tui.ErrCancelled
	}
	if fm.Err != nil {
		return Result{}, fm.Err
	}
	return fm.Result, nil
}

func newModel(deps Deps) *Model {
	m := &Model{
		Deps:         deps,
		LLMCache:     make(map[string][]detector.RepoMatch),
		SummaryCache: make(map[string][]string),
		CloneInclude: make(map[string]bool),
	}
	m.Pages = []Page{
		newTicketPage(),
		newReposPage(),
		newPlanPage(),
	}
	// Preselected-ticket Path: seed the Ticket page so it renders
	// read-only summary, and start the wizard on Repos. The user can
	// still go ← to peek at the ticket details.
	if deps.Preselected != nil {
		tp := m.Pages[0].(*ticketPage)
		tp.preseed(*deps.Preselected)
		m.Ticket = *deps.Preselected
		m.TicketID = deps.Preselected.SourceID
		m.Active = 1
	}
	return m
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
