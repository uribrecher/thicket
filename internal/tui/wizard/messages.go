package wizard

import (
	"time"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/detector"
	"github.com/uribrecher/thicket/internal/secrets"
	"github.com/uribrecher/thicket/internal/ticket"
	"github.com/uribrecher/thicket/internal/workspace"
)

// ----- edit-flow messages -----

// workspacesLoadedMsg carries the result of the ListManaged call that
// the Workspace page fires on first activation. Sync read, but routed
// through a tea.Cmd to keep the page's render loop clean.
type workspacesLoadedMsg struct {
	workspaces []workspace.ManagedWorkspace
	err        error
}

// workspaceCommittedMsg fires when the user advances past the
// Workspace page with a chosen workspace. The wizard intercepts to
// update shared state.
type workspaceCommittedMsg struct {
	ws *workspace.ManagedWorkspace
}

// additionsCommittedMsg fires when the user advances past the Repos
// page with their chosen additions.
type additionsCommittedMsg struct {
	additions []catalog.Repo
}

// editPlanBuiltMsg carries the post-init result of the submit page's
// plan build (BranchExists probes + clone-needed split).
type editPlanBuiltMsg struct {
	addPlan workspace.AddPlan
	toClone []catalog.Repo
	branch  string
	err     error
}

// editDoneMsg signals the submit page has finished its clone phase
// and the AddPlan is ready to execute. workspace.Add itself runs
// AFTER the wizard exits — same pattern as createDoneMsg.
type editDoneMsg struct {
	result EditResult
	err    error
}

// configDoneMsg signals the config Submit page has been confirmed. The
// wizard's handler stashes the populated config and quits; cmd/thicket
// then runs Validate + Save.
type configDoneMsg struct {
	err error
}

// secretValidatedMsg carries the result of a secretPicker's live
// reference test. `ref` and `manager` echo the pair we validated so
// the picker can drop late results when the user has since edited
// the inputs.
type secretValidatedMsg struct {
	ref     string
	manager string
	err     error
}

// opAccountsLoadedMsg carries the result of ListOnePasswordAccounts.
// pickerID lets a stale picker drop the message if the user has
// switched pages (or backed out of 1P mode) in the meantime.
type opAccountsLoadedMsg struct {
	pickerID int
	accounts []secrets.OnePasswordAccount
	err      error
}

// opItemsLoadedMsg carries the result of a per-account ListItems
// call. account is the UUID we listed against so the picker can
// double-check that the user is still on the same account.
type opItemsLoadedMsg struct {
	pickerID int
	account  string
	items    []secrets.OnePasswordItem
	err      error
}

// opItemDetailLoadedMsg carries the result of GetItem (used to
// populate the field picker).
type opItemDetailLoadedMsg struct {
	pickerID int
	itemID   string
	detail   *secrets.OnePasswordItemDetail
	err      error
}

// goNextMsg is emitted when the user advances past the current page
// (via →, or auto-advance after a page's own commit work finishes).
// The wizard intercepts it to trigger advance(); back-nav is handled
// inline in the wizard's key router so no symmetric goPrevMsg exists.
type goNextMsg struct{}

// ticketsLoadedMsg lands when the initial ListAssigned call returns
// (or fails — err is non-nil in that case).
type ticketsLoadedMsg struct {
	tickets []ticket.Ticket
	err     error
}

// ticketFetchedMsg carries the result of the per-row re-Fetch the
// Ticket page does after a row is selected. The slim search payload
// doesn't carry the description / labels / requester — Fetch does.
type ticketFetchedMsg struct {
	tk  ticket.Ticket
	err error
}

// ticketCommittedMsg fires when the user advances past the Ticket
// page with a fetched ticket. The wizard intercepts to update shared
// state and invalidate caches if the ticket id changed.
type ticketCommittedMsg struct {
	tk ticket.Ticket
}

// existingWorkspaceMsg signals that the picked ticket already has a
// managed workspace — the wizard short-circuits and reuses it instead
// of building a new one.
type existingWorkspaceMsg struct {
	path string
}

// picksLoadedMsg carries the LLM's repo picks for the current ticket.
type picksLoadedMsg struct {
	ticketID string
	picks    []detector.RepoMatch
	err      error
}

// summarizedMsg carries the LLM-generated short summary for the
// current ticket. Only emitted when Deps.Summarize is wired — the
// unwired case never reaches this message. A non-nil err or an
// empty lines slice means the call produced no usable output; the
// renderer silently falls back to the dumb first-N-description-
// lines view, so a failed summary here is non-fatal.
type summarizedMsg struct {
	ticketID string
	lines    []string
	err      error
}

// reposCommittedMsg fires when the user advances past the Repos page.
type reposCommittedMsg struct {
	chosen []catalog.Repo
}

// planBuiltMsg carries the plan and the list of repos that still need
// cloning. Built once on entry into the Plan page.
type planBuiltMsg struct {
	plan    workspace.Plan
	toClone []catalog.Repo
	err     error
}

// cloneStartedMsg / cloneDoneMsg stream clone progress for the Plan page.
type cloneStartedMsg struct{ name string }
type cloneDoneMsg struct {
	name      string
	localPath string
	err       error
}

// createDoneMsg signals the workspace is fully materialized.
type createDoneMsg struct {
	result Result
	err    error
}

// tickMsg drives elapsed-time counters for in-flight async work.
type tickMsg time.Time

// cancelledMsg is the unified cancel signal — produced by esc/ctrl+c.
type cancelledMsg struct{}
