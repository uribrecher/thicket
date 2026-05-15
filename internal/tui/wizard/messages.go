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

// WorkspacesLoadedMsg carries the result of the ListManaged call that
// the Workspace page fires on first activation. Sync read, but routed
// through a tea.Cmd to keep the page's render loop clean.
type WorkspacesLoadedMsg struct {
	workspaces []workspace.ManagedWorkspace
	err        error
}

// WorkspaceCommittedMsg fires when the user advances past the
// Workspace page with a chosen workspace. The wizard intercepts to
// update shared state.
type WorkspaceCommittedMsg struct {
	ws *workspace.ManagedWorkspace
}

// AdditionsCommittedMsg fires when the user advances past the Repos
// page with their chosen additions.
type AdditionsCommittedMsg struct {
	additions []catalog.Repo
}

// EditPlanBuiltMsg carries the post-init result of the submit page's
// plan build (BranchExists probes + clone-needed split).
type EditPlanBuiltMsg struct {
	addPlan workspace.AddPlan
	toClone []catalog.Repo
	branch  string
	err     error
}

// EditDoneMsg signals the submit page has finished its clone phase
// and the AddPlan is ready to execute. workspace.Add itself runs
// AFTER the wizard exits — same pattern as CreateDoneMsg.
type EditDoneMsg struct {
	result EditResult
	err    error
}

// ConfigDoneMsg signals the config Submit page has been confirmed. The
// wizard's handler stashes the populated config and quits; cmd/thicket
// then runs Validate + Save.
type ConfigDoneMsg struct {
	err error
}

// SecretValidatedMsg carries the result of a secretPicker's live
// reference test. `ref` and `manager` echo the pair we validated so
// the picker can drop late results when the user has since edited
// the inputs.
type SecretValidatedMsg struct {
	ref     string
	manager string
	err     error
}

// OpAccountsLoadedMsg carries the result of ListOnePasswordAccounts.
// pickerID lets a stale picker drop the message if the user has
// switched pages (or backed out of 1P mode) in the meantime.
type OpAccountsLoadedMsg struct {
	pickerID int
	accounts []secrets.OnePasswordAccount
	err      error
}

// OpItemsLoadedMsg carries the result of a per-account ListItems
// call. account is the UUID we listed against so the picker can
// double-check that the user is still on the same account.
type OpItemsLoadedMsg struct {
	pickerID int
	account  string
	items    []secrets.OnePasswordItem
	err      error
}

// OpItemDetailLoadedMsg carries the result of GetItem (used to
// populate the field picker).
type OpItemDetailLoadedMsg struct {
	pickerID int
	itemID   string
	detail   *secrets.OnePasswordItemDetail
	err      error
}

// GoNextMsg is emitted when the user advances past the current page
// (via →, or auto-advance after a page's own commit work finishes).
// The wizard intercepts it to trigger advance(); back-nav is handled
// inline in the wizard's key router so no symmetric goPrevMsg exists.
type GoNextMsg struct{}

// TicketsLoadedMsg lands when the initial ListAssigned call returns
// (or fails — err is non-nil in that case).
type TicketsLoadedMsg struct {
	tickets []ticket.Ticket
	err     error
}

// TicketFetchedMsg carries the result of the per-row re-Fetch the
// Ticket page does after a row is selected. The slim search payload
// doesn't carry the description / labels / requester — Fetch does.
type TicketFetchedMsg struct {
	tk  ticket.Ticket
	err error
}

// TicketCommittedMsg fires when the user advances past the Ticket
// page with a fetched ticket. The wizard intercepts to update shared
// state and invalidate caches if the ticket id changed.
type TicketCommittedMsg struct {
	tk ticket.Ticket
}

// ExistingWorkspaceMsg signals that the picked ticket already has a
// managed workspace — the wizard short-circuits and reuses it instead
// of building a new one.
type ExistingWorkspaceMsg struct {
	path string
}

// PicksLoadedMsg carries the LLM's repo picks for the current ticket.
type PicksLoadedMsg struct {
	ticketID string
	picks    []detector.RepoMatch
	err      error
}

// SummarizedMsg carries the LLM-generated short summary for the
// current ticket. Only emitted when Deps.Summarize is wired — the
// unwired case never reaches this message. A non-nil err or an
// empty lines slice means the call produced no usable output; the
// renderer silently falls back to the dumb first-N-description-
// lines view, so a failed summary here is non-fatal.
type SummarizedMsg struct {
	ticketID string
	lines    []string
	err      error
}

// ReposCommittedMsg fires when the user advances past the Repos page.
type ReposCommittedMsg struct {
	chosen []catalog.Repo
}

// PlanBuiltMsg carries the plan and the list of repos that still need
// cloning. Built once on entry into the Plan page.
type PlanBuiltMsg struct {
	plan    workspace.Plan
	toClone []catalog.Repo
	err     error
}

// CloneStartedMsg / CloneDoneMsg stream clone progress for the Plan page.
type CloneStartedMsg struct{ name string }
type CloneDoneMsg struct {
	name      string
	localPath string
	err       error
}

// CreateDoneMsg signals the workspace is fully materialized.
type CreateDoneMsg struct {
	result Result
	err    error
}

// TickMsg drives elapsed-time counters for in-flight async work.
type TickMsg time.Time

// CancelledMsg is the unified cancel signal — produced by esc/ctrl+c.
type CancelledMsg struct{}
