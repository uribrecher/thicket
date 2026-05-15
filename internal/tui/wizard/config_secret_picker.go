package wizard

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sahilm/fuzzy"

	"github.com/uribrecher/thicket/internal/secrets"
)

// secretPicker is a small page-embedded state machine that walks the
// user through "where does this secret live?". For non-1Password
// managers it's a plain manager-list + typed-ref input with live
// validation via mgr.Get. For 1Password it expands into the full
// account → item → field cascade — the same flow the old huh-based
// `thicket config` shipped, just rendered inline as Bubble Tea so we
// don't need to tear down and rebuild the wizard's tea.Program.
//
// State transitions:
//
//	stateManager ──── op ────► stateOpLoadingAccounts ── single acct ─► stateOpLoadingItems
//	      │                              │                                       │
//	      │                              └── multi acct ──► stateOpPickAccount ──┘
//	      │                                                                       │
//	      │                                                                       ▼
//	      │                                                              stateOpPickItem
//	      │                                                                       │
//	      │                                                                       ▼
//	      │                                                       stateOpLoadingItemDetail
//	      │                                                                       │
//	      │                                                                       ▼
//	      │                                                              stateOpPickField
//	      │                                                                       │
//	      │                                                                       ▼
//	      │                                                              stateValidated
//	      │                                                                       ▲
//	      └── non-op ─► stateTypedRef ── enter & mgr.Get OK ────────────────────────┘
//
// shift+tab steps back one state within the picker (useful for
// re-picking an account/item without abandoning the wizard).
type pickerState int

const (
	stateManager             pickerState = iota
	stateOpLoadingAccounts               // op account list in flight
	stateOpPickAccount                   // multi-account picker
	stateOpLoadingItems                  // op item list in flight (per chosen account)
	stateOpPickItem                      // item picker with fuzzy filter
	stateOpLoadingItemDetail             // op item get in flight
	stateOpPickField                     // field picker
	stateTypedRef                        // non-1P typed reference + mgr.Get validation
	stateValidated                       // done; ref+manager locked in
)

const opItemVisibleRows = 10

// nextPickerID generates the per-instance correlation id used to drop
// async results that arrive after the picker has moved on.
var nextPickerID atomic.Int32

// secretPicker is one slot's worth of state.
type secretPicker struct {
	secretLabel string
	envVar      string
	id          int

	state pickerState

	// Manager-pick state.
	managerIdx int

	// 1P account-pick state.
	accountCursor int
	chosenAccount string // UUID of the picked account

	// 1P item-pick state.
	itemInput   textinput.Model
	itemMatches []int // indexes into the cached items slice
	itemCursor  int
	chosenItem  *secrets.OnePasswordItem

	// 1P field-pick state.
	itemDetail   *secrets.OnePasswordItemDetail
	fieldOptions []fieldOption
	fieldCursor  int

	// Typed-ref state (non-1P).
	refInput textinput.Model

	// Validation state (only used in stateTypedRef while validating
	// and once stateValidated is reached).
	validating bool
	lastErr    error
	chosenRef  string
	chosenMgr  string

	// loadingSince is used to render elapsed-seconds counters during
	// the three loading states.
	loadingSince time.Time

	// walked1P is true once the user actually entered the 1Password
	// load/pick path (i.e. fired an `op` call) in this session.
	// preseed → stateValidated does NOT set this — we only nudge the
	// macOS App Management hint after a fresh walk, since re-runs
	// have presumably already lived through it.
	walked1P bool
}

// fieldOption is one row of the field picker.
type fieldOption struct {
	label     string
	reference string
	kind      string // "password", "secret", "text" — for the hint column
}

func newSecretPicker(label, envVar string) *secretPicker {
	idVal := int(nextPickerID.Add(1))
	ri := textinput.New()
	ri.CharLimit = 200
	ri.Width = 60
	ri.Prompt = "› "
	ri.Placeholder = "op://Vault/Item/field  (or env-var name, etc.)"

	ii := textinput.New()
	ii.CharLimit = 80
	ii.Width = 60
	ii.Prompt = "› "
	ii.Placeholder = "type to filter…"

	return &secretPicker{
		secretLabel: label,
		envVar:      envVar,
		id:          idVal,
		refInput:    ri,
		itemInput:   ii,
	}
}

// preseed populates manager + ref from existing config values. When
// the recorded manager is 1Password and the ref looks like a usable
// op:// URI, we jump straight to stateValidated so re-runs don't
// force the user to walk the picker again.
func (sp *secretPicker) preseed(manager, ref string) {
	if manager != "" {
		for i, m := range secrets.Supported {
			if m == manager {
				sp.managerIdx = i
				break
			}
		}
	}
	if ref == "" {
		return
	}
	switch manager {
	case "1password":
		if strings.HasPrefix(ref, "op://") {
			sp.chosenRef = ref
			sp.chosenMgr = "1password"
			sp.state = stateValidated
		}
	case "env":
		if looksLikeEnvVarName(ref) {
			sp.refInput.SetValue(ref)
			sp.chosenRef = ref
			sp.chosenMgr = "env"
			sp.state = stateValidated
		}
	default:
		// Trust the persisted ref for bitwarden / pass — the user
		// validated it once before; re-validation can wait until the
		// next runtime fetch.
		sp.refInput.SetValue(ref)
		sp.chosenRef = ref
		sp.chosenMgr = manager
		sp.state = stateValidated
	}
}

func (sp *secretPicker) manager() string { return secrets.Supported[sp.managerIdx] }
func (sp *secretPicker) ref() string     { return strings.TrimSpace(sp.refInput.Value()) }

// validated reports whether the picker has a usable (manager, ref)
// pair. The containing page consults this for its Complete() check.
func (sp *secretPicker) validated() bool { return sp.state == stateValidated }

// finalRef / finalManager return the picker's final outputs once
// validated (otherwise empty strings).
func (sp *secretPicker) finalRef() string {
	if sp.state == stateValidated {
		return sp.chosenRef
	}
	return ""
}

func (sp *secretPicker) finalManager() string {
	if sp.state == stateValidated {
		return sp.chosenMgr
	}
	return ""
}

// finalAccount returns the 1P account UUID when the validated ref
// came from a 1P walkthrough; empty for other managers.
func (sp *secretPicker) finalAccount() string {
	if sp.state == stateValidated && sp.chosenMgr == "1password" {
		return sp.chosenAccount
	}
	return ""
}

func (sp *secretPicker) hints() string {
	switch sp.state {
	case stateManager:
		return "↑/↓ pick manager · enter selects"
	case stateOpLoadingAccounts, stateOpLoadingItems, stateOpLoadingItemDetail:
		return "loading…"
	case stateOpPickAccount:
		return "↑/↓ pick account · enter selects · shift+tab back"
	case stateOpPickItem:
		return "↑/↓ navigate · type to filter · enter picks · shift+tab back"
	case stateOpPickField:
		return "↑/↓ pick field · enter selects · shift+tab back"
	case stateTypedRef:
		return "type ref · enter validates · shift+tab back"
	case stateValidated:
		return "✓ validated · shift+tab re-pick"
	}
	return ""
}

// update routes messages through the state machine. Returns any cmd
// the new state needs to fire (e.g. starting an async load).
func (sp *secretPicker) update(m *Model, msg tea.Msg) tea.Cmd {
	// Always handle stale-correlation drops first so we don't tangle
	// up the active state with messages meant for a previous picker
	// generation.
	switch v := msg.(type) {
	case opAccountsLoadedMsg:
		if v.pickerID != sp.id || sp.state != stateOpLoadingAccounts {
			return nil
		}
		return sp.onAccountsLoaded(m, v)
	case opItemsLoadedMsg:
		if v.pickerID != sp.id || sp.state != stateOpLoadingItems {
			return nil
		}
		return sp.onItemsLoaded(m, v)
	case opItemDetailLoadedMsg:
		if v.pickerID != sp.id || sp.state != stateOpLoadingItemDetail {
			return nil
		}
		return sp.onItemDetailLoaded(v)
	case secretValidatedMsg:
		if v.ref != sp.ref() || v.manager != sp.manager() || sp.state != stateTypedRef {
			return nil
		}
		sp.validating = false
		sp.lastErr = v.err
		if v.err == nil {
			sp.chosenRef = v.ref
			sp.chosenMgr = v.manager
			sp.state = stateValidated
		}
		return nil
	case tickMsg:
		// Drive elapsed-seconds counters during loading states.
		if sp.isLoading() {
			return sp.tickCmd()
		}
		return nil
	}

	k, isKey := msg.(tea.KeyMsg)
	if !isKey {
		// Forward to whichever textinput is live, if any.
		switch sp.state {
		case stateOpPickItem:
			prev := sp.itemInput.Value()
			var cmd tea.Cmd
			sp.itemInput, cmd = sp.itemInput.Update(msg)
			if sp.itemInput.Value() != prev {
				sp.recomputeItemMatches(m)
			}
			return cmd
		case stateTypedRef:
			prev := sp.refInput.Value()
			var cmd tea.Cmd
			sp.refInput, cmd = sp.refInput.Update(msg)
			if sp.refInput.Value() != prev {
				sp.lastErr = nil
			}
			return cmd
		}
		return nil
	}

	// shift+tab is the global "back one state" within the picker.
	// stateManager is the floor.
	if k.String() == "shift+tab" {
		return sp.stepBack(m)
	}

	return sp.routeKey(m, k)
}

func (sp *secretPicker) routeKey(m *Model, k tea.KeyMsg) tea.Cmd {
	switch sp.state {
	case stateManager:
		switch k.String() {
		case "up", "k":
			if sp.managerIdx > 0 {
				sp.managerIdx--
			}
		case "down", "j":
			if sp.managerIdx < len(secrets.Supported)-1 {
				sp.managerIdx++
			}
		case "enter":
			return sp.commitManager(m)
		}
		return nil

	case stateOpPickAccount:
		switch k.String() {
		case "up", "k":
			if sp.accountCursor > 0 {
				sp.accountCursor--
			}
		case "down", "j":
			if sp.accountCursor < len(m.configOpAccounts)-1 {
				sp.accountCursor++
			}
		case "enter":
			return sp.commitAccount(m, m.configOpAccounts[sp.accountCursor].AccountUUID)
		}
		return nil

	case stateOpPickItem:
		switch k.String() {
		case "up", "k":
			if sp.itemCursor > 0 {
				sp.itemCursor--
			}
			return nil
		case "down", "j":
			if sp.itemCursor < len(sp.itemMatches)-1 {
				sp.itemCursor++
			}
			return nil
		case "enter":
			if sp.itemCursor >= len(sp.itemMatches) {
				return nil
			}
			items := m.configOpItemCache[sp.chosenAccount]
			chosen := items[sp.itemMatches[sp.itemCursor]]
			return sp.commitItem(chosen)
		}
		// Forward typing to the filter input.
		prev := sp.itemInput.Value()
		var cmd tea.Cmd
		sp.itemInput, cmd = sp.itemInput.Update(k)
		if sp.itemInput.Value() != prev {
			sp.recomputeItemMatches(m)
		}
		return cmd

	case stateOpPickField:
		switch k.String() {
		case "up", "k":
			if sp.fieldCursor > 0 {
				sp.fieldCursor--
			}
		case "down", "j":
			if sp.fieldCursor < len(sp.fieldOptions)-1 {
				sp.fieldCursor++
			}
		case "enter":
			if sp.fieldCursor >= len(sp.fieldOptions) {
				return nil
			}
			opt := sp.fieldOptions[sp.fieldCursor]
			sp.chosenRef = opt.reference
			sp.chosenMgr = "1password"
			sp.state = stateValidated
		}
		return nil

	case stateTypedRef:
		if k.String() == "enter" {
			return sp.startTypedRefValidation(m.configDeps.Ctx)
		}
		// Forward to the input.
		prev := sp.refInput.Value()
		var cmd tea.Cmd
		sp.refInput, cmd = sp.refInput.Update(k)
		if sp.refInput.Value() != prev {
			sp.lastErr = nil
		}
		return cmd

	case stateValidated:
		// The shift+tab back-step is handled above. The wizard's → /
		// enter advances to the next page. The one extra binding
		// here is y/n for the macOS App Management hint shown after
		// a fresh 1P walk-through.
		if sp.shouldShowDarwinHint(m) {
			switch k.String() {
			case "y", "Y":
				openSystemSettingsAppManagement()
				m.configOpHintDismissed = true
			case "n", "N":
				m.configOpHintDismissed = true
			}
		}
		return nil
	}
	return nil
}

// commitManager fires the right async load for the picked manager,
// or transitions into the typed-ref state when the manager doesn't
// need a 1P-style cascade.
func (sp *secretPicker) commitManager(m *Model) tea.Cmd {
	switch sp.manager() {
	case "1password":
		sp.walked1P = true
		// Reuse cached accounts when available (e.g. the user already
		// walked the picker on the Tickets page and is now on Agent).
		if len(m.configOpAccounts) > 0 {
			return sp.enterAccountPick(m)
		}
		sp.state = stateOpLoadingAccounts
		sp.loadingSince = time.Now()
		return tea.Batch(sp.tickCmd(), loadOpAccountsCmd(sp.id))
	case "env":
		sp.state = stateTypedRef
		sp.refInput.Focus()
		if sp.refInput.Value() == "" && sp.envVar != "" {
			sp.refInput.SetValue(sp.envVar)
		}
		return textinput.Blink
	default:
		sp.state = stateTypedRef
		sp.refInput.Focus()
		return textinput.Blink
	}
}

func (sp *secretPicker) enterAccountPick(m *Model) tea.Cmd {
	// Single-account shortcut: skip the picker.
	if len(m.configOpAccounts) == 1 {
		return sp.commitAccount(m, m.configOpAccounts[0].AccountUUID)
	}
	sp.state = stateOpPickAccount
	// Default cursor to a previously-chosen account when one exists.
	for i, a := range m.configOpAccounts {
		if a.AccountUUID == sp.chosenAccount {
			sp.accountCursor = i
			return nil
		}
	}
	sp.accountCursor = 0
	return nil
}

func (sp *secretPicker) onAccountsLoaded(m *Model, msg opAccountsLoadedMsg) tea.Cmd {
	if msg.err != nil {
		sp.lastErr = msg.err
		sp.state = stateManager
		return nil
	}
	if len(msg.accounts) == 0 {
		sp.lastErr = fmt.Errorf("no 1Password accounts signed in — run `op signin` first")
		sp.state = stateManager
		return nil
	}
	m.configOpAccounts = msg.accounts
	return sp.enterAccountPick(m)
}

func (sp *secretPicker) commitAccount(m *Model, account string) tea.Cmd {
	sp.chosenAccount = account
	// Cache hit: skip the load.
	if items, ok := m.configOpItemCache[account]; ok && len(items) > 0 {
		return sp.enterItemPick(m, items)
	}
	sp.state = stateOpLoadingItems
	sp.loadingSince = time.Now()
	return tea.Batch(sp.tickCmd(), loadOpItemsCmd(sp.id, account))
}

func (sp *secretPicker) onItemsLoaded(m *Model, msg opItemsLoadedMsg) tea.Cmd {
	if msg.err != nil {
		sp.lastErr = msg.err
		sp.state = stateOpPickAccount
		return nil
	}
	if msg.account != sp.chosenAccount {
		// Stale load — drop.
		return nil
	}
	if len(msg.items) == 0 {
		sp.lastErr = fmt.Errorf("no 1Password items visible to this account")
		sp.state = stateOpPickAccount
		return nil
	}
	m.configOpItemCache[msg.account] = msg.items
	return sp.enterItemPick(m, msg.items)
}

func (sp *secretPicker) enterItemPick(m *Model, items []secrets.OnePasswordItem) tea.Cmd {
	sp.state = stateOpPickItem
	sp.itemInput.SetValue("")
	sp.itemInput.Focus()
	sp.itemCursor = 0
	sp.recomputeItemMatches(m)
	return textinput.Blink
}

// recomputeItemMatches re-runs the fuzzy filter over the cached items
// for the chosen account. Items are pre-sorted by credentialPriority
// before fuzzy matching so the most likely candidates surface first
// when the user hasn't typed a filter yet.
func (sp *secretPicker) recomputeItemMatches(m *Model) {
	items := m.configOpItemCache[sp.chosenAccount]
	sorted := sortedItems(items)
	// Replace the cache with the sorted copy so cursor indices line up
	// with the same order the View renders.
	m.configOpItemCache[sp.chosenAccount] = sorted
	q := strings.TrimSpace(sp.itemInput.Value())
	sp.itemMatches = sp.itemMatches[:0]
	if q == "" {
		for i := range sorted {
			if i >= opItemVisibleRows {
				break
			}
			sp.itemMatches = append(sp.itemMatches, i)
		}
	} else {
		haystack := make([]string, len(sorted))
		for i, it := range sorted {
			haystack[i] = it.Title + " " + it.Vault.Name
		}
		fm := fuzzy.Find(q, haystack)
		for i, mm := range fm {
			if i >= opItemVisibleRows {
				break
			}
			sp.itemMatches = append(sp.itemMatches, mm.Index)
		}
	}
	if sp.itemCursor >= len(sp.itemMatches) {
		sp.itemCursor = 0
	}
}

func (sp *secretPicker) commitItem(it secrets.OnePasswordItem) tea.Cmd {
	sp.chosenItem = &it
	sp.state = stateOpLoadingItemDetail
	sp.loadingSince = time.Now()
	return tea.Batch(sp.tickCmd(), loadOpItemDetailCmd(sp.id, sp.chosenAccount, it.ID))
}

func (sp *secretPicker) onItemDetailLoaded(msg opItemDetailLoadedMsg) tea.Cmd {
	if msg.err != nil {
		sp.lastErr = msg.err
		sp.state = stateOpPickItem
		return nil
	}
	if msg.detail == nil || len(msg.detail.Fields) == 0 {
		sp.lastErr = fmt.Errorf("item has no fields")
		sp.state = stateOpPickItem
		return nil
	}
	sp.itemDetail = msg.detail
	sp.fieldOptions = sp.fieldOptions[:0]
	defaultIdx := -1
	for _, f := range msg.detail.Fields {
		if f.Reference == "" {
			continue
		}
		opt := fieldOption{
			label:     firstNonEmptyStr(f.Label, f.ID),
			reference: f.Reference,
			kind:      friendlyFieldType(f.Type, f.Purpose),
		}
		sp.fieldOptions = append(sp.fieldOptions, opt)
		if defaultIdx == -1 && f.Type == "CONCEALED" {
			defaultIdx = len(sp.fieldOptions) - 1
		}
	}
	if len(sp.fieldOptions) == 0 {
		sp.lastErr = fmt.Errorf("item %q exposes no referenceable fields", msg.detail.Title)
		sp.state = stateOpPickItem
		return nil
	}
	// Skip the field picker when there's only one option.
	if len(sp.fieldOptions) == 1 {
		sp.chosenRef = sp.fieldOptions[0].reference
		sp.chosenMgr = "1password"
		sp.state = stateValidated
		return nil
	}
	if defaultIdx < 0 {
		defaultIdx = 0
	}
	sp.fieldCursor = defaultIdx
	sp.state = stateOpPickField
	return nil
}

// stepBack returns the picker to the previous logical state. Helpful
// when the user wants to re-pick an account/item without abandoning
// the wizard. From stateManager it's a no-op.
func (sp *secretPicker) stepBack(m *Model) tea.Cmd {
	switch sp.state {
	case stateManager:
		return nil
	case stateOpLoadingAccounts, stateOpPickAccount, stateTypedRef:
		sp.state = stateManager
		sp.lastErr = nil
		return nil
	case stateOpLoadingItems, stateOpPickItem:
		if len(m.configOpAccounts) > 1 {
			sp.state = stateOpPickAccount
		} else {
			sp.state = stateManager
		}
		sp.lastErr = nil
		return nil
	case stateOpLoadingItemDetail, stateOpPickField:
		// Re-enter the item picker with the cached items.
		items := m.configOpItemCache[sp.chosenAccount]
		if len(items) == 0 {
			sp.state = stateManager
			return nil
		}
		return sp.enterItemPick(m, items)
	case stateValidated:
		// Back up to wherever was prior: 1P → field-pick, env/other → typed-ref.
		switch sp.chosenMgr {
		case "1password":
			if len(sp.fieldOptions) > 1 {
				sp.state = stateOpPickField
			} else if items := m.configOpItemCache[sp.chosenAccount]; len(items) > 0 {
				return sp.enterItemPick(m, items)
			} else {
				sp.state = stateManager
			}
		default:
			sp.state = stateTypedRef
			sp.refInput.Focus()
		}
		sp.lastErr = nil
		return nil
	}
	return nil
}

// startTypedRefValidation fires the non-1P live fetch.
func (sp *secretPicker) startTypedRefValidation(ctx context.Context) tea.Cmd {
	ref := sp.ref()
	mgrName := sp.manager()
	if ref == "" {
		return nil
	}
	sp.validating = true
	sp.lastErr = nil
	return func() tea.Msg {
		if mgrName == "env" {
			if !looksLikeEnvVarName(ref) {
				return secretValidatedMsg{
					ref:     ref,
					manager: mgrName,
					err:     fmt.Errorf("env-var name must match [A-Z_][A-Z0-9_]* (uppercase, underscores)"),
				}
			}
			return secretValidatedMsg{ref: ref, manager: mgrName}
		}
		mgr, err := secrets.New(mgrName)
		if err != nil {
			return secretValidatedMsg{ref: ref, manager: mgrName, err: err}
		}
		if _, err := mgr.Get(ctx, ref); err != nil {
			return secretValidatedMsg{ref: ref, manager: mgrName, err: err}
		}
		return secretValidatedMsg{ref: ref, manager: mgrName}
	}
}

func (sp *secretPicker) isLoading() bool {
	switch sp.state {
	case stateOpLoadingAccounts, stateOpLoadingItems, stateOpLoadingItemDetail:
		return true
	}
	return false
}

func (sp *secretPicker) tickCmd() tea.Cmd {
	if !sp.isLoading() {
		return nil
	}
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// view renders the picker's current state. Composes the section
// header (set by the page) with the per-state body.
func (sp *secretPicker) view(m *Model) string {
	var b strings.Builder
	b.WriteString(sectionStyle.Render("Password manager") + "\n")
	for i, name := range secrets.Supported {
		marker := "  "
		style := dimStyle
		if i == sp.managerIdx {
			if sp.state == stateManager {
				marker = cursorStyle.Render("▶ ")
				style = cursorStyle
			} else {
				marker = selectedTagStyle.Render("● ")
				style = highlightStyle
			}
		}
		b.WriteString("  " + marker + style.Render(name))
		if i == sp.managerIdx {
			b.WriteString("  " + hintStyle.Render(describeManager(name)))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	switch sp.state {
	case stateManager:
		b.WriteString("  " + hintStyle.Render("press enter to choose this manager") + "\n")
	case stateOpLoadingAccounts:
		secs := int(time.Since(sp.loadingSince).Seconds())
		b.WriteString("  " + hintStyle.Render(fmt.Sprintf("loading 1Password accounts… %ds", secs)) + "\n")
	case stateOpPickAccount:
		b.WriteString("  " + sectionStyle.Render("Pick a 1Password account") + "\n")
		for i, a := range m.configOpAccounts {
			marker := "  "
			style := dimStyle
			if i == sp.accountCursor {
				marker = cursorStyle.Render("▶ ")
				style = cursorStyle
			}
			b.WriteString("    " + marker + style.Render(fmt.Sprintf("%s  (%s)", a.Email, a.URL)) + "\n")
		}
	case stateOpLoadingItems:
		secs := int(time.Since(sp.loadingSince).Seconds())
		b.WriteString("  " + hintStyle.Render(fmt.Sprintf(
			"loading 1Password items for %s… %ds  (may prompt for biometric auth)",
			abbrevAccount(m, sp.chosenAccount), secs)) + "\n")
	case stateOpPickItem:
		items := m.configOpItemCache[sp.chosenAccount]
		b.WriteString("  " + sectionStyle.Render(fmt.Sprintf("Pick the item for %s", sp.secretLabel)) + "\n")
		b.WriteString("  " + sp.itemInput.View() + "\n")
		q := strings.TrimSpace(sp.itemInput.Value())
		switch {
		case q == "":
			b.WriteString("  " + hintStyle.Render(fmt.Sprintf("showing first %d of %d", len(sp.itemMatches), len(items))) + "\n")
		case len(sp.itemMatches) == 0:
			b.WriteString("  " + hintStyle.Render(fmt.Sprintf("no match for %q", q)) + "\n")
		default:
			b.WriteString("  " + hintStyle.Render(fmt.Sprintf("%d match(es)", len(sp.itemMatches))) + "\n")
		}
		b.WriteString("\n")
		const (
			titleW = 36
			vaultW = 16
			typeW  = 18
		)
		b.WriteString("    ")
		for _, c := range []struct {
			t string
			w int
		}{{"Item", titleW}, {"Vault", vaultW}, {"Type", typeW}} {
			b.WriteString(sectionStyle.Render(padRight(c.t, c.w)) + "  ")
		}
		b.WriteString("\n    ")
		for _, w := range []int{titleW, vaultW, typeW} {
			b.WriteString(hintStyle.Render(strings.Repeat("─", w)) + "  ")
		}
		b.WriteString("\n")
		for vi, idx := range sp.itemMatches {
			it := items[idx]
			marker := " "
			style := dimStyle
			if vi == sp.itemCursor {
				marker = cursorStyle.Render("▶")
				style = cursorStyle
			}
			b.WriteString("   " + marker + " ")
			b.WriteString(style.Render(padRight(truncate(it.Title, titleW), titleW)) + "  ")
			b.WriteString(style.Render(padRight(truncate(it.Vault.Name, vaultW), vaultW)) + "  ")
			b.WriteString(style.Render(padRight(friendlyCategory(it.Category), typeW)))
			b.WriteString("\n")
		}
	case stateOpLoadingItemDetail:
		secs := int(time.Since(sp.loadingSince).Seconds())
		b.WriteString("  " + hintStyle.Render(fmt.Sprintf("loading item fields… %ds", secs)) + "\n")
	case stateOpPickField:
		title := ""
		if sp.itemDetail != nil {
			title = sp.itemDetail.Title
		}
		b.WriteString("  " + sectionStyle.Render(fmt.Sprintf("Pick the field from %q", title)) + "\n")
		for i, opt := range sp.fieldOptions {
			marker := "  "
			style := dimStyle
			if i == sp.fieldCursor {
				marker = cursorStyle.Render("▶ ")
				style = cursorStyle
			}
			b.WriteString("    " + marker + style.Render(opt.label))
			b.WriteString("  " + hintStyle.Render("("+opt.kind+")"))
			b.WriteString("\n")
		}
	case stateTypedRef:
		b.WriteString("  " + sectionStyle.Render(sp.secretLabel+" reference") + "\n")
		b.WriteString("    " + sp.refInput.View() + "\n")
		b.WriteString("    " + hintStyle.Render(refHintFor(sp.manager())) + "\n")
		switch {
		case sp.validating:
			b.WriteString("\n  " + hintStyle.Render("validating…") + "\n")
		case sp.lastErr != nil:
			b.WriteString("\n  " + errStyle.Render("✗ "+sp.lastErr.Error()) + "\n")
		case sp.ref() != "":
			b.WriteString("\n  " + hintStyle.Render("press enter to validate the reference") + "\n")
		}
	case stateValidated:
		b.WriteString("  " + selectedTagStyle.Render("✓ "+sp.secretLabel+" — "+sp.chosenRef) + "\n")
		if sp.chosenMgr == "1password" && sp.chosenAccount != "" {
			b.WriteString("    " + hintStyle.Render("account: "+abbrevAccount(m, sp.chosenAccount)) + "\n")
		}
		if sp.shouldShowDarwinHint(m) {
			b.WriteString("\n")
			b.WriteString("  " + warnStyle.Render("macOS tip — silence the cross-app prompts") + "\n")
			b.WriteString("  " + hintStyle.Render(
				"Each `op` call fires a \"iTerm would like to access data from other apps\" prompt") + "\n")
			b.WriteString("  " + hintStyle.Render(
				"until iTerm is enabled in System Settings → Privacy & Security → App Management.") + "\n")
			b.WriteString("\n")
			b.WriteString("  " + sectionStyle.Render("Open System Settings → App Management now?") + "  ")
			b.WriteString(highlightStyle.Render("[y]") + hintStyle.Render(" yes  ") +
				highlightStyle.Render("[n]") + hintStyle.Render(" skip") + "\n")
		}
		b.WriteString("\n  " + hintStyle.Render("press → to continue (shift+tab to re-pick)") + "\n")
	}
	// Sticky error (from earlier load failures).
	if sp.lastErr != nil && sp.state != stateTypedRef && sp.state != stateValidated {
		b.WriteString("\n  " + errStyle.Render("✗ "+sp.lastErr.Error()) + "\n")
	}
	return b.String()
}

// abbrevAccount returns "<email> (<url>)" for the given UUID, or the
// UUID itself if we don't have it cached.
func abbrevAccount(m *Model, uuid string) string {
	for _, a := range m.configOpAccounts {
		if a.AccountUUID == uuid {
			return fmt.Sprintf("%s (%s)", a.Email, a.URL)
		}
	}
	return uuid
}

// ----- async loader commands -----

func loadOpAccountsCmd(pickerID int) tea.Cmd {
	return func() tea.Msg {
		accs, err := secrets.ListOnePasswordAccounts(context.Background())
		return opAccountsLoadedMsg{pickerID: pickerID, accounts: accs, err: err}
	}
}

func loadOpItemsCmd(pickerID int, account string) tea.Cmd {
	return func() tea.Msg {
		op := &secrets.OnePassword{Runner: secrets.DefaultRunner{}, Account: account}
		items, err := op.ListItems(context.Background())
		return opItemsLoadedMsg{pickerID: pickerID, account: account, items: items, err: err}
	}
}

func loadOpItemDetailCmd(pickerID int, account, itemID string) tea.Cmd {
	return func() tea.Msg {
		op := &secrets.OnePassword{Runner: secrets.DefaultRunner{}, Account: account}
		detail, err := op.GetItem(context.Background(), itemID)
		return opItemDetailLoadedMsg{pickerID: pickerID, itemID: itemID, detail: detail, err: err}
	}
}

// ----- presentation helpers -----

// sortedItems orders items by credentialPriority (API_CREDENTIAL >
// PASSWORD > LOGIN > other), then alphabetically by vault and title.
// Mirrors the pre-wizard init.go helper of the same name.
func sortedItems(items []secrets.OnePasswordItem) []secrets.OnePasswordItem {
	sorted := make([]secrets.OnePasswordItem, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool {
		ip := credentialPriority(sorted[i].Category)
		jp := credentialPriority(sorted[j].Category)
		if ip != jp {
			return ip > jp
		}
		if sorted[i].Vault.Name != sorted[j].Vault.Name {
			return sorted[i].Vault.Name < sorted[j].Vault.Name
		}
		return sorted[i].Title < sorted[j].Title
	})
	return sorted
}

func credentialPriority(category string) int {
	switch category {
	case "API_CREDENTIAL":
		return 3
	case "PASSWORD":
		return 2
	case "LOGIN":
		return 1
	default:
		return 0
	}
}

func friendlyCategory(c string) string {
	switch c {
	case "API_CREDENTIAL":
		return "API credential"
	case "PASSWORD":
		return "password"
	case "LOGIN":
		return "login"
	case "":
		return "item"
	default:
		return strings.ToLower(strings.ReplaceAll(c, "_", " "))
	}
}

func friendlyFieldType(t, purpose string) string {
	switch {
	case t == "CONCEALED" && purpose == "PASSWORD":
		return "password"
	case t == "CONCEALED":
		return "secret"
	case purpose == "USERNAME":
		return "username"
	case t == "STRING":
		return "text"
	case t == "OTP":
		return "OTP"
	default:
		return strings.ToLower(t)
	}
}

func describeManager(name string) string {
	mgr, err := secrets.New(name)
	if err != nil {
		return ""
	}
	return mgr.Describe()
}

func refHintFor(manager string) string {
	switch manager {
	case "1password":
		return "Format: op://Vault/Item/field"
	case "bitwarden":
		return "Format: item name or ID; thicket reads the `password` field via `bw get item …`."
	case "pass":
		return "Format: pass entry name (the relative path under your password-store)."
	case "env":
		return "Type the env-var name (UPPER_SNAKE_CASE). Thicket reads it at runtime."
	}
	return ""
}

// firstNonEmptyStr returns the first non-blank input. (Distinct from
// firstNonEmpty in page_ticket.go, which works on slice indices.)
func firstNonEmptyStr(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// shouldShowDarwinHint reports whether the post-validated hint about
// macOS App Management should be rendered + accept y/n input. Gated
// on:
//   - we're on macOS (the prompt only fires on darwin)
//   - the user actually walked the 1P picker this session (preseed
//     skip-to-validated doesn't count; that user has presumably
//     already lived through the prompt on a prior init)
//   - the user hasn't dismissed it already on a previous page in
//     this same wizard run
func (sp *secretPicker) shouldShowDarwinHint(m *Model) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	if !sp.walked1P {
		return false
	}
	if sp.chosenMgr != "1password" {
		return false
	}
	return !m.configOpHintDismissed
}

// openSystemSettingsAppManagement launches the macOS System Settings
// app directly on the App Management privacy pane via the
// x-apple.systempreferences: URL scheme. We .Start() (not .Run())
// so we don't block the wizard waiting for the user to interact
// with Settings.
func openSystemSettingsAppManagement() {
	cmd := exec.Command("open",
		"x-apple.systempreferences:com.apple.preference.security?Privacy_AppBundles")
	_ = cmd.Start()
}

// looksLikeEnvVarName accepts conventional uppercase-underscore env
// var names.
func looksLikeEnvVarName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if !((r >= 'A' && r <= 'Z') || r == '_') {
				return false
			}
			continue
		}
		if !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}
