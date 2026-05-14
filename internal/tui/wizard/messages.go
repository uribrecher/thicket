package wizard

import (
	"time"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/detector"
	"github.com/uribrecher/thicket/internal/ticket"
	"github.com/uribrecher/thicket/internal/workspace"
)

// goPrevMsg / goNextMsg are emitted by the wizard's own key router so
// page logic and tab nav share the same advance/back path.
type goPrevMsg struct{}
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

// reposCommittedMsg fires when the user advances past the Repos page.
type reposCommittedMsg struct {
	chosen []catalog.Repo
}

// planBuiltMsg carries the plan and the list of repos that still need
// cloning. Built once on entry into the Plan page.
type planBuiltMsg struct {
	plan     workspace.Plan
	toClone  []catalog.Repo
	err      error
}

// cloneStartedMsg / cloneDoneMsg stream clone progress for the Plan page.
type cloneStartedMsg struct{ name string }
type cloneDoneMsg struct {
	name      string
	localPath string
	err       error
}

// createProgressMsg streams workspace.Create's per-step progress lines.
type createProgressMsg struct{ line string }

// createDoneMsg signals the workspace is fully materialized.
type createDoneMsg struct {
	result Result
	err    error
}

// tickMsg drives elapsed-time counters for in-flight async work.
type tickMsg time.Time

// cancelledMsg is the unified cancel signal — produced by esc/ctrl+c.
type cancelledMsg struct{}
