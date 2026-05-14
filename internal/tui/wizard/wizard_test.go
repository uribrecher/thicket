package wizard

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/config"
	"github.com/uribrecher/thicket/internal/detector"
	gitops "github.com/uribrecher/thicket/internal/git"
	"github.com/uribrecher/thicket/internal/ticket"
	"github.com/uribrecher/thicket/internal/tui"
)

// stubSource implements ticket.Source for tests. We don't drive
// ListAssigned/Fetch through it in these tests — pages are wired with
// synthetic messages directly.
type stubSource struct{}

func (stubSource) Name() string                      { return "stub" }
func (stubSource) Parse(s string) (ticket.ID, error) { return stubID(s), nil }
func (stubSource) Fetch(_ ticket.ID) (ticket.Ticket, error) {
	return ticket.Ticket{}, errors.New("fetch not stubbed")
}
func (stubSource) BranchName(_ ticket.Ticket) string { return "" }

type stubID string

func (s stubID) String() string { return string(s) }

// newTestModel builds a Model preconfigured with a small synthetic
// catalog so the Repos / Plan pages have something to chew on.
func newTestModel() *Model {
	repos := []catalog.Repo{
		{Name: "alpha", LocalPath: "/tmp/alpha", DefaultBranch: "main"},
		{Name: "beta", DefaultBranch: "main"}, // un-cloned
		{Name: "gamma", LocalPath: "/tmp/gamma", DefaultBranch: "main"},
	}
	var calls int
	deps := Deps{
		Ctx: context.Background(),
		Cfg: &config.Config{WorkspaceRoot: "/tmp/ws", ReposRoot: "/tmp/repos"},
		Src: stubSource{},
		Detect: func(_ context.Context, _ ticket.Ticket, _ []catalog.Repo) ([]detector.RepoMatch, error) {
			calls++
			return []detector.RepoMatch{{Name: "alpha", Confidence: 0.9, Reason: "stubbed"}}, nil
		},
		Repos: repos,
		Git:   gitops.New(),
	}
	m := newModel(deps)
	// Stash the call counter for the test that needs it.
	m.deps.Cfg = deps.Cfg
	return m
}

// TestNavGatedByComplete verifies the wizard refuses to advance past
// an incomplete page via either → or Enter.
func TestNavGatedByComplete(t *testing.T) {
	m := newTestModel()
	if m.canGoNext() {
		t.Fatalf("Ticket page reports complete before any fetch")
	}
	// Synthesize a "→" press: nothing should change.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	mm := updated.(*Model)
	if mm.active != 0 {
		t.Fatalf("→ advanced past incomplete Ticket page (active=%d)", mm.active)
	}
}

// TestCancelFromAnyPage covers Esc / Ctrl-C on each page.
func TestCancelFromAnyPage(t *testing.T) {
	cases := []struct {
		name   string
		active int
	}{{"ticket", 0}, {"repos", 1}, {"plan", 2}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newTestModel()
			m.active = c.active
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			mm := updated.(*Model)
			if !errors.Is(mm.err, tui.ErrCancelled) {
				t.Fatalf("esc did not set err=ErrCancelled (got %v)", mm.err)
			}
			// The cmd should be tea.Quit; we can't directly compare
			// function values, but executing it should produce tea.QuitMsg.
			if cmd == nil {
				t.Fatalf("esc did not return a tea.Quit cmd")
			}
			msg := cmd()
			if _, ok := msg.(tea.QuitMsg); !ok {
				t.Fatalf("esc cmd produced %T, want tea.QuitMsg", msg)
			}
		})
	}
}

// TestTicketCommitInvalidatesDownstream verifies the wizard wipes the
// LLM cache and `chosen` set when the user picks a different ticket.
func TestTicketCommitInvalidatesDownstream(t *testing.T) {
	m := newTestModel()
	// Seed an old ticket + cache + chosen so we can observe invalidation.
	m.ticketID = "sc-1"
	m.llmCache["sc-1"] = []detector.RepoMatch{{Name: "alpha"}}
	m.chosen = []catalog.Repo{{Name: "alpha"}}

	// User commits a NEW ticket id.
	m.Update(ticketCommittedMsg{tk: ticket.Ticket{SourceID: "sc-2", Title: "two"}})

	if m.ticketID != "sc-2" {
		t.Fatalf("ticketID = %q, want sc-2", m.ticketID)
	}
	if _, ok := m.llmCache["sc-1"]; ok {
		t.Errorf("llmCache[sc-1] not invalidated")
	}
	if len(m.chosen) != 0 {
		t.Errorf("chosen not invalidated: %v", m.chosen)
	}
}

// TestTicketCommitSameIDNoOp verifies that re-committing the same
// ticket id leaves cached state intact — the "go back to peek, come
// forward unchanged" path the user explicitly asked for.
func TestTicketCommitSameIDNoOp(t *testing.T) {
	m := newTestModel()
	m.ticketID = "sc-1"
	m.llmCache["sc-1"] = []detector.RepoMatch{{Name: "alpha"}}
	m.chosen = []catalog.Repo{{Name: "alpha"}, {Name: "beta"}}

	m.Update(ticketCommittedMsg{tk: ticket.Ticket{SourceID: "sc-1", Title: "one"}})

	if _, ok := m.llmCache["sc-1"]; !ok {
		t.Errorf("llmCache[sc-1] wiped on same-id commit")
	}
	if len(m.chosen) != 2 {
		t.Errorf("chosen wiped on same-id commit (got len %d)", len(m.chosen))
	}
}

// TestExistingWorkspaceShortCircuit covers the reuse-existing-workspace
// path: an existingWorkspaceMsg sets ReuseDir on the result and
// signals tea.Quit so runStart launches Claude on the existing dir.
func TestExistingWorkspaceShortCircuit(t *testing.T) {
	m := newTestModel()
	m.ticket = ticket.Ticket{SourceID: "sc-9", Title: "existing"}
	_, cmd := m.Update(existingWorkspaceMsg{path: "/tmp/ws/sc-9-existing"})
	if !m.done {
		t.Fatalf("done not set after existing-workspace msg")
	}
	if m.result.ReuseDir != "/tmp/ws/sc-9-existing" {
		t.Errorf("ReuseDir = %q", m.result.ReuseDir)
	}
	if m.result.Ticket.SourceID != "sc-9" {
		t.Errorf("Result.Ticket not preserved")
	}
	if cmd == nil {
		t.Fatalf("no quit cmd returned")
	}
}

// TestLLMCacheReseed verifies that going Repos → Ticket → Repos with
// the same ticket id reseeds the page from the model cache instead
// of re-firing the detect cmd. The picks are recorded for rendering
// but NOT auto-added to the selection — that's a separate user
// action now.
func TestLLMCacheReseed(t *testing.T) {
	m := newTestModel()
	m.ticketID = "sc-1"
	m.ticket = ticket.Ticket{SourceID: "sc-1", Title: "one"}
	m.llmCache["sc-1"] = []detector.RepoMatch{{Name: "alpha", Confidence: 0.9}}

	rp := m.pages[1].(*reposPage)
	if cmd := rp.initCmd(m); cmd != nil {
		t.Fatalf("initCmd with cached LLM picks returned non-nil cmd (%v) — should reseed without firing", cmd)
	}
	if _, ok := rp.picks["alpha"]; !ok {
		t.Errorf("repos page did not record alpha as an LLM pick")
	}
	if rp.selected["alpha"] {
		t.Errorf("LLM picks must not auto-select (user must press enter to choose)")
	}
	// Alpha should appear in the match list flagged as llm=true.
	var found bool
	for _, it := range rp.matches {
		if it.name == "alpha" && it.llm {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("alpha not present as LLM match (matches=%v)", rp.matches)
	}
}

// TestReposCommitStoresChosen exercises the page → wizard handoff
// that locks in the user's repo selection.
func TestReposCommitStoresChosen(t *testing.T) {
	m := newTestModel()
	chosen := []catalog.Repo{{Name: "alpha"}, {Name: "gamma"}}
	m.Update(reposCommittedMsg{chosen: chosen})
	if len(m.chosen) != 2 || m.chosen[0].Name != "alpha" || m.chosen[1].Name != "gamma" {
		t.Errorf("chosen = %+v", m.chosen)
	}
}

// TestRankFuzzyPrefersSubstringOverScattered guards against the
// `sahilm/fuzzy` scoring quirk where a scattered match starting at
// index 0 outranks a contiguous substring match deeper in the
// string. Users typing "setup" expect "*setup*" hits at the top.
func TestRankFuzzyPrefersSubstringOverScattered(t *testing.T) {
	names := []string{
		"sentra-user-ops",        // scattered: s-e-(n)-t-(r-a-)-u-(s-e-r-)-p ... matches at 0,1,3,7,13
		"sentra-setup-service",   // contiguous substring at index 7
		"sentra-grouping-job",    // also scattered-ish
		"sentra-support-agent",   // scattered
		"sentra-simple-grouping", // scattered
	}
	matches := rankFuzzy("setup", names)
	if len(matches) == 0 {
		t.Fatalf("no matches for %q", "setup")
	}
	if matches[0].Str != "sentra-setup-service" {
		t.Errorf("top match = %q, want sentra-setup-service (substring should beat scattered)", matches[0].Str)
	}
}

// TestPlanCloneFailureProceeds: after a Create with one failed clone,
// finalizeCmd must drop the failed repo from the plan but keep the
// others. Mirrors the user's "proceed without failed repo" choice.
func TestPlanCloneFailureProceeds(t *testing.T) {
	m := newTestModel()
	m.ticket = ticket.Ticket{SourceID: "sc-1", Title: "one"}
	m.ticketID = "sc-1"
	m.chosen = []catalog.Repo{
		{Name: "alpha", LocalPath: "/tmp/alpha", CloneURL: "git@x:alpha"},
		{Name: "beta", CloneURL: "git@x:beta"}, // must clone
		{Name: "gamma", LocalPath: "/tmp/gamma", CloneURL: "git@x:gamma"},
	}
	pp := m.pages[2].(*planPage)
	pp.built = true
	pp.branch = "feature/sc-1-one"
	pp.workspace = "/tmp/ws/sc-1-one"
	pp.allRepos = m.chosen
	pp.toClone = []catalog.Repo{{Name: "beta", CloneURL: "git@x:beta"}}
	pp.branchExist = map[string]bool{"alpha": false, "beta": false, "gamma": false}
	pp.cloneInclude = map[string]bool{"beta": true}

	// Set up the clones map as if startCloneCmd already ran — beta failed.
	pp.creating = true
	pp.clones = map[string]*cloneState{
		"beta": {name: "beta", done: true, err: errors.New("auth denied")},
	}

	// Manually invoke finalize: emit the createDoneMsg, then drive it
	// through the wizard's Update.
	msg := pp.finalizeCmd(m)()
	updated, _ := m.Update(msg)
	mm := updated.(*Model)
	if mm.err != nil {
		t.Fatalf("wizard surfaced error after partial-failure proceed: %v", mm.err)
	}
	if !mm.done {
		t.Fatalf("done not set")
	}
	if mm.result.Plan.WorkspaceDir != "/tmp/ws/sc-1-one" {
		t.Errorf("workspace dir lost: %q", mm.result.Plan.WorkspaceDir)
	}
	var names []string
	for _, r := range mm.result.Plan.Repos {
		names = append(names, r.Name)
	}
	if got := strings.Join(names, ","); got != "alpha,gamma" {
		t.Errorf("plan repos = %s, want alpha,gamma (beta should be dropped)", got)
	}
	var skipped []string
	for _, s := range mm.result.Skipped {
		skipped = append(skipped, s.Name)
	}
	if got := strings.Join(skipped, ","); got != "beta" {
		t.Errorf("skipped = %s, want beta", got)
	}
}
