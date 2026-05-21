# Launch Options (Color Palette + Initial Prompt) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Post-implementation addendum (2026-05-21):** The shipped implementation drops the `/color <name>\n<user-prompt>` prefix originally described below. Smoke testing showed `claude` treats the positional prompt arg as a single user message — the trailing user prose got swallowed into the `/color` slash command's argument (e.g. `/color purple\nsolve it` parsed as color = `"purple\nsolve it"`). The launcher now passes the trimmed user prompt as the only positional arg; the picked palette name continues to drive the iTerm2 tab tint on every launch via `term.PaletteHex`. See commit `5328799` for the change. The Goal/Architecture and Task 6 sections below preserve the original design as a historical record — they DO NOT reflect shipped behavior.

**Goal (historical):** Add an optional initial-prompt input to the Plan page and switch the LLM color suggestion from free-form hex (`#RRGGBB`) to a fixed Claude color palette displayed as a horizontal swatch picker. On the first launch only, prepend `/color <name>` to the initial-prompt argument passed to `claude`. *(Shipped: the `/color` prefix is omitted — see addendum.)*

**Architecture (historical):**
- New `internal/term/palette.go` defines a fixed palette (name → representative hex). Both the manifest and the LLM speak palette **names**; iTerm2 tab tinting still uses the hex via lookup.
- `internal/launcher.Launcher` gains an `InitialPrompt` field that becomes a positional argument to `claude` (after `--name <slug>`).
- ~~`cmd/thicket/start.go`'s wizard-finalize path is the only call site that passes a non-empty prompt; the wizard's Plan page produces it as `"/color <name>\n<user-typed prompt>"`.~~ *(Shipped: the wizard-finalize path passes just the trimmed user prompt; the color is no longer encoded into the prompt arg.)* Every reuse path (existing-workspace launch from `thicket start [id]` or from inside a workspace) passes empty.

**Tech Stack:** Go, Bubble Tea (charmbracelet/bubbletea + bubbles/textinput), Lipgloss for swatch rendering.

---

## File Structure

**New files:**
- `internal/term/palette.go` — palette definition, canonicalization, hex lookup.
- `internal/term/palette_test.go` — palette table tests.

**Modified files:**
- `internal/detector/nickname.go` — prompt text + `parseSuggestion` switch to palette names.
- `internal/detector/nickname_test.go` — update expected color values.
- `internal/workspace/workspace.go` — `Plan.Color` / `State.Color` now hold palette names; `writeState` sanitizes via palette.
- `internal/workspace/workspace_test.go` — update color-roundtrip tests.
- `internal/launcher/launcher.go` — add `InitialPrompt` field; append as positional arg.
- `internal/launcher/launcher_test.go` — new test for `InitialPrompt`.
- `internal/tui/wizard/start/plan.go` — color swatch picker focus zone + prompt input focus zone; thread prompt through `finalizeCmd`.
- `internal/tui/wizard/messages.go` — add `InitialPrompt` to `wizard.Result`.
- `cmd/thicket/start.go` — `launchClaudeIn` gains `prompt string`; only wizard-finalize path passes non-empty.

---

## Task 1: Define the Claude color palette

**Goal:** A single source of truth for palette names + hex values used by the LLM prompt, the TUI swatch picker, the manifest persistence layer, and the iTerm2 tinting.

**Files:**
- Create: `internal/term/palette.go`
- Create: `internal/term/palette_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/term/palette_test.go`:

```go
package term

import "testing"

func TestSanitizePaletteName_canonicalizes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"blue", "blue"},
		{"BLUE", "blue"},
		{"  Blue  ", "blue"},
		{"purple", "purple"},
		{"not-a-color", ""},
		{"", ""},
		{"#FF5733", ""}, // hex inputs no longer accepted as palette names
	}
	for _, tc := range cases {
		if got := SanitizePaletteName(tc.in); got != tc.want {
			t.Errorf("SanitizePaletteName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPaletteHex_returnsRepresentativeHex(t *testing.T) {
	if PaletteHex("blue") == "" {
		t.Error("PaletteHex(blue) is empty")
	}
	if got := PaletteHex("unknown"); got != "" {
		t.Errorf("PaletteHex(unknown) = %q, want empty", got)
	}
}

func TestPaletteNames_returnsStableOrder(t *testing.T) {
	names := PaletteNames()
	if len(names) < 4 {
		t.Fatalf("palette too small: %v", names)
	}
	again := PaletteNames()
	for i := range names {
		if names[i] != again[i] {
			t.Errorf("order mismatch at %d: %q vs %q", i, names[i], again[i])
		}
	}
	// Spot-check that the canonical name canonicalizes to itself.
	if SanitizePaletteName(names[0]) != names[0] {
		t.Errorf("names[0]=%q does not canonicalize to itself", names[0])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/term/ -run TestSanitizePaletteName -v
```
Expected: FAIL with "undefined: SanitizePaletteName".

- [ ] **Step 3: Write minimal implementation**

Create `internal/term/palette.go`:

```go
package term

import "strings"

// paletteEntry pairs a canonical palette name with its representative
// hex value. The name is what we pass to `/color <name>` and what we
// persist in the workspace manifest. The hex is what we feed to
// WriteTabColor so iTerm2 can tint the tab background. Keeping the
// table here means TUI rendering, LLM prompt construction, manifest
// validation, and iTerm2 tinting all agree on the set of values.
type paletteEntry struct {
	Name string
	Hex  string
}

// palette is the fixed set of colors we let the LLM suggest and the
// user pick from. Order is rendering order (the TUI swatch picker
// uses PaletteNames() directly).
var palette = []paletteEntry{
	{"red", "#C8232C"},
	{"orange", "#FF9900"},
	{"yellow", "#F9A825"},
	{"green", "#2E7D32"},
	{"cyan", "#00ACC1"},
	{"blue", "#1565C0"},
	{"purple", "#6A1B9A"},
	{"pink", "#E91E63"},
}

// SanitizePaletteName normalizes an input string to a canonical
// palette name. Trims whitespace, lowercases, and returns "" when
// the value isn't in the palette. Used at the persistence boundary
// and when parsing LLM output.
func SanitizePaletteName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, p := range palette {
		if p.Name == s {
			return p.Name
		}
	}
	return ""
}

// PaletteHex returns the representative hex for a canonical palette
// name (e.g. "blue" → "#1565C0"), or "" when the name isn't in the
// palette. Used by the iTerm2 tinting path.
func PaletteHex(name string) string {
	name = SanitizePaletteName(name)
	if name == "" {
		return ""
	}
	for _, p := range palette {
		if p.Name == name {
			return p.Hex
		}
	}
	return ""
}

// PaletteNames returns the palette in rendering order. Callers MUST
// NOT mutate the slice.
func PaletteNames() []string {
	out := make([]string, len(palette))
	for i, p := range palette {
		out[i] = p.Name
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/term/ -v
```
Expected: PASS (all palette tests + existing iterm2 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/term/palette.go internal/term/palette_test.go
git commit -m "feat(term): add Claude color palette table"
```

---

## Task 2: Switch LLM nickname suggester to palette names

**Goal:** Replace the "output `#RRGGBB`" instruction in the LLM prompt with "output one palette name from this list". `NicknameSuggestion.Color` continues to be a string, but its values are now palette names like `"blue"` instead of `"#FF5733"`.

**Files:**
- Modify: `internal/detector/nickname.go`
- Modify: `internal/detector/nickname_test.go`

- [ ] **Step 1: Update the parser test to assert palette-name output**

Open `internal/detector/nickname_test.go` and replace the existing `parseSuggestion` test cases with palette-name expectations. Find each `wantColor: "#FF5733"` and the surrounding case lines; replace `"#FF5733"` with `"blue"` (and adjust the input strings to contain `"blue"` instead of `"#FF5733"`). Replace the "color line wins regardless of order" case input similarly.

The single test in `nickname_test.go:14-79` should look like (replace the whole `cases` slice):

```go
cases := []struct {
    name         string
    raw          string
    wantNickname string
    wantColor    string
}{
    {
        name:         "nickname then color",
        raw:          "🐛 Wix S3 dedup\nblue",
        wantNickname: "🐛 Wix S3 dedup",
        wantColor:    "blue",
    },
    {
        name:         "color then nickname",
        raw:          "blue\n🐛 Wix S3 dedup",
        wantNickname: "🐛 Wix S3 dedup",
        wantColor:    "blue",
    },
    {
        name:         "color prefix",
        raw:          "🐛 Wix S3 dedup\ncolor: blue",
        wantNickname: "🐛 Wix S3 dedup",
        wantColor:    "blue",
    },
    {
        name:         "no color line",
        raw:          "🐛 Wix S3 dedup",
        wantNickname: "🐛 Wix S3 dedup",
        wantColor:    "",
    },
    {
        name:         "leading prose then content",
        raw:          "Here you go:\n🐛 Wix S3 dedup\nblue",
        wantNickname: "🐛 Wix S3 dedup",
        wantColor:    "blue",
    },
    {
        name:         "unknown color name drops to empty",
        raw:          "🐛 Wix S3 dedup\nchartreuse",
        wantNickname: "🐛 Wix S3 dedup",
        wantColor:    "",
    },
    {
        name:         "uppercase color",
        raw:          "🐛 Wix S3 dedup\nBLUE",
        wantNickname: "🐛 Wix S3 dedup",
        wantColor:    "blue",
    },
}
```

Then update `TestParseSuggestion_uppercaseHexNormalized` and similar tests that asserted `"#FF5733"` / hex-specific behavior — rename them and adjust to assert palette behavior. For the "empty color when unparseable" test (currently using `"#GGGGGG"` or similar invalid hex), keep the structure but use `"chartreuse"` as the invalid input.

For the `TestRenderExistingColorsClause` test (`nickname_test.go:137-160`), update its inputs from hex (`"#FF9900"`, `"#0078D4"`, `"#13AA52"`) to palette names (`"orange"`, `"blue"`, `"green"`).

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/detector/ -run TestParseSuggestion -v
go test ./internal/detector/ -run TestRenderExistingColorsClause -v
```
Expected: FAIL — parser still uses `term.SanitizeHexColor`.

- [ ] **Step 3: Update the prompt template + parser to palette names**

In `internal/detector/nickname.go`, replace the entire `nicknamePromptTemplate` constant (lines 48-92) with:

```go
const nicknamePromptTemplate = `Suggest a workspace nickname AND a tab color for this ticket. The developer is juggling many concurrent workspaces and needs to recognize THIS one INSTANTLY in a list of tabs.

NICKNAME RULES:
- Maximum 25 characters. Use ACRONYMS and shorthand to fit — e.g. "MR" for Munich Re, "WD" for Workday, "SP" for SharePoint, "GD" for GoogleDrive.
- MINE the title and body for distinctive nouns and use them in the nickname:
  * Customer / company / org names (Wix, Munich Re, Workday, Rivian, Sentra, Anthropic, etc.) — these are gold.
  * Hosting-service names (SharePoint, GoogleDrive, S3, FileShare, Snowflake, Databricks, Confluence, Jira, GitHub, Azure Blob, BigQuery, MongoDB, Postgres, etc.).
  * File-format or domain keywords when central (CAD, PDF, DICOM, parquet, etc.).
- One emoji prefix is encouraged when it tightly conveys the WORK TYPE:
  🐛 bug · ⚡ perf · 🔒 security · 📝 docs · 🎨 UI · 🧪 test · 🔧 refactor · 🚀 deploy · 📊 data · 🔍 investigate · ✨ feature
- BAD (too generic — never do this): "fix the bug", "add feature", "investigate issue", "update code", "ticket fix".
- GOOD: "🐛 Wix S3 dedup", "🔍 MR Snowflake enum", "⚡ WD GDrive scan", "🧪 SP file probe", "📝 Rivian CAD docs", "🔧 Sentra retry loop".

COLOR RULES — pick exactly one name from this fixed list:
  red, orange, yellow, green, cyan, blue, purple, pink

- PRIMARY inspiration — match a famous brand when one is mentioned in the ticket:
  * AWS / S3 → orange
  * Google Drive / GCP → blue or green
  * Microsoft / SharePoint / OneDrive / Azure → blue
  * Snowflake → cyan
  * Databricks → red
  * Atlassian / Jira / Confluence → blue
  * GitHub → purple
  * MongoDB → green
  * Slack → purple or pink
- FALLBACK inspiration — when no brand fits, pick from the WORK TYPE:
  * Bug / fix → red
  * Feature → green or blue
  * Refactor / chore → blue
  * Investigation / spike → purple
  * Performance → cyan
  * Security / auth → red or orange
  * Docs → green
- DIFFERENTIATION (CRITICAL): %s

Output format — EXACTLY two lines, in this order, no preamble or trailing prose:
<nickname>
<color-name>

TICKET TITLE:
%s

TICKET BODY:
%s`
```

In the same file, update `renderExistingColorsClause` (lines 98-108):

```go
func renderExistingColorsClause(existingColors []string) string {
	if len(existingColors) == 0 {
		return "no other thicket workspaces are currently colored — feel free to pick anything from the list."
	}
	if len(existingColors) > 8 {
		existingColors = existingColors[:8]
	}
	return fmt.Sprintf("other workspace tabs are already using these colors — pick something different so the user can tell them apart at a glance: %s.",
		strings.Join(existingColors, ", "))
}
```

In `parseSuggestion` (lines 119-160), replace the `term.SanitizeHexColor(line)` call (line 147) with `term.SanitizePaletteName(line)`:

```go
		// Try color first — if it canonicalizes to a palette name,
		// it's the color line regardless of where it appeared.
		if c := term.SanitizePaletteName(line); c != "" && s.Color == "" {
			s.Color = c
			continue
		}
```

Also update the `NicknameSuggestion.Color` doc comment (lines 23-26):

```go
	// Color is the canonical palette name (e.g. "blue") sanitized
	// via term.SanitizePaletteName. Empty when the model didn't
	// emit a value that matches the palette.
	Color string
```

And the final-line addition in `ClaudeCLINicknameSuggester.Suggest` (line 235) — change `"Return ONLY the two lines (nickname then #RRGGBB). No prose around them."` to `"Return ONLY the two lines (nickname then color-name). No prose around them."`.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/detector/ -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/detector/nickname.go internal/detector/nickname_test.go
git commit -m "feat(detector): switch color suggestion to palette names"
```

---

## Task 3: Switch workspace.State / Plan.Color to palette names

**Goal:** The persisted `Color` is now a palette name (`"blue"`), not a hex (`"#FF5733"`). Sanitization happens at `writeState`. The iTerm2 tinting code stays as-is for now — its hex input will be wired through `term.PaletteHex` in Task 6.

**Files:**
- Modify: `internal/workspace/workspace.go`
- Modify: `internal/workspace/workspace_test.go`

- [ ] **Step 1: Update the roundtrip test to expect palette names**

Open `internal/workspace/workspace_test.go` and locate `TestState_ColorRoundtrip` (line 561). Replace the `Color: "#FF5733"` literal and the matching `got.Color != "#FF5733"` assertion with `"blue"`:

```go
func TestState_ColorRoundtrip(t *testing.T) {
	dir := t.TempDir()
	st := State{
		TicketID:  "sc-1",
		Branch:    "main",
		Color:     "blue",
		CreatedAt: time.Now().Round(time.Second),
		Repos:     []StateRepo{{Name: "r", SourcePath: "/s", WorktreePath: "/w"}},
	}
	if err := writeStateAtomic(dir, st); err != nil {
		t.Fatal(err)
	}
	got, err := ReadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Color != "blue" {
		t.Errorf("color lost on roundtrip: got %q", got.Color)
	}
}
```

Also add a sanitization test (place immediately after `TestState_ColorRoundtrip`):

```go
func TestState_DropsUnknownColor(t *testing.T) {
	dir := t.TempDir()
	// Plans built before this migration may still carry hex values
	// (e.g. "#FF5733") or junk; writeState should drop anything not
	// in the palette so the manifest is always canonical.
	p := Plan{
		WorkspaceDir: dir,
		Branch:       "main",
		Color:        "#FF5733",
		Repos:        []PlanRepo{{Name: "r", SourcePath: "/s", WorktreePath: "/w"}},
		Memory:       memory.Input{TicketID: "sc-1", Branch: "main", WorkspaceDir: dir, CreatedAt: time.Now()},
	}
	if err := writeState(p); err != nil {
		t.Fatal(err)
	}
	got, err := ReadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Color != "" {
		t.Errorf("unknown color was persisted: %q", got.Color)
	}
}
```

(If `memory` isn't already imported in this test file, add the import `"github.com/uribrecher/thicket/internal/memory"`.)

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/workspace/ -run "TestState_(ColorRoundtrip|DropsUnknownColor)" -v
```
Expected: FAIL — `TestState_DropsUnknownColor` produces `"#FF5733"` (current `SanitizeHexColor` accepts hex); `TestState_ColorRoundtrip` may pass-as-junk (hex was the old encoding).

- [ ] **Step 3: Switch writeState to palette sanitization**

In `internal/workspace/workspace.go`, locate `writeState` (line 587) and replace `term.SanitizeHexColor(p.Color)` with `term.SanitizePaletteName(p.Color)`:

```go
		Color:     term.SanitizePaletteName(p.Color),
```

Also update the doc comments for `Plan.Color` (lines 44-49) and `State.Color` (lines 79-82) to reflect palette-name semantics:

```go
	// Color is the workspace's tab-color palette name (e.g. "blue").
	// Used by the launcher to look up a representative hex for the
	// iTerm2 tab tint and to prepend `/color <name>` to claude's
	// initial prompt on first launch. Optional — when empty the tab
	// is left uncolored. Sanitized at the persistence boundary via
	// term.SanitizePaletteName; values outside the palette are
	// dropped to "".
	Color  string
```

```go
	// Color is the tab-color palette name (e.g. "blue"). The launcher
	// resolves a representative hex via term.PaletteHex for iTerm2's
	// tab tint. `omitempty` — manifests without a color decode as "".
	Color     string      `json:"color,omitempty"`
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/workspace/ -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/workspace/workspace.go internal/workspace/workspace_test.go
git commit -m "feat(workspace): store tab color as palette name"
```

---

## Task 4: Extend launcher with InitialPrompt

**Goal:** `launcher.Launcher.InitialPrompt`, when non-empty, becomes the final positional argument to `claude`, after any `ExtraArgs`.

**Files:**
- Modify: `internal/launcher/launcher.go`
- Modify: `internal/launcher/launcher_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/launcher/launcher_test.go`:

```go
func TestLaunch_appendsInitialPrompt(t *testing.T) {
	tmp := t.TempDir()
	cwdBefore, _ := os.Getwd()
	defer func() { _ = os.Chdir(cwdBefore) }()

	var gotArgv []string
	l := &Launcher{
		BinaryName:    "claude",
		ExtraArgs:     []string{"--name", "ws-x"},
		InitialPrompt: "/color blue\nreview the plan",
		LookPath:      func(name string) (string, error) { return "/opt/claude/claude", nil },
		Exec: func(_ string, argv []string, _ []string) error {
			gotArgv = argv
			return nil
		},
	}
	if err := l.Launch(tmp); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if len(gotArgv) != 4 ||
		gotArgv[0] != "claude" ||
		gotArgv[1] != "--name" ||
		gotArgv[2] != "ws-x" ||
		gotArgv[3] != "/color blue\nreview the plan" {
		t.Errorf("argv = %v", gotArgv)
	}
}

func TestLaunch_emptyInitialPromptOmitted(t *testing.T) {
	tmp := t.TempDir()
	cwdBefore, _ := os.Getwd()
	defer func() { _ = os.Chdir(cwdBefore) }()

	var gotArgv []string
	l := &Launcher{
		BinaryName:    "claude",
		ExtraArgs:     []string{"--name", "ws-x"},
		InitialPrompt: "",
		LookPath:      func(name string) (string, error) { return "/opt/claude/claude", nil },
		Exec: func(_ string, argv []string, _ []string) error {
			gotArgv = argv
			return nil
		},
	}
	if err := l.Launch(tmp); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if len(gotArgv) != 3 {
		t.Errorf("argv = %v (want length 3, prompt should be omitted)", gotArgv)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/launcher/ -run TestLaunch_appendsInitialPrompt -v
```
Expected: FAIL — `InitialPrompt` field undefined.

- [ ] **Step 3: Add the field + append logic**

In `internal/launcher/launcher.go`, add the field to the `Launcher` struct (after `ExtraArgs`, around line 36):

```go
	// InitialPrompt, when non-empty, is appended as the final positional
	// argument to the claude binary. claude treats this as the first
	// user message of the session, so leading slash commands (e.g.
	// `/color blue`) fire on session start just as if typed.
	InitialPrompt string
```

In `Launch` (around line 65), update the argv construction:

```go
	argv := append([]string{l.BinaryName}, l.ExtraArgs...)
	if l.InitialPrompt != "" {
		argv = append(argv, l.InitialPrompt)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/launcher/ -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/launcher/launcher.go internal/launcher/launcher_test.go
git commit -m "feat(launcher): support InitialPrompt positional arg"
```

---

## Task 5: Add color swatch picker + prompt input to the Plan page

**Goal:** Replace the read-only color row on the Plan page with a horizontal swatch picker over `term.PaletteNames()`. Add a single-line optional prompt input below the color row. The LLM suggestion seeds the initially-selected swatch.

Focus order top→bottom: clone rows → nickname → **color swatch** → **prompt** → Create button. Inside color focus, left/right (`h`/`l`) cycles palette entries.

**Files:**
- Modify: `internal/tui/wizard/start/plan.go`
- Modify: `internal/tui/wizard/start/start_test.go` (extend if it already covers Plan page behavior; otherwise leave for now)

- [ ] **Step 1: Add the new focus zones to the struct**

In `internal/tui/wizard/start/plan.go`, modify the `planPage` struct (lines 23-66). Replace the existing `color string` field's commentary and add prompt + color-index state:

```go
	// color is the canonical palette name selected via the swatch
	// picker. Seeded from the suggester's `cached.Color`; user can
	// cycle left/right while the color zone has focus. Persisted via
	// plan.Color and used by launchClaudeIn to look up a representative
	// hex for iTerm2 tab tinting AND to prepend `/color <name>` to the
	// initial prompt on first launch.
	color string

	// promptInput is the optional, one-shot initial prompt the
	// launcher will pass as claude's first user message. Empty by
	// default. Single-line by design — the launcher always prepends
	// "/color <name>\n", so the user input ends up as line 2.
	promptInput textinput.Model

	// focusColor / focusPrompt mirror focusNickname for the new
	// focus zones. Exactly one of focusNickname/focusColor/
	// focusPrompt/focusBtn is true at a time; when all are false the
	// cursor is on a clone row at index `cursor`.
	focusColor  bool
	focusPrompt bool
```

In `newPlanPage` (lines 68-78), construct the prompt input alongside the nickname input:

```go
func newPlanPage() *planPage {
	ni := textinput.New()
	ni.CharLimit = workspace.NicknameMaxChars
	ni.Width = 30
	ni.Prompt = "› "
	ni.Placeholder = "short label (≤25 chars, acronyms + emoji ok)"
	pi := textinput.New()
	pi.CharLimit = 200
	pi.Width = 50
	pi.Prompt = "› "
	pi.Placeholder = "optional first-message prompt (leave empty to skip)"
	return &planPage{
		cloneInclude:  make(map[string]bool),
		nicknameInput: ni,
		promptInput:   pi,
	}
}
```

- [ ] **Step 2: Reset prompt + color on ticket change**

Still in `plan.go`, extend `InitCmd` (lines 115-133) so the new fields reset when the ticket changes. After the `p.nicknameInput.SetValue("")` and `p.nicknameDirty = false` and `p.color = ""` lines (the existing reset block), add:

```go
		p.promptInput.SetValue("")
		p.focusColor = false
		p.focusPrompt = false
```

- [ ] **Step 3: Update focus navigation**

Replace `moveFocusUp` (lines 379-402) and `moveFocusDown` (lines 406-427) so the new zones sit between nickname and Create:

```go
// moveFocusUp shifts focus toward the top of the page across the
// focus zones: clone rows → nickname → color → prompt → Create.
func (p *planPage) moveFocusUp() tea.Cmd {
	switch {
	case p.focusBtn:
		// Create → prompt.
		p.focusBtn = false
		p.focusPrompt = true
		return p.promptInput.Focus()
	case p.focusPrompt:
		// Prompt → color.
		p.focusPrompt = false
		p.focusColor = true
		p.promptInput.Blur()
		return nil
	case p.focusColor:
		// Color → nickname.
		p.focusColor = false
		p.focusNickname = true
		return p.nicknameInput.Focus()
	case p.focusNickname:
		// Nickname → last clone row (if any).
		if len(p.toClone) > 0 {
			p.focusNickname = false
			p.cursor = len(p.toClone) - 1
			p.nicknameInput.Blur()
		}
		return nil
	default:
		// On a clone row.
		if p.cursor > 0 {
			p.cursor--
		}
		return nil
	}
}

// moveFocusDown is the mirror.
func (p *planPage) moveFocusDown() tea.Cmd {
	switch {
	case p.focusBtn:
		return nil
	case p.focusPrompt:
		// Prompt → Create.
		p.focusPrompt = false
		p.focusBtn = true
		p.promptInput.Blur()
		return nil
	case p.focusColor:
		// Color → prompt.
		p.focusColor = false
		p.focusPrompt = true
		return p.promptInput.Focus()
	case p.focusNickname:
		// Nickname → color.
		p.focusNickname = false
		p.focusColor = true
		p.nicknameInput.Blur()
		return nil
	default:
		// On a clone row.
		if p.cursor < len(p.toClone)-1 {
			p.cursor++
			return nil
		}
		// Last clone row → nickname.
		p.focusNickname = true
		return p.nicknameInput.Focus()
	}
}
```

Also update the initial-focus block inside the `wizard.PlanBuiltMsg` handler (lines 259-271). The fallback when there are no toClone rows currently focuses nickname; keep that behavior but ensure the new zones default to false:

```go
		switch {
		case len(p.toClone) > 0:
			p.cursor = 0
			p.focusBtn = false
			p.focusNickname = false
			p.focusColor = false
			p.focusPrompt = false
			p.nicknameInput.Blur()
			p.promptInput.Blur()
		default:
			p.focusBtn = false
			p.focusColor = false
			p.focusPrompt = false
			p.promptInput.Blur()
			p.focusNickname = true
			focusCmd = p.nicknameInput.Focus()
		}
```

- [ ] **Step 4: Key handling for color cycling + prompt typing**

Inside `Update`'s `tea.KeyMsg` branch (lines 321-372), add handlers for left/right when color is focused, and forward unhandled keys to the prompt input when prompt is focused. Insert these handlers between the existing `enter` / space handling and the fallthrough nickname forwarder.

Replace the existing `key` switch (lines 326-360) with:

```go
		key := v.String()
		switch key {
		case "up", "k":
			return p, p.moveFocusUp()
		case "down", "j":
			// Don't intercept "j" inside a focused text input — fall
			// through to the input's own key handler so the user can
			// actually type the letter j.
			if p.focusNickname || p.focusPrompt {
				break
			}
			return p, p.moveFocusDown()
		case "left", "h":
			if p.focusColor {
				p.cyclePaletteLeft()
				return p, nil
			}
			if p.focusNickname || p.focusPrompt {
				break
			}
			return p, nil
		case "right", "l":
			if p.focusColor {
				p.cyclePaletteRight()
				return p, nil
			}
			if p.focusNickname || p.focusPrompt {
				break
			}
			return p, nil
		case "enter":
			switch {
			case p.focusNickname:
				p.focusNickname = false
				p.focusColor = true
				p.nicknameInput.Blur()
				return p, nil
			case p.focusColor:
				p.focusColor = false
				p.focusPrompt = true
				return p, p.promptInput.Focus()
			case p.focusPrompt:
				p.focusPrompt = false
				p.focusBtn = true
				p.promptInput.Blur()
				return p, nil
			case p.focusBtn || len(p.toClone) == 0:
				return p, p.startCloneCmd(m)
			default:
				name := p.toClone[p.cursor].Name
				p.cloneInclude[name] = !p.cloneInclude[name]
				return p, nil
			}
		case " ":
			if !p.focusBtn && !p.focusNickname && !p.focusColor && !p.focusPrompt && len(p.toClone) > 0 {
				name := p.toClone[p.cursor].Name
				p.cloneInclude[name] = !p.cloneInclude[name]
			}
			if !p.focusNickname && !p.focusPrompt {
				return p, nil
			}
		}
		// Forward unhandled keys to whichever text input is focused.
		if p.focusNickname {
			prev := p.nicknameInput.Value()
			var cmd tea.Cmd
			p.nicknameInput, cmd = p.nicknameInput.Update(v)
			if p.nicknameInput.Value() != prev {
				p.nicknameDirty = true
			}
			return p, cmd
		}
		if p.focusPrompt {
			var cmd tea.Cmd
			p.promptInput, cmd = p.promptInput.Update(v)
			return p, cmd
		}
```

Add the cycle helpers near `moveFocusDown` in the same file:

```go
// cyclePaletteLeft / cyclePaletteRight move the color selection by
// one palette entry, wrapping at both ends. The seed value (from
// the LLM suggester) is treated as the starting position.
func (p *planPage) cyclePaletteLeft() {
	names := term.PaletteNames()
	if len(names) == 0 {
		return
	}
	idx := paletteIndex(names, p.color)
	idx = (idx - 1 + len(names)) % len(names)
	p.color = names[idx]
}

func (p *planPage) cyclePaletteRight() {
	names := term.PaletteNames()
	if len(names) == 0 {
		return
	}
	idx := paletteIndex(names, p.color)
	idx = (idx + 1) % len(names)
	p.color = names[idx]
}

// paletteIndex returns the index of `cur` in names, or 0 when not
// found (so an empty/unknown seed lands on the first palette entry).
func paletteIndex(names []string, cur string) int {
	for i, n := range names {
		if n == cur {
			return i
		}
	}
	return 0
}
```

Add the import for `term` if not already present:

```go
"github.com/uribrecher/thicket/internal/term"
```

- [ ] **Step 5: Render the swatch picker + prompt input**

In `View` (around lines 661-687, the "tab color:" row plus its surroundings), replace the existing `if p.color != ""` block with a horizontal swatch row that always renders (even when `p.color == ""`, so the user always sees something to cycle through), and add a prompt row beneath it. Locate the existing block:

```go
		if p.color != "" {
			swatch := lipgloss.NewStyle().
				Background(lipgloss.Color(p.color)).
				Render("    ")
			b.WriteString(fmt.Sprintf("    tab color:     %s  %s\n",
				swatch, wizard.HintStyle.Render(p.color+"  (iTerm2 tab tint)")))
		}
```

Replace with:

```go
		// Color row: horizontal swatch picker over the fixed palette.
		// The selected entry is bracketed and labelled; cursor marker
		// appears when this zone is focused.
		clrMarker := "  "
		if p.focusColor {
			clrMarker = wizard.CursorStyle.Render("▶ ")
		}
		b.WriteString(fmt.Sprintf("  %stab color:     %s\n",
			clrMarker, renderPaletteSwatches(p.color)))

		// Prompt row: optional one-shot initial prompt. Cursor marker
		// when focused.
		prMarker := "  "
		if p.focusPrompt {
			prMarker = wizard.CursorStyle.Render("▶ ")
		}
		b.WriteString(fmt.Sprintf("  %sinitial prompt: %s\n",
			prMarker, p.promptInput.View()))
```

Add the helper near the bottom of the file:

```go
// renderPaletteSwatches draws the palette as a row of colored blocks
// with the currently-selected entry bracketed and named. Used by the
// Plan page's color zone — always renders even when selected is "",
// so the user has something to start from.
func renderPaletteSwatches(selected string) string {
	names := term.PaletteNames()
	var b strings.Builder
	for _, n := range names {
		sw := lipgloss.NewStyle().
			Background(lipgloss.Color(term.PaletteHex(n))).
			Render("  ")
		if n == selected {
			b.WriteString(wizard.CursorStyle.Render("[") + sw + wizard.CursorStyle.Render("]"))
		} else {
			b.WriteString(" " + sw + " ")
		}
	}
	if selected != "" {
		b.WriteString("  " + wizard.HintStyle.Render(selected))
	}
	return b.String()
}
```

- [ ] **Step 6: Update the Hints line**

Replace `Hints` (lines 85-96) so the picker zones produce sensible affordance text:

```go
func (p *planPage) Hints() string {
	if p.creating {
		return ""
	}
	switch {
	case p.focusNickname:
		return "type nickname (≤25) · ↑/↓ leaves · enter accepts"
	case p.focusColor:
		return "←/→ cycles palette · ↑/↓ leaves · enter accepts"
	case p.focusPrompt:
		return "type optional prompt · ↑/↓ leaves · enter accepts"
	case len(p.toClone) > 0:
		return "↑/↓ cursor · space toggles clone · enter creates"
	default:
		return "↑/↓ moves to nickname · enter creates"
	}
}
```

- [ ] **Step 7: Thread prompt + color through finalizeCmd**

The existing `finalizeCmd` already writes `Color: p.color` into the returned `workspace.Plan` (line 589). Add the prompt to the wizard result. Since `workspace.Plan` is the persistence-facing struct, we'll route `InitialPrompt` via a sibling field on `wizard.Result` instead.

Open `internal/tui/wizard/messages.go` and locate the `Result` struct (search for `type Result struct`). Add a field:

```go
	// InitialPrompt is the optional first-message prompt the Plan
	// page collected. The wizard's caller passes this to the
	// launcher; it is NOT persisted to the workspace manifest
	// because it's first-run-only.
	InitialPrompt string
```

Back in `plan.go`, in `finalizeCmd` (lines 526-595), wherever the function constructs the final `wizard.CreateDoneMsg{Result: wizard.Result{...}}` (around line 593), include the prompt:

```go
		return wizard.CreateDoneMsg{Result: wizard.Result{
			Plan:          plan,
			Skipped:       m.Result.Skipped,
			InitialPrompt: strings.TrimSpace(p.promptInput.Value()),
		}}
```

(`strings` is already imported.)

- [ ] **Step 8: Build + run TUI tests**

```bash
go build ./...
go test ./internal/tui/... -v
```
Expected: PASS. Existing TUI tests should continue to pass; the Plan page now has two extra focus zones but the legacy navigation paths (clone toggle + Create) are preserved.

- [ ] **Step 9: Commit**

```bash
git add internal/tui/wizard/start/plan.go internal/tui/wizard/messages.go
git commit -m "feat(tui): add color swatch picker and prompt input to Plan page"
```

---

## Task 6: Wire first-run prompt + palette-hex lookup into launchClaudeIn

**Goal:** The wizard-finalize path in `cmd/thicket/start.go` builds `"/color <name>\n<user-prompt>"` and passes it as the first-launch prompt. Every reuse path passes empty. iTerm2 tab tinting resolves the hex via `term.PaletteHex(name)` instead of using the manifest field directly.

**Files:**
- Modify: `cmd/thicket/start.go`

- [ ] **Step 1: Extend launchClaudeIn signature**

In `cmd/thicket/start.go`, update the function declaration (lines 412-413):

```go
// launchClaudeIn opens the configured Claude binary in workspaceDir,
// passing `--name <name>` so the session is distinguishable in
// Claude's prompt box, /resume picker, and the terminal title.
// Honors --no-launch by printing the cd line instead.
//
// On iTerm2, also emits OSC escapes for the tab title, badge, and a
// background tint resolved from the color palette name via
// term.PaletteHex.
//
// `prompt` is the optional first-message prompt. When non-empty it is
// appended as a positional arg to the claude invocation, becoming the
// first user message. Reuse callers (existing-workspace launches)
// pass "" so resuming a workspace doesn't re-fire the initial prompt.
func launchClaudeIn(out io.Writer, cfg *config.Config, name, color, prompt,
	workspaceDir string, noLaunch bool) error {
```

In the body, replace the iTerm2 block and the launcher setup. The current block (lines 425-435) writes the manifest's `color` field straight into `WriteTabColor`; switch to a palette-hex lookup, and wire `InitialPrompt`:

```go
	name = workspace.SanitizeNickname(name)

	if noLaunch {
		fmt.Fprintf(out, "cd %s\n", workspaceDir)
		return nil
	}
	if thicketterm.IsITerm2() && term.IsTerminal(int(os.Stdout.Fd())) {
		thicketterm.WriteTabTitle(os.Stdout, name)
		thicketterm.WriteBadge(os.Stdout, name)
		thicketterm.WriteTabColor(os.Stdout, thicketterm.PaletteHex(color))
	}
	l := launcher.New(cfg.ClaudeBinary)
	l.ExtraArgs = []string{"--name", name}
	l.InitialPrompt = prompt
	if err := l.Launch(workspaceDir); err != nil {
		if errors.Is(err, launcher.ErrMissingBinary) {
			launcher.PrintFallback(out, workspaceDir)
			return nil
		}
		return err
	}
	return nil
}
```

(Ensure the `thicketterm` import alias is the one already used in the file — search for the existing `WriteTabColor` call to confirm.)

- [ ] **Step 2: Pass empty prompt at every reuse site**

Update every existing call to `launchClaudeIn` to pass `""` for `prompt`, EXCEPT the wizard-finalize site at line 253. Find all six call sites:

- Line 66 (`runStart` reuse branch): add `""` as the new prompt arg.
- Line 129 (legacy reuse): add `""`.
- Line 234 (`res.ReuseDir != ""` branch): add `""`.
- Line 304 (legacy reuse via `findWorkspaceForTicket`): add `""`.
- Line 362 (legacy create — no UI for prompt): add `""`.

For each, the call shape changes from:

```go
return launchClaudeIn(out, cfg, name, color, workspaceDir, noLaunch)
```

to:

```go
return launchClaudeIn(out, cfg, name, color, "", workspaceDir, noLaunch)
```

- [ ] **Step 3: Build the first-run prompt at the wizard-finalize site**

At line 253 (the post-`workspace.Create` launch in `runStart`), wrap the prompt-building logic right before the `launchClaudeIn` call. Replace:

```go
	return launchClaudeIn(out, cfg,
		nicknameOrSlug(plan.Nickname, workspace.Slug(res.Ticket.SourceID, res.Ticket.Title)),
		plan.Color, plan.WorkspaceDir, flags.noLaunch)
```

with:

```go
	prompt := buildInitialPrompt(plan.Color, res.InitialPrompt)
	return launchClaudeIn(out, cfg,
		nicknameOrSlug(plan.Nickname, workspace.Slug(res.Ticket.SourceID, res.Ticket.Title)),
		plan.Color, prompt, plan.WorkspaceDir, flags.noLaunch)
```

Add the helper near `launchClaudeIn`:

```go
// buildInitialPrompt assembles the first-message prompt for a fresh
// workspace launch. The launcher always prepends a "/color <name>"
// line when a palette color is set, so the workspace tab is themed
// from the very first session regardless of the user prompt. Returns
// "" when both inputs are empty so the launcher omits the positional
// arg entirely.
func buildInitialPrompt(color, userPrompt string) string {
	userPrompt = strings.TrimSpace(userPrompt)
	color = workspaceterm.SanitizePaletteName(color)
	switch {
	case color == "" && userPrompt == "":
		return ""
	case color == "":
		return userPrompt
	case userPrompt == "":
		return "/color " + color
	default:
		return "/color " + color + "\n" + userPrompt
	}
}
```

The `workspaceterm` alias is the alias for `internal/term` already used in this file (search for the existing import — it appears as `thicketterm "github.com/uribrecher/thicket/internal/term"`). Use that alias rather than introducing a new one; the code becomes `thicketterm.SanitizePaletteName(color)`.

- [ ] **Step 4: Build + sanity-test the binary**

```bash
go build ./...
go test ./... -count=1
```
Expected: PASS. If any caller of `launchClaudeIn` was missed, the build will surface it as a "not enough arguments" compile error — add the missing `""` to it.

- [ ] **Step 5: Smoke-test on a real workspace**

Run the end-to-end flow against a real ticket (with `--no-launch` to keep the test cheap):

```bash
go run ./cmd/thicket start --no-launch <some-ticket-id>
```
Walk through the wizard, type a one-line prompt at the new field, pick a different swatch with ←/→, and finish. `thicket start --no-launch` exits with a `cd …` line; inspect `<workspace>/.thicket/state.json` to confirm `"color": "<palette-name>"`.

Then run without `--no-launch` on the same ticket id to confirm the resume path doesn't re-send the prompt (the `claude` session opens with the workspace nickname but no auto-typed first message). If `claude` isn't on PATH this step will print the `cd` fallback and is informative either way.

- [ ] **Step 6: Commit**

```bash
git add cmd/thicket/start.go
git commit -m "feat(start): launch claude with first-run /color + optional prompt"
```

---

## Self-Review

**Spec coverage:**
- ✅ Color picker becomes horizontal swatch over Claude palette → Task 1 (palette) + Task 5 (TUI).
- ✅ LLM suggests palette name instead of RGB → Task 2.
- ✅ Optional single-line prompt input on Plan page, empty by default → Task 5.
- ✅ Prompt sent only on first run, never on subsequent → Task 6 (all reuse sites pass `""`).
- ✅ Internally prepended with `/color <name>\n` → Task 6 `buildInitialPrompt`.
- ✅ Auto mode kept as global default + CLI flag override (NOT in this plan per user direction; will be a separate plan).

**Type consistency:**
- `Color string` (palette name) is consistent across `detector.NicknameSuggestion`, `workspace.Plan`, `workspace.State`, and the TUI's `planPage.color`.
- `InitialPrompt string` is consistent: `launcher.Launcher.InitialPrompt`, `wizard.Result.InitialPrompt`, and the `prompt` parameter of `launchClaudeIn`.
- Palette helpers `SanitizePaletteName`, `PaletteHex`, `PaletteNames` are named consistently across all call sites.

**Placeholder scan:** No TBDs, no "add appropriate handling", no "similar to Task N" without code.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-20-launch-options.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
