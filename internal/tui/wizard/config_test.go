package wizard

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uribrecher/thicket/internal/config"
	"github.com/uribrecher/thicket/internal/secrets"
)

// secretsItem is a tiny constructor used by tests to build a
// OnePasswordItem without redeclaring the verbose struct literal at
// every call site.
func secretsItem(id, title string) secrets.OnePasswordItem {
	return secrets.OnePasswordItem{ID: id, Title: title}
}

// TestInitModelFirstRunPages covers the page-set assembly for first-
// run init when neither secret env var is set: welcome → git →
// tickets → agent → submit.
func TestInitModelFirstRunPages(t *testing.T) {
	t.Setenv("SHORTCUT_API_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	d := config.Default()
	m := newConfigModel(ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: true})

	want := []string{"Welcome", "Git", "Tickets", "Agent", "Submit"}
	if len(m.pages) != len(want) {
		t.Fatalf("page count = %d, want %d", len(m.pages), len(want))
	}
	for i, w := range want {
		if got := m.pages[i].Title(); got != w {
			t.Errorf("page[%d].Title() = %q, want %q", i, got, w)
		}
	}
}

// TestInitModelSkipWelcomeOnReRun drops the Welcome page when the
// caller signals FirstRun=false.
func TestInitModelSkipWelcomeOnReRun(t *testing.T) {
	t.Setenv("SHORTCUT_API_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	d := config.Default()
	m := newConfigModel(ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})

	if m.pages[0].Title() == "Welcome" {
		t.Fatalf("Welcome page included on re-run")
	}
	if m.pages[0].Title() != "Git" {
		t.Errorf("first page = %q, want Git", m.pages[0].Title())
	}
}

// TestInitModelSkipTicketsWithEnv drops the Tickets page when
// SHORTCUT_API_TOKEN is already set in the environment.
func TestInitModelSkipTicketsWithEnv(t *testing.T) {
	t.Setenv("SHORTCUT_API_TOKEN", "sc-xxx")
	t.Setenv("ANTHROPIC_API_KEY", "")

	d := config.Default()
	m := newConfigModel(ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: true})

	for _, p := range m.pages {
		if p.Title() == "Tickets" {
			t.Fatalf("Tickets page included even though SHORTCUT_API_TOKEN is set")
		}
	}
}

// TestInitSubmitConfirms drives the submit page through an Enter
// press and verifies the wizard sets the confirmed result.
func TestInitSubmitConfirms(t *testing.T) {
	t.Setenv("SHORTCUT_API_TOKEN", "sc-xxx")
	t.Setenv("ANTHROPIC_API_KEY", "ak-xxx")

	d := config.Default()
	d.GithubOrgs = []string{"my-org"}
	d.ClaudeBackend = "cli"

	m := newConfigModel(ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	// Re-run + both env vars set → only Git, Agent, Submit pages.
	m.active = len(m.pages) - 1
	if m.pages[m.active].Title() != "Submit" {
		t.Fatalf("active page = %q, want Submit", m.pages[m.active].Title())
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(*Model)
	// The submit page emits ConfigDoneMsg as a cmd; deliver it.
	page, _ := mm.pages[mm.active].Update(mm, tea.KeyMsg{Type: tea.KeyEnter})
	mm.pages[mm.active] = page
	// Run the cmd manually.
	_, cmd := mm.pages[mm.active].Update(mm, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("submit page did not produce a cmd on Enter")
	}
	msg := cmd()
	mm.Update(msg)
	if !mm.configResult.Confirmed {
		t.Errorf("Confirmed not set after submit Enter")
	}
	if mm.configResult.Cfg == nil {
		t.Errorf("Cfg nil after confirm")
	}
}

// TestSecretPicker1PFieldPickerSkippedOnSingle exercises the
// state-machine path where a 1Password item exposes exactly one
// referenceable field — the picker must short-circuit straight to
// stateValidated instead of leaving the user stuck on a one-option
// field picker.
func TestSecretPicker1PFieldPickerSkippedOnSingle(t *testing.T) {
	t.Setenv("SHORTCUT_API_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	d := config.Default()
	m := newConfigModel(ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	tp := findPage(t, m, "Tickets").(*configTicketsPage)
	tp.InitCmd(m)
	sp := tp.picker

	sp.state = stateOpLoadingItemDetail
	sp.chosenAccount = "acct-1"
	it := secretsItem("itm-1", "Shortcut Token")
	sp.chosenItem = &it
	sp.onItemDetailLoaded(OpItemDetailLoadedMsg{
		pickerID: sp.id,
		itemID:   "itm-1",
		detail: &secrets.OnePasswordItemDetail{
			ID:    "itm-1",
			Title: "Shortcut Token",
			Fields: []secrets.OnePasswordField{
				{ID: "credential", Label: "credential", Type: "CONCEALED", Purpose: "PASSWORD",
					Reference: "op://Prod/Shortcut/credential"},
			},
		},
	})
	if sp.state != stateValidated {
		t.Fatalf("state = %v, want stateValidated (single field should auto-resolve)", sp.state)
	}
	if sp.chosenRef != "op://Prod/Shortcut/credential" {
		t.Errorf("chosenRef = %q", sp.chosenRef)
	}
	if sp.chosenMgr != "1password" {
		t.Errorf("chosenMgr = %q", sp.chosenMgr)
	}
}

// TestSecretPickerPreseedJumpsToValidated covers the re-run path:
// an existing op:// ref in config should drop the picker straight
// into stateValidated so the user doesn't have to re-walk the
// account → item → field cascade just to confirm what's already
// saved.
func TestSecretPickerPreseedJumpsToValidated(t *testing.T) {
	sp := newSecretPicker("Shortcut API token", "SHORTCUT_API_TOKEN")
	sp.preseed("1password", "op://Prod/Shortcut/credential")
	if sp.state != stateValidated {
		t.Fatalf("state = %v, want stateValidated", sp.state)
	}
	if sp.finalRef() != "op://Prod/Shortcut/credential" {
		t.Errorf("finalRef = %q", sp.finalRef())
	}
	if sp.finalManager() != "1password" {
		t.Errorf("finalManager = %q", sp.finalManager())
	}
}

// TestDarwinHintGatedOnWalk1P verifies the macOS App Management hint
// is suppressed when the user reached stateValidated via preseed
// (i.e. didn't actually fire an `op` call this run).
func TestDarwinHintGatedOnWalk1P(t *testing.T) {
	sp := newSecretPicker("Shortcut API token", "SHORTCUT_API_TOKEN")
	sp.preseed("1password", "op://Prod/Shortcut/credential")
	d := config.Default()
	m := newConfigModel(ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	if sp.shouldShowDarwinHint(m) {
		t.Errorf("hint shown even though user did not walk the 1P picker (walked1P=%v)", sp.walked1P)
	}
}

// TestDarwinHintSuppressedOnDismiss verifies that once the user
// dismisses the hint on the Tickets page, it doesn't reappear on
// the Agent page.
func TestDarwinHintSuppressedOnDismiss(t *testing.T) {
	sp := newSecretPicker("Anthropic API key", "ANTHROPIC_API_KEY")
	sp.walked1P = true
	sp.state = stateValidated
	sp.chosenMgr = "1password"
	d := config.Default()
	m := newConfigModel(ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	m.configOpHintDismissed = true
	if sp.shouldShowDarwinHint(m) {
		t.Errorf("hint shown despite m.configOpHintDismissed=true")
	}
}

// TestSecretPickerStaleMsgsDropped verifies the picker ignores
// async results addressed to a different picker id. Without that
// guard, the Tickets page's picker would react to an op-items load
// that landed for the Agent page's picker.
func TestSecretPickerStaleMsgsDropped(t *testing.T) {
	t.Setenv("SHORTCUT_API_TOKEN", "")
	d := config.Default()
	m := newConfigModel(ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	tp := findPage(t, m, "Tickets").(*configTicketsPage)
	tp.InitCmd(m)
	sp := tp.picker
	sp.state = stateOpLoadingItems
	sp.chosenAccount = "acct-1"
	sp.update(m, OpItemsLoadedMsg{
		pickerID: sp.id + 99, // someone else's load
		account:  "acct-1",
		items:    []secrets.OnePasswordItem{secretsItem("x", "Other")},
	})
	if sp.state != stateOpLoadingItems {
		t.Errorf("stale msg leaked into picker state (now %v)", sp.state)
	}
}

func findPage(t *testing.T, m *Model, title string) Page {
	t.Helper()
	for _, pg := range m.pages {
		if pg.Title() == title {
			return pg
		}
	}
	t.Fatalf("page %q not found in wizard", title)
	return nil
}

// TestInitGitPageCommitsOnAdvance writes the input values back to the
// shared config when the page receives GoNextMsg.
func TestInitGitPageCommitsOnAdvance(t *testing.T) {
	t.Setenv("SHORTCUT_API_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	d := config.Default()
	m := newConfigModel(ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	gp, ok := m.pages[0].(*configGitPage)
	if !ok {
		t.Fatalf("first page is not the Git page: %T", m.pages[0])
	}
	gp.InitCmd(m)
	gp.inputs[gitFieldReposRoot].SetValue("/tmp/code")
	gp.inputs[gitFieldWorkspaceRoot].SetValue("/tmp/work")
	gp.inputs[gitFieldOrgs].SetValue("alpha, beta")
	gp.Update(m, GoNextMsg{})
	if d.ReposRoot != "/tmp/code" {
		t.Errorf("ReposRoot = %q", d.ReposRoot)
	}
	if d.WorkspaceRoot != "/tmp/work" {
		t.Errorf("WorkspaceRoot = %q", d.WorkspaceRoot)
	}
	if strings.Join(d.GithubOrgs, ",") != "alpha,beta" {
		t.Errorf("GithubOrgs = %v", d.GithubOrgs)
	}
}
