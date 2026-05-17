package config

import (
	"github.com/uribrecher/thicket/internal/tui/wizard"

	"context"
	"errors"
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
	m := newModel(wizard.ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: true})

	want := []string{"Welcome", "Git", "Tickets", "Agent", "Submit"}
	if len(m.Pages) != len(want) {
		t.Fatalf("page count = %d, want %d", len(m.Pages), len(want))
	}
	for i, w := range want {
		if got := m.Pages[i].Title(); got != w {
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
	m := newModel(wizard.ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})

	if m.Pages[0].Title() == "Welcome" {
		t.Fatalf("Welcome page included on re-run")
	}
	if m.Pages[0].Title() != "Git" {
		t.Errorf("first page = %q, want Git", m.Pages[0].Title())
	}
}

// TestInitModelSkipTicketsWithEnv drops the Tickets page when
// SHORTCUT_API_TOKEN is already set in the environment.
func TestInitModelSkipTicketsWithEnv(t *testing.T) {
	t.Setenv("SHORTCUT_API_TOKEN", "sc-xxx")
	t.Setenv("ANTHROPIC_API_KEY", "")

	d := config.Default()
	m := newModel(wizard.ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: true})

	for _, p := range m.Pages {
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

	m := newModel(wizard.ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	// Re-run + both env vars set → only Git, Agent, Submit pages.
	m.Active = len(m.Pages) - 1
	if m.Pages[m.Active].Title() != "Submit" {
		t.Fatalf("active page = %q, want Submit", m.Pages[m.Active].Title())
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(*wizard.Model)
	// The submit page emits wizard.ConfigDoneMsg as a cmd; deliver it.
	page, _ := mm.Pages[mm.Active].Update(mm, tea.KeyMsg{Type: tea.KeyEnter})
	mm.Pages[mm.Active] = page
	// Run the cmd manually.
	_, cmd := mm.Pages[mm.Active].Update(mm, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("submit page did not produce a cmd on Enter")
	}
	msg := cmd()
	mm.Update(msg)
	if !mm.ConfigResult.Confirmed {
		t.Errorf("Confirmed not set after submit Enter")
	}
	if mm.ConfigResult.Cfg == nil {
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
	m := newModel(wizard.ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	tp := findPage(t, m, "Tickets").(*ticketsPage)
	tp.InitCmd(m)
	sp := tp.picker

	sp.state = stateOpLoadingItemDetail
	sp.chosenAccount = "acct-1"
	it := secretsItem("itm-1", "Shortcut Token")
	sp.chosenItem = &it
	sp.onItemDetailLoaded(wizard.OpItemDetailLoadedMsg{
		PickerID: sp.id,
		ItemID:   "itm-1",
		Detail: &secrets.OnePasswordItemDetail{
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

// TestSecretPickerPreseedJumpsToValidated covers the re-run Path: // an existing op:// ref in config should drop the picker straight
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
	m := newModel(wizard.ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
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
	m := newModel(wizard.ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	m.ConfigOpHintDismissed = true
	if sp.shouldShowDarwinHint(m) {
		t.Errorf("hint shown despite m.ConfigOpHintDismissed=true")
	}
}

// TestSecretPickerStaleMsgsDropped verifies the picker ignores
// async results addressed to a different picker id. Without that
// guard, the Tickets page's picker would react to an op-items load
// that landed for the Agent page's picker.
func TestSecretPickerStaleMsgsDropped(t *testing.T) {
	t.Setenv("SHORTCUT_API_TOKEN", "")
	d := config.Default()
	m := newModel(wizard.ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	tp := findPage(t, m, "Tickets").(*ticketsPage)
	tp.InitCmd(m)
	sp := tp.picker
	sp.state = stateOpLoadingItems
	sp.chosenAccount = "acct-1"
	sp.update(m, wizard.OpItemsLoadedMsg{
		PickerID: sp.id + 99, // someone else's load
		Account:  "acct-1",
		Items:    []secrets.OnePasswordItem{secretsItem("x", "Other")},
	})
	if sp.state != stateOpLoadingItems {
		t.Errorf("stale msg leaked into picker state (now %v)", sp.state)
	}
}

func findPage(t *testing.T, m *wizard.Model, title string) wizard.Page {
	t.Helper()
	for _, pg := range m.Pages {
		if pg.Title() == title {
			return pg
		}
	}
	t.Fatalf("page %q not found in wizard", title)
	return nil
}

// TestInitGitPageCommitsOnAdvance writes the input values back to the
// shared config when the page receives wizard.GoNextMsg.
func TestGitPage_singleOrgAutoFillsTextinput(t *testing.T) {
	d := config.Default()
	m := newModel(wizard.ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	gp := m.Pages[0].(*gitPage)
	gp.InitCmd(m)

	gp.Update(m, wizard.ConfigOrgsLoadedMsg{Orgs: []string{"only-org"}})
	if got := gp.inputs[gitFieldOrgs].Value(); got != "only-org" {
		t.Errorf("single-org auto-fill: got %q, want %q", got, "only-org")
	}
	if gp.orgsPickerActive() {
		t.Errorf("picker should NOT be active for a single-org probe result")
	}
}

func TestGitPage_multiOrgFlipsToPicker(t *testing.T) {
	d := config.Default()
	m := newModel(wizard.ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	gp := m.Pages[0].(*gitPage)
	gp.InitCmd(m)

	gp.Update(m, wizard.ConfigOrgsLoadedMsg{Orgs: []string{"alpha", "beta", "gamma"}})
	if !gp.orgsPickerActive() {
		t.Fatalf("picker should be active for 3-org probe result")
	}
	// All orgs should be checked by default; textinput synced.
	got := splitOrgs(gp.inputs[gitFieldOrgs].Value())
	if strings.Join(got, ",") != "alpha,beta,gamma" {
		t.Errorf("default selection synced to textinput = %v, want all three", got)
	}
}

func TestGitPage_pickerSpaceTogglesAndSyncs(t *testing.T) {
	d := config.Default()
	m := newModel(wizard.ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	gp := m.Pages[0].(*gitPage)
	gp.InitCmd(m)
	// Focus the orgs field so picker-mode key handling kicks in.
	gp.focus = gitFieldOrgs

	gp.Update(m, wizard.ConfigOrgsLoadedMsg{Orgs: []string{"alpha", "beta"}})
	// Cursor starts at row 0 (alpha). Space toggles it off.
	gp.Update(m, tea.KeyMsg{Type: tea.KeySpace})
	if gp.selOrgs["alpha"] {
		t.Errorf("alpha should be deselected after space toggle")
	}
	if got := splitOrgs(gp.inputs[gitFieldOrgs].Value()); strings.Join(got, ",") != "beta" {
		t.Errorf("textinput after toggling alpha off: got %v, want [beta]", got)
	}
}

func TestGitPage_probeErrorLeavesTextinputAlone(t *testing.T) {
	d := config.Default()
	d.GithubOrgs = []string{"already-set"}
	m := newModel(wizard.ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	gp := m.Pages[0].(*gitPage)
	gp.InitCmd(m) // seeds textinput from cfg

	gp.Update(m, wizard.ConfigOrgsLoadedMsg{Err: errors.New("gh: not authenticated")})
	if got := gp.inputs[gitFieldOrgs].Value(); got != "already-set" {
		t.Errorf("probe error should leave textinput alone: got %q", got)
	}
	if gp.orgsPickerActive() {
		t.Errorf("picker should not activate on probe error")
	}
}

func TestGitPage_pickerRespectsPreseedSelection(t *testing.T) {
	// User re-running `thicket config` with a saved GithubOrgs
	// subset should land in the picker with only the previously-
	// selected orgs checked.
	d := config.Default()
	d.GithubOrgs = []string{"alpha", "gamma"} // beta already deselected
	m := newModel(wizard.ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	gp := m.Pages[0].(*gitPage)
	gp.InitCmd(m)

	gp.Update(m, wizard.ConfigOrgsLoadedMsg{Orgs: []string{"alpha", "beta", "gamma"}})
	if !gp.selOrgs["alpha"] || gp.selOrgs["beta"] || !gp.selOrgs["gamma"] {
		t.Errorf("preseed not honored: sel=%+v", gp.selOrgs)
	}
}

// TestTicketsPage_forkPicksGenerateEmitsDeferred verifies the
// brand-new-user path: "I don't have a token yet" → "Open browser"
// → ConfigDeferredMsg. The wizard handler upstream of this turns
// the message into ConfigResult.DeferredForToken so cmd/thicket can
// print the re-run hint and exit 0.
func TestTicketsPage_forkPicksGenerateEmitsDeferred(t *testing.T) {
	t.Setenv("SHORTCUT_API_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	d := config.Default()
	m := newModel(wizard.ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	tp := findPage(t, m, "Tickets").(*ticketsPage)
	tp.InitCmd(m)
	if tp.step != stepFork {
		t.Fatalf("fresh page should land on fork, got step=%v", tp.step)
	}
	// "generate" is the first fork option (idx 0); just press enter.
	tp.Update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if tp.step != stepGenerate {
		t.Fatalf("step after picking generate = %v, want stepGenerate", tp.step)
	}
	_, cmd := tp.Update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("generate-button enter did not produce a cmd")
	}
	if _, ok := cmd().(wizard.ConfigDeferredMsg); !ok {
		t.Errorf("generate-button cmd produced %T, want ConfigDeferredMsg", cmd())
	}
}

// TestTicketsPage_forkPicksHaveOneEntersPicker verifies the
// "already have a token" branch lands on the existing secret picker.
func TestTicketsPage_forkPicksHaveOneEntersPicker(t *testing.T) {
	t.Setenv("SHORTCUT_API_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	d := config.Default()
	m := newModel(wizard.ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	tp := findPage(t, m, "Tickets").(*ticketsPage)
	tp.InitCmd(m)
	tp.Update(m, tea.KeyMsg{Type: tea.KeyDown}) // idx 0 → 1
	tp.Update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if tp.step != stepPicker {
		t.Errorf("step after picking have-one = %v, want stepPicker", tp.step)
	}
}

// TestTicketsPage_preseedSkipsFork verifies the re-run path: an
// existing op:// ref jumps the page past the fork so the user lands
// directly on the validated picker.
func TestTicketsPage_preseedSkipsFork(t *testing.T) {
	t.Setenv("SHORTCUT_API_TOKEN", "")

	d := config.Default()
	d.Passwords.Manager = "1password"
	d.Passwords.ShortcutTokenRef = "op://Prod/Shortcut/credential"
	m := newModel(wizard.ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	tp := findPage(t, m, "Tickets").(*ticketsPage)
	tp.InitCmd(m)
	if tp.step != stepPicker {
		t.Errorf("preseeded ref should skip fork: step=%v", tp.step)
	}
	if !tp.picker.validated() {
		t.Errorf("preseeded picker should be validated, state=%v", tp.picker.state)
	}
}

// TestShortcutTokensURL covers both branches: with and without a
// configured workspace slug.
func TestShortcutTokensURL(t *testing.T) {
	cases := []struct {
		slug string
		want string
	}{
		{"", "https://app.shortcut.com/settings/account/api-tokens"},
		{"sentra", "https://app.shortcut.com/sentra/settings/account/api-tokens"},
		{"  acme  ", "https://app.shortcut.com/acme/settings/account/api-tokens"},
	}
	for _, c := range cases {
		if got := shortcutTokensURL(c.slug); got != c.want {
			t.Errorf("shortcutTokensURL(%q) = %q, want %q", c.slug, got, c.want)
		}
	}
}

func TestInitGitPageCommitsOnAdvance(t *testing.T) {
	t.Setenv("SHORTCUT_API_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	d := config.Default()
	m := newModel(wizard.ConfigDeps{Ctx: context.Background(), Cfg: &d, FirstRun: false})
	gp, ok := m.Pages[0].(*gitPage)
	if !ok {
		t.Fatalf("first page is not the Git page: %T", m.Pages[0])
	}
	gp.InitCmd(m)
	gp.inputs[gitFieldReposRoot].SetValue("/tmp/code")
	gp.inputs[gitFieldWorkspaceRoot].SetValue("/tmp/work")
	gp.inputs[gitFieldOrgs].SetValue("alpha, beta")
	gp.Update(m, wizard.GoNextMsg{})
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
