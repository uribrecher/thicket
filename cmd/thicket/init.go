package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/uribrecher/thicket/internal/config"
	"github.com/uribrecher/thicket/internal/secrets"
)

func runInit(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	cfgPath, err := config.Path()
	if err != nil {
		return err
	}

	cfg, err := config.Load(cfgPath)
	if errors.Is(err, config.ErrNoConfig) || cfg == nil {
		d := config.Default()
		cfg = &d
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "warning: existing config at %s is invalid (%v); starting fresh.\n",
			cfgPath, err)
		d := config.Default()
		cfg = &d
	}

	// Step 1: paths, orgs (no secrets here — trust gained gradually).
	if err := runBaseConfigForm(cfg); err != nil {
		return err
	}

	// Step 2: password manager — choose, then verify CLI is available + unlocked.
	mgr, err := chooseAndVerifyManager(ctx, cfg)
	if err != nil {
		return err
	}

	// Step 3: per-secret item references. We only ever ask for the *ref*,
	// never the value; we test-fetch each one immediately to fail fast.
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

	fmt.Printf("\nconfig written to %s\n", cfgPath)
	verifyExternalTools(cfg)
	return nil
}

// ----- Step 1 -----

func runBaseConfigForm(cfg *config.Config) error {
	orgs := strings.Join(cfg.GithubOrgs, ",")

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().
				Title("Welcome to thicket").
				Description("Walk through the prerequisites once. You can re-run `thicket init` any time to change values.\n\nThicket never asks you to paste a raw API token. Instead, you point it at your password manager and we fetch on demand."),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("GitHub orgs").
				Description("Comma-separated list (e.g. sentrasec,my-org)").
				Value(&orgs).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("at least one GitHub org is required")
					}
					return nil
				}),
			huh.NewInput().
				Title("Where do your repo clones live? (repos_root)").
				Value(&cfg.ReposRoot),
			huh.NewInput().
				Title("Where should new workspaces be created? (workspace_root)").
				Value(&cfg.WorkspaceRoot),
		),
	)
	if err := form.Run(); err != nil {
		return err
	}
	cfg.GithubOrgs = splitCSV(orgs)
	fillDefaults(cfg)
	return nil
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

// ----- Step 2 -----

func chooseAndVerifyManager(ctx context.Context, cfg *config.Config) (secrets.Manager, error) {
	options := make([]huh.Option[string], 0, len(secrets.Supported))
	for _, id := range secrets.Supported {
		// Construct an instance just to call Describe() — cheap, no network/exec.
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
				Description("Thicket fetches API tokens from your password manager on demand. Pick the one you already use. If you don't use any of these, choose \"env\" and we'll read from environment variables."),
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

	// 1Password may have multiple signed-in accounts; pick one before
	// constructing the manager so subsequent op calls scope correctly.
	var opts secrets.Options
	if choice == "1password" {
		account, err := pickOnePasswordAccount(ctx, cfg.Passwords.OnePassword.Account)
		if err != nil {
			return nil, err
		}
		cfg.Passwords.OnePassword.Account = account
		opts.OnePasswordAccount = account
	}

	mgr, err := secrets.New(choice, opts)
	if err != nil {
		return nil, err
	}
	// Deliberately no Check() here: for 1Password it would do
	// `op --account <X> vault list`, which fires biometric auth before
	// we have a clear reason for the user to grant it. The first
	// actually-needed call (item-list / test-fetch) prompts naturally
	// and is scoped to the chosen account.
	return mgr, nil
}

// pickOnePasswordAccount lists 1Password accounts known to the local op
// CLI and, if more than one exists, presents a picker. Single-account
// users get an automatic pass-through. Returns the account UUID (stable
// across email changes), or "" when only one account is signed in and we
// can let op pick its default.
func pickOnePasswordAccount(ctx context.Context, current string) (string, error) {
	accs, err := secrets.ListOnePasswordAccounts(ctx)
	if err != nil {
		return "", fmt.Errorf("list 1Password accounts: %w", err)
	}
	if len(accs) == 0 {
		return "", errors.New("no 1Password accounts signed in — run `op signin` first")
	}
	if len(accs) == 1 {
		fmt.Printf("  ✓ 1Password: using account %s (%s)\n", accs[0].Email, accs[0].URL)
		return accs[0].AccountUUID, nil
	}

	opts := make([]huh.Option[string], 0, len(accs))
	for _, a := range accs {
		opts = append(opts, huh.NewOption(
			fmt.Sprintf("%s  (%s)", a.Email, a.URL),
			a.AccountUUID,
		))
	}
	choice := current
	if choice == "" {
		choice = accs[0].AccountUUID
	}
	if err := huh.NewSelect[string]().
		Title("Which 1Password account holds your dev tokens?").
		Description("Pick one. You can change this later by re-running `thicket init`.").
		Options(opts...).
		Value(&choice).
		Run(); err != nil {
		return "", err
	}
	return choice, nil
}

// ----- Step 3 -----

type secretSlot struct {
	label   string
	current *string
}

func collectSecretRefs(ctx context.Context, cfg *config.Config, mgr secrets.Manager) error {
	slots := []secretSlot{
		{"Shortcut API token", &cfg.Passwords.ShortcutTokenRef},
		{"Anthropic API key", &cfg.Passwords.AnthropicKeyRef},
	}

	// 1Password gets the nice item/field picker; other managers stick
	// with the simpler text-input path (no API exists to enumerate
	// `pass` / `bw` items uniformly here).
	if op, ok := mgr.(*secrets.OnePassword); ok {
		return collectSecretRefs1Password(ctx, op, slots)
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
		val := *s.current
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
		*s.current = strings.TrimSpace(val)
		if envMode {
			fmt.Printf("  ✓ %s — recorded (will read $%s at runtime)\n", s.label, *s.current)
		} else {
			fmt.Printf("  ✓ %s — fetched OK\n", s.label)
		}
	}
	return nil
}

// collectSecretRefs1Password fetches the user's 1Password items once,
// then presents an autocomplete picker per secret. After picking an
// item, the user picks which field to use; the canonical op:// ref
// comes straight from `op item get`'s `reference` value.
func collectSecretRefs1Password(ctx context.Context, op *secrets.OnePassword, slots []secretSlot) error {
	fmt.Println()
	fmt.Println("Loading your 1Password items… (first time today may prompt for biometric auth)")
	items, err := op.ListItems(ctx)
	if err != nil {
		return fmt.Errorf("list 1Password items: %w", err)
	}
	if len(items) == 0 {
		return errors.New("no 1Password items visible to this account")
	}
	fmt.Printf("  ✓ loaded %d items\n\n", len(items))

	// Sort: items with credential-like categories first, then alpha by
	// vault then title. Doesn't hide anything — just orders the picker
	// so the likely-correct items rise to the top.
	sort.SliceStable(items, func(i, j int) bool {
		ip := credentialPriority(items[i].Category)
		jp := credentialPriority(items[j].Category)
		if ip != jp {
			return ip > jp
		}
		if items[i].Vault.Name != items[j].Vault.Name {
			return items[i].Vault.Name < items[j].Vault.Name
		}
		return items[i].Title < items[j].Title
	})

	itemOptions := make([]huh.Option[string], 0, len(items))
	for _, it := range items {
		label := fmt.Sprintf("%s  ·  %s  ·  %s", it.Title, it.Vault.Name, friendlyCategory(it.Category))
		itemOptions = append(itemOptions, huh.NewOption(label, it.ID))
	}

	for _, s := range slots {
		ref, err := pick1PasswordRef(ctx, op, itemOptions, s.label)
		if err != nil {
			return err
		}
		*s.current = ref
		fmt.Printf("  ✓ %s — %s\n", s.label, ref)
	}
	return nil
}

// pick1PasswordRef shows the item picker, then the field picker, then
// returns the canonical op:// reference.
func pick1PasswordRef(ctx context.Context, op *secrets.OnePassword,
	itemOptions []huh.Option[string], label string) (string, error) {

	var itemID string
	if err := huh.NewSelect[string]().
		Title(fmt.Sprintf("%s — pick a 1Password item", label)).
		Description("Type to filter. Showing all items across your vaults.").
		Options(itemOptions...).
		Filtering(true).
		Height(15).
		Value(&itemID).
		Run(); err != nil {
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
		labelStr := fmt.Sprintf("%s  (%s)", coalesce(f.Label, f.ID), friendlyFieldType(f.Type, f.Purpose))
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

	ref := defaultRef
	if err := huh.NewSelect[string]().
		Title(fmt.Sprintf("%s — which field?", label)).
		Description(fmt.Sprintf("Item: %s", detail.Title)).
		Options(fieldOpts...).
		Filtering(true).
		Value(&ref).
		Run(); err != nil {
		return "", err
	}
	return ref, nil
}

func coalesce(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
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

func verifyExternalTools(cfg *config.Config) {
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
			fmt.Printf("  %s %s — not found on PATH\n", marker, t.name)
		} else {
			fmt.Printf("  ✓ %s — %s\n", t.name, path)
		}
	}
}
