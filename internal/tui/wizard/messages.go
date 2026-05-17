package wizard

import (
	"time"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/detector"
	"github.com/uribrecher/thicket/internal/secrets"
	"github.com/uribrecher/thicket/internal/ticket"
	"github.com/uribrecher/thicket/internal/workspace"
)

// All wizard-flow messages live in this one file (split into
// `start-flow`, `edit-flow`, `config-flow`, and `shared` banners
// below) so the root wizard.Update can switch on them without a
// cyclic import from the per-wizard sub-packages. Fields are
// exported because the producing pages live in sub-packages and
// build these struct literals across the package boundary.

// ----- edit-flow messages -----

// WorkspacesLoadedMsg carries the result of the ListManaged call that
// the Workspace page fires on first activation. Sync read, but routed
// through a tea.Cmd to keep the page's render loop clean.
type WorkspacesLoadedMsg struct {
	Workspaces []workspace.ManagedWorkspace
	Err        error
}

// WorkspaceCommittedMsg fires when the user advances past the
// Workspace page with a chosen workspace. The wizard intercepts to
// update shared state.
type WorkspaceCommittedMsg struct {
	Ws *workspace.ManagedWorkspace
}

// AdditionsCommittedMsg fires when the user advances past the Repos
// page with their chosen additions.
type AdditionsCommittedMsg struct {
	Additions []catalog.Repo
}

// EditPlanBuiltMsg carries the post-init result of the submit page's
// plan build (BranchExists probes + clone-needed split).
type EditPlanBuiltMsg struct {
	AddPlan workspace.AddPlan
	ToClone []catalog.Repo
	Branch  string
	Err     error
}

// EditDoneMsg signals the submit page has finished its clone phase
// and the AddPlan is ready to execute. workspace.Add itself runs
// AFTER the wizard exits — same pattern as CreateDoneMsg.
type EditDoneMsg struct {
	Result EditResult
	Err    error
}

// ----- config-flow messages -----

// ConfigDoneMsg signals the config Submit page has been confirmed. The
// wizard's handler stashes the populated config and quits; cmd/thicket
// then runs Validate + Save.
type ConfigDoneMsg struct {
	Err error
}

// ConfigDeferredMsg signals that the user bailed out of the config
// flow on the Tickets page to go mint a Shortcut API token in their
// browser. The wizard quits without writing the config; cmd/thicket
// prints a "re-run after you save the token" hint and exits 0.
type ConfigDeferredMsg struct{}

// SecretValidatedMsg carries the result of a secretPicker's live
// reference test. Ref and Manager echo the pair we validated so the
// picker can drop late results when the user has since edited the
// inputs.
type SecretValidatedMsg struct {
	Ref     string
	Manager string
	Err     error
}

// OpAccountsLoadedMsg carries the result of ListOnePasswordAccounts.
// PickerID lets a stale picker drop the message if the user has
// switched pages (or backed out of 1P mode) in the meantime.
type OpAccountsLoadedMsg struct {
	PickerID int
	Accounts []secrets.OnePasswordAccount
	Err      error
}

// OpItemsLoadedMsg carries the result of a per-account ListItems
// call. Account is the UUID we listed against so the picker can
// double-check that the user is still on the same account.
type OpItemsLoadedMsg struct {
	PickerID int
	Account  string
	Items    []secrets.OnePasswordItem
	Err      error
}

// OpItemDetailLoadedMsg carries the result of GetItem (used to
// populate the field picker).
type OpItemDetailLoadedMsg struct {
	PickerID int
	ItemID   string
	Detail   *secrets.OnePasswordItemDetail
	Err      error
}

// ----- start-flow messages -----

// TicketsLoadedMsg lands when the initial ListAssigned call returns
// (or fails — Err is non-nil in that case).
type TicketsLoadedMsg struct {
	Tickets []ticket.Ticket
	Err     error
}

// TicketFetchedMsg carries the result of the per-row re-Fetch the
// Ticket page does after a row is selected. The slim search payload
// doesn't carry the description / labels / requester — Fetch does.
type TicketFetchedMsg struct {
	Tk  ticket.Ticket
	Err error
}

// TicketCommittedMsg fires when the user advances past the Ticket
// page with a fetched ticket. The wizard intercepts to update shared
// state and invalidate caches if the ticket id changed.
type TicketCommittedMsg struct {
	Tk ticket.Ticket
}

// ExistingWorkspaceMsg signals that the picked ticket already has a
// managed workspace — the wizard short-circuits and reuses it instead
// of building a new one.
type ExistingWorkspaceMsg struct {
	Path string
}

// PicksLoadedMsg carries the LLM's repo picks for the current ticket.
type PicksLoadedMsg struct {
	TicketID string
	Picks    []detector.RepoMatch
	Err      error
}

// SummarizedMsg carries the LLM-generated short summary for the
// current ticket. Only emitted when Deps.Summarize is wired — the
// unwired case never reaches this message. A non-nil Err or an empty
// Lines slice means the call produced no usable output; the renderer
// silently falls back to the dumb first-N-description-lines view,
// so a failed summary here is non-fatal.
type SummarizedMsg struct {
	TicketID string
	Lines    []string
	Err      error
}

// ConfigOrgsLoadedMsg carries the result of probing `gh api
// user/orgs` from the config wizard's Git page. The page uses it to
// auto-populate the GitHub-orgs field: empty / nil Orgs leaves the
// textinput as-is (user types manually), one org auto-fills the
// field, and 2+ orgs flip the section into a checkbox multiselect.
// Failures are non-fatal — the user can still type orgs by hand.
type ConfigOrgsLoadedMsg struct {
	Orgs []string
	Err  error
}

// NicknameSuggestedMsg carries the LLM-suggested label (nickname +
// optional tab color) for the current ticket. Only emitted when
// Deps.Nickname is wired. The wizard caches the suggestion when
// EITHER field is non-empty — a color-only response is still
// actionable (the launcher will tint the tab even if the user types
// their own nickname). A non-nil Err with both fields empty is the
// "no usable output" case; the suggester is non-fatal, so the Plan
// page's input simply stays empty.
type NicknameSuggestedMsg struct {
	TicketID   string
	Suggestion detector.NicknameSuggestion
	Err        error
}

// ReposCommittedMsg fires when the user advances past the Repos page.
type ReposCommittedMsg struct {
	Chosen []catalog.Repo
}

// PlanBuiltMsg carries the plan and the list of repos that still need
// cloning. Built once on entry into the Plan page.
type PlanBuiltMsg struct {
	Plan    workspace.Plan
	ToClone []catalog.Repo
	Err     error
}

// CloneStartedMsg / CloneDoneMsg stream clone progress for the Plan page.
type CloneStartedMsg struct{ Name string }

type CloneDoneMsg struct {
	Name      string
	LocalPath string
	Err       error
}

// CreateDoneMsg signals the workspace is fully materialized.
type CreateDoneMsg struct {
	Result Result
	Err    error
}

// ----- shared messages -----

// GoNextMsg is emitted when the user advances past the current page
// (via →, or auto-advance after a page's own commit work finishes).
// The wizard intercepts it to trigger advance(); back-nav is handled
// inline in the wizard's key router so no symmetric goPrevMsg exists.
type GoNextMsg struct{}

// TickMsg drives elapsed-time counters for in-flight async work.
type TickMsg time.Time

// CancelledMsg is the unified cancel signal — produced by esc/ctrl+c.
type CancelledMsg struct{}
