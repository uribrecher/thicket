package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/uribrecher/thicket/internal/config"
	"github.com/uribrecher/thicket/internal/secrets"
	"github.com/uribrecher/thicket/internal/tui"
)

func runInit(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	cfgPath, err := config.Path()
	if err != nil {
		return err
	}

	cfg, err := config.Load(cfgPath)
	if errors.Is(err, config.ErrNoConfig) || cfg == nil {
		d := config.Default()
		cfg = &d
	} else if err != nil {
		fmt.Fprintf(errOut, "warning: existing config at %s is invalid (%v); starting fresh.\n",
			cfgPath, err)
		d := config.Default()
		cfg = &d
	}

	// Step 1: paths, orgs (no secrets here — trust gained gradually).
	if err := runBaseConfigForm(cfg); err != nil {
		return err
	}

	// Step 2: pick the Claude backend (CLI vs API) so we know whether
	// to ask for an Anthropic API key at all.
	if err := chooseClaudeBackend(cfg); err != nil {
		return err
	}

	// Step 3: password manager — choose only; account selection happens
	// per-secret in step 4 because users may keep secrets in different
	// 1Password accounts.
	mgr, err := chooseManager(cfg)
	if err != nil {
		return err
	}

	// Step 4: per-secret references. Skips the Anthropic key when
	// claude_backend is "cli" — the local `claude` CLI handles auth.
	if err := collectSecretRefs(ctx, cfg, mgr); err != nil {
		return err
	}

	// Expand ~ in path fields so MkdirAll doesn't create a literal
	// ./~/tasks folder when the user accepts the default. The saved
	// config then carries absolute paths, which round-trip cleanly
	// through Load.
	if err := cfg.ExpandPaths(); err != nil {
		return err
	}

	// Persist.
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.Save(cfgPath); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.WorkspaceRoot, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not create workspace_root %s: %v\n",
			cfg.WorkspaceRoot, err)
	}

	fmt.Fprintf(out, "\nconfig written to %s\n", cfgPath)
	verifyExternalTools(out, cfg)
	return nil
}

// ----- Step 1 -----

func runBaseConfigForm(cfg *config.Config) error {
	available := availableGitHubOrgs()

	// Welcome note runs in its own form so the orgs widget can switch
	// shape (multiselect vs typed input) based on what gh tells us.
	if err := huh.NewForm(huh.NewGroup(
		huh.NewNote().
			Title("Welcome to thicket").
			Description("Walk through the prerequisites once. You can re-run `thicket init` any time to change values.\n\nThicket never asks you to paste a raw API token — point it at your password manager and we fetch on demand."),
	)).Run(); err != nil {
		return err
	}

	if err := collectGitHubOrgs(cfg, available); err != nil {
		return err
	}

	// Paths come last so we don't make the user re-pick everything if
	// gh enumeration was slow.
	if err := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Where do your repo clones live? (repos_root)").
			Value(&cfg.ReposRoot),
		huh.NewInput().
			Title("Where should new workspaces be created? (workspace_root)").
			Value(&cfg.WorkspaceRoot),
	)).Run(); err != nil {
		return err
	}

	fillDefaults(cfg)
	warnAboutEmptyOrgs(cfg.GithubOrgs)
	return nil
}

// availableGitHubOrgs queries the gh user's org memberships. Returns
// nil on any error so the caller falls back to free-text input.
func availableGitHubOrgs() []string {
	out, err := exec.Command("gh", "api", "user/orgs", "--jq", ".[].login").Output()
	if err != nil {
		return nil
	}
	var orgs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			orgs = append(orgs, t)
		}
	}
	return orgs
}

// collectGitHubOrgs shows a multiselect over the user's actual gh
// memberships when available. Falls back to free-text input if gh
// returned nothing useful (not auth'd, offline, no org memberships).
func collectGitHubOrgs(cfg *config.Config, available []string) error {
	if len(available) == 0 {
		var orgs string
		err := huh.NewForm(huh.NewGroup(
			huh.NewInput().
				Title("GitHub orgs to scan for repos").
				Description("Comma-separated. (Could not list your gh memberships — type the names.)").
				Value(&orgs).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("at least one GitHub org is required")
					}
					return nil
				}),
		)).Run()
		if err != nil {
			return err
		}
		cfg.GithubOrgs = splitCSV(orgs)
		return nil
	}

	// Pre-select any previously-saved orgs that are still available.
	already := make(map[string]bool, len(cfg.GithubOrgs))
	for _, o := range cfg.GithubOrgs {
		already[o] = true
	}
	options := make([]huh.Option[string], 0, len(available))
	preselected := []string{}
	for _, o := range available {
		options = append(options, huh.NewOption(o, o))
		if already[o] {
			preselected = append(preselected, o)
		}
	}

	chosen := preselected
	if err := huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[string]().
			Title("GitHub orgs to scan for repos").
			Description("space toggles  ·  type to filter  ·  enter confirms").
			Options(options...).
			Value(&chosen).
			Filterable(true).
			Validate(func(s []string) error {
				if len(s) == 0 {
					return errors.New("pick at least one org")
				}
				return nil
			}),
	)).Run(); err != nil {
		return err
	}
	cfg.GithubOrgs = chosen
	return nil
}

// warnAboutEmptyOrgs probes each configured org with `gh repo list`. If
// gh succeeds but returns zero repos for some, names those (and lists
// the orgs the gh user actually belongs to). If gh itself errors, we
// say so honestly instead of pretending every org is empty — a missing
// `gh auth login` is a different problem than a typo'd org name.
func warnAboutEmptyOrgs(orgs []string) {
	var empties []string
	for _, org := range orgs {
		out, err := exec.Command("gh", "repo", "list", org, "--limit", "1", "--json", "name").Output()
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nwarning: could not query org %q via gh: %v\n", org, err)
			fmt.Fprintln(os.Stderr, "  (run `gh auth status` to check; thicket won't be able to list repos until this is fixed)")
			return
		}
		trimmed := strings.TrimSpace(string(out))
		if trimmed == "" || trimmed == "[]" {
			empties = append(empties, org)
		}
	}
	if len(empties) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\nwarning: no visible repos in %v\n", empties)
	if memberships, err := exec.Command("gh", "api", "user/orgs", "--jq", ".[].login").Output(); err == nil {
		got := strings.TrimSpace(string(memberships))
		if got != "" {
			fmt.Fprintf(os.Stderr, "  orgs your gh user belongs to: %s\n",
				strings.Join(strings.Split(got, "\n"), ", "))
		}
	}
	fmt.Fprintln(os.Stderr, "  edit ~/.config/thicket/config.toml or re-run `thicket init` to fix.")
}

func fillDefaults(cfg *config.Config) {
	d := config.Default()
	if cfg.ClaudeModel == "" {
		cfg.ClaudeModel = d.ClaudeModel
	}
	if cfg.ClaudeBinary == "" {
		cfg.ClaudeBinary = d.ClaudeBinary
	}
	if cfg.DefaultBranch == "" {
		cfg.DefaultBranch = d.DefaultBranch
	}
	if cfg.TicketSource == "" {
		cfg.TicketSource = d.TicketSource
	}
}

// ----- Step 2: Claude backend -----

func chooseClaudeBackend(cfg *config.Config) error {
	// Default to "cli" when the `claude` binary is present; otherwise
	// API is the only working option.
	def := cfg.ClaudeBackend
	if def == "" {
		def = "cli"
		if _, err := exec.LookPath("claude"); err != nil {
			def = "api"
		}
	}
	choice := def
	err := huh.NewForm(huh.NewGroup(
		huh.NewNote().
			Title("How should thicket talk to Claude?").
			Description("Repo detection uses Claude. \"cli\" shells out to the local `claude` binary (reuses Claude Code / Enterprise auth — no API key). \"api\" calls the Anthropic API directly and needs an API key in your password manager."),
		huh.NewSelect[string]().
			Title("Claude backend").
			Options(
				huh.NewOption("cli — local `claude` binary (no API key)", "cli"),
				huh.NewOption("api — Anthropic API (needs anthropic_key_ref)", "api"),
			).
			Value(&choice),
	)).Run()
	if err != nil {
		return err
	}
	cfg.ClaudeBackend = choice
	return nil
}

// ----- Step 3: password manager -----

func chooseManager(cfg *config.Config) (secrets.Manager, error) {
	options := make([]huh.Option[string], 0, len(secrets.Supported))
	for _, id := range secrets.Supported {
		m, _ := secrets.New(id)
		options = append(options, huh.NewOption(
			fmt.Sprintf("%s — %s", id, m.Describe()),
			id,
		))
	}
	choice := cfg.Passwords.Manager
	if choice == "" {
		choice = "1password"
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().
				Title("Pick a password manager").
				Description("Thicket fetches API tokens from your password manager on demand. For 1Password, you'll pick the account per-secret next so different secrets can live in different accounts."),
			huh.NewSelect[string]().
				Title("Password manager").
				Options(options...).
				Value(&choice).
				Height(8),
		),
	)
	if err := form.Run(); err != nil {
		return nil, err
	}
	cfg.Passwords.Manager = choice

	// Construct a baseline manager (no account scoping yet). For
	// 1Password, the actual op calls happen later — each scoped to a
	// per-secret account.
	mgr, err := secrets.New(choice)
	if err != nil {
		return nil, err
	}
	return mgr, nil
}

// ----- Step 4 -----

type secretSlot struct {
	label   string
	refPtr  *string
	acctPtr *string // nil for non-1password managers
}

func collectSecretRefs(ctx context.Context, cfg *config.Config, mgr secrets.Manager) error {
	// All candidate slots, paired with the env var that would
	// short-circuit them at runtime.
	type candidate struct {
		slot   secretSlot
		envVar string
	}
	candidates := []candidate{
		{secretSlot{"Shortcut API token", &cfg.Passwords.ShortcutTokenRef, &cfg.Passwords.ShortcutTokenAccount}, "SHORTCUT_API_TOKEN"},
	}
	// Only ask for the Anthropic key when the API backend is configured.
	// Under the CLI backend the local `claude` binary handles auth.
	if cfg.ClaudeBackend == "api" {
		candidates = append(candidates, candidate{
			secretSlot{"Anthropic API key", &cfg.Passwords.AnthropicKeyRef, &cfg.Passwords.AnthropicKeyAccount},
			"ANTHROPIC_API_KEY",
		})
	}

	// Filter: skip any slot whose env var is currently set. The env var
	// also wins at runtime via fetchSecret, so we don't need a stored
	// reference for it.
	var slots []secretSlot
	for _, c := range candidates {
		if v := os.Getenv(c.envVar); v != "" {
			fmt.Printf("\n  ℹ $%s is set — skipping %s setup (env var wins at runtime).\n",
				c.envVar, c.slot.label)
			continue
		}
		slots = append(slots, c.slot)
	}
	if len(slots) == 0 {
		return nil
	}

	// 1Password gets the nice account-per-slot + item/field picker.
	// Other managers stick with the typed-string path.
	if mgr.Name() == "1password" {
		return collectSecretRefs1Password(ctx, slots)
	}
	return collectSecretRefsTyped(ctx, mgr, slots)
}

// collectSecretRefsTyped is the typed-string fallback used by every
// manager except 1Password.
func collectSecretRefsTyped(ctx context.Context, mgr secrets.Manager, slots []secretSlot) error {
	fmt.Println()
	fmt.Println("For each secret below, enter the item reference in your password manager.")
	fmt.Printf("  Reference format: %s\n\n", mgr.Describe())

	// env mode records env-var *names* — the variables themselves may not
	// be set at init time (they'll be set in CI / the shell config), so we
	// only validate the name shape and skip the live fetch.
	envMode := mgr.Name() == "env"

	for _, s := range slots {
		val := *s.refPtr
		err := huh.NewInput().
			Title(s.label + " reference").
			Value(&val).
			Validate(func(in string) error {
				in = strings.TrimSpace(in)
				if in == "" {
					return errors.New("reference is required")
				}
				if envMode {
					if !looksLikeEnvVarName(in) {
						return errors.New("env-var name must match [A-Z_][A-Z0-9_]* (uppercase, underscores)")
					}
					return nil
				}
				_, err := mgr.Get(ctx, in)
				if err != nil {
					return fmt.Errorf("test fetch failed: %w", err)
				}
				return nil
			}).
			Run()
		if err != nil {
			return err
		}
		*s.refPtr = strings.TrimSpace(val)
		if envMode {
			fmt.Printf("  ✓ %s — recorded (will read $%s at runtime)\n", s.label, *s.refPtr)
		} else {
			fmt.Printf("  ✓ %s — fetched OK\n", s.label)
		}
	}
	return nil
}

// collectSecretRefs1Password walks the user through each secret one at
// a time: pick the 1Password account, then the item, then the field.
// The previous slot's account is offered as the default for the next so
// users with multi-account setups can move quickly while still being
// able to switch.
func collectSecretRefs1Password(ctx context.Context, slots []secretSlot) error {
	accs, err := secrets.ListOnePasswordAccounts(ctx)
	if err != nil {
		return fmt.Errorf("list 1Password accounts: %w", err)
	}
	if len(accs) == 0 {
		return errors.New("no 1Password accounts signed in — run `op signin` first")
	}

	// Cache the per-account item lists so each account only pays its
	// first-call biometric prompt once across the whole init session.
	itemsByAccount := map[string][]secrets.OnePasswordItem{}
	lastAccount := ""

	for i, s := range slots {
		fmt.Printf("\n━━ [%d/%d] %s ━━\n", i+1, len(slots), s.label)

		def := firstNonEmpty(*s.acctPtr, lastAccount, accs[0].AccountUUID)
		account, err := pickAccountForSlot(s.label, accs, def)
		if err != nil {
			return err
		}
		lastAccount = account
		*s.acctPtr = account

		items, err := loadItemsForAccount(ctx, itemsByAccount, account, accountLabel(account, accs))
		if err != nil {
			return err
		}
		op := &secrets.OnePassword{Runner: secrets.DefaultRunner{}, Account: account}
		ref, err := pick1PasswordRef(ctx, op, items, s.label)
		if err != nil {
			return err
		}
		*s.refPtr = ref
		fmt.Printf("  ✓ %s — %s\n", s.label, ref)
	}
	fmt.Println()
	return nil
}

// pickAccountForSlot shows a per-slot account picker. With only one
// account known to op, we skip the picker entirely. `def` is the
// account to highlight initially when the picker does appear.
func pickAccountForSlot(label string, accs []secrets.OnePasswordAccount,
	def string) (string, error) {

	if len(accs) == 1 {
		fmt.Printf("  ✓ account: %s (%s)\n", accs[0].Email, accs[0].URL)
		return accs[0].AccountUUID, nil
	}

	options := make([]huh.Option[string], 0, len(accs))
	for _, a := range accs {
		options = append(options, huh.NewOption(
			fmt.Sprintf("%s  (%s)", a.Email, a.URL),
			a.AccountUUID,
		))
	}
	choice := def
	if err := huh.NewSelect[string]().
		Title(fmt.Sprintf("Which 1Password account holds the %s?", label)).
		Description("↑/↓ to move  ·  enter to select").
		Options(options...).
		Value(&choice).
		Run(); err != nil {
		return "", err
	}
	return choice, nil
}

// loadItemsForAccount fetches and caches the item list for one account.
func loadItemsForAccount(ctx context.Context, cache map[string][]secrets.OnePasswordItem,
	account, accountLabel string) ([]secrets.OnePasswordItem, error) {

	if items, ok := cache[account]; ok {
		return items, nil
	}
	fmt.Printf("Loading 1Password items for %s… (may prompt for biometric auth)\n", accountLabel)
	op := &secrets.OnePassword{Runner: secrets.DefaultRunner{}, Account: account}
	items, err := op.ListItems(ctx)
	if err != nil {
		return nil, fmt.Errorf("list 1Password items: %w", err)
	}
	if len(items) == 0 {
		return nil, errors.New("no 1Password items visible to this account")
	}
	fmt.Printf("  ✓ loaded %d items\n", len(items))
	cache[account] = items
	return items, nil
}

// sortedItems orders items by credentialPriority (API_CREDENTIAL >
// PASSWORD > LOGIN > other), then alphabetically by vault and title,
// so the likeliest match for an API-token slot surfaces first.
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

// accountLabel returns the friendly "<email> (<url>)" form for a UUID.
func accountLabel(uuid string, accs []secrets.OnePasswordAccount) string {
	for _, a := range accs {
		if a.AccountUUID == uuid {
			return fmt.Sprintf("%s (%s)", a.Email, a.URL)
		}
	}
	return uuid
}

// pick1PasswordRef shows the item picker (tableized via tui.PickOne),
// then the field picker, then returns the canonical op:// reference.
func pick1PasswordRef(ctx context.Context, op *secrets.OnePassword,
	items []secrets.OnePasswordItem, label string) (string, error) {

	sorted := sortedItems(items)
	columns := []tui.Column{
		{Title: "Item", Width: 38},
		{Title: "Vault", Width: 16},
		{Title: "Type", Width: 18},
	}
	rows := make([]tui.Row, len(sorted))
	for i, it := range sorted {
		rows[i] = tui.Row{
			Key:    it.ID,
			Cells:  []string{it.Title, it.Vault.Name, friendlyCategory(it.Category)},
			Filter: it.Title + " " + it.Vault.Name,
		}
	}
	itemID, err := tui.PickOne(
		fmt.Sprintf("Pick the 1Password item for %s", label),
		columns, rows)
	if err != nil {
		return "", err
	}

	detail, err := op.GetItem(ctx, itemID)
	if err != nil {
		return "", fmt.Errorf("fetch item details: %w", err)
	}
	if len(detail.Fields) == 0 {
		return "", fmt.Errorf("item %q has no fields", detail.Title)
	}

	// Field picker: default to the first CONCEALED field (most API
	// tokens live there). Hide fields without a usable Reference.
	fieldOpts := make([]huh.Option[string], 0, len(detail.Fields))
	defaultRef := ""
	for _, f := range detail.Fields {
		if f.Reference == "" {
			continue
		}
		labelStr := fmt.Sprintf("%s  (%s)", firstNonEmpty(f.Label, f.ID), friendlyFieldType(f.Type, f.Purpose))
		fieldOpts = append(fieldOpts, huh.NewOption(labelStr, f.Reference))
		if defaultRef == "" && f.Type == "CONCEALED" {
			defaultRef = f.Reference
		}
	}
	if len(fieldOpts) == 0 {
		return "", fmt.Errorf("item %q exposes no referenceable fields", detail.Title)
	}
	if defaultRef == "" {
		defaultRef = fieldOpts[0].Value
	}

	// Skip the field picker entirely when there's only one usable field.
	if len(fieldOpts) == 1 {
		return fieldOpts[0].Value, nil
	}

	ref := defaultRef
	if err := huh.NewSelect[string]().
		Title(fmt.Sprintf("Pick the field from \"%s\"", detail.Title)).
		Description("↑/↓ to move  ·  enter to select").
		Options(fieldOpts...).
		Value(&ref).
		Run(); err != nil {
		return "", err
	}
	return ref, nil
}

// firstNonEmpty returns the first argument whose trimmed value is
// non-empty, or "" if all are empty/whitespace.
func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// credentialPriority orders categories so API-credential-like items
// surface first in the picker.
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

// looksLikeEnvVarName accepts conventional uppercase-underscore env var
// names. Cases like `Path` or `my-var` are rejected since they wouldn't
// survive cross-shell handling reliably.
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

// ----- helpers shared with other subcommands -----

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func verifyExternalTools(out io.Writer, cfg *config.Config) {
	claudeBin := cfg.ClaudeBinary
	if claudeBin == "" {
		claudeBin = "claude"
	}
	type tool struct {
		name     string
		optional bool
	}
	tools := []tool{{"git", false}, {"gh", false}, {claudeBin, true}}
	for _, t := range tools {
		path, err := exec.LookPath(t.name)
		if err != nil {
			marker := "✗"
			if t.optional {
				marker = "?"
			}
			fmt.Fprintf(out, "  %s %s — not found on PATH\n", marker, t.name)
		} else {
			fmt.Fprintf(out, "  ✓ %s — %s\n", t.name, path)
		}
	}
}
