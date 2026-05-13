package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	if err := mgr.Check(ctx); err != nil {
		return nil, fmt.Errorf("%s check failed: %w — install the CLI and sign in, then re-run `thicket init`",
			choice, err)
	}
	fmt.Printf("  ✓ %s CLI is installed and unlocked\n", choice)
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

func collectSecretRefs(ctx context.Context, cfg *config.Config, mgr secrets.Manager) error {
	fmt.Println()
	fmt.Println("For each secret below, enter the item reference in your password manager.")
	fmt.Printf("  Reference format: %s\n\n", mgr.Describe())

	// env mode records env-var *names* — the variables themselves may not
	// be set at init time (they'll be set in CI / the shell config), so we
	// only validate the name shape and skip the live fetch.
	envMode := mgr.Name() == "env"

	type slot struct {
		label   string
		current *string
	}
	slots := []slot{
		{"Shortcut API token reference", &cfg.Passwords.ShortcutTokenRef},
		{"Anthropic API key reference", &cfg.Passwords.AnthropicKeyRef},
	}

	for _, s := range slots {
		val := *s.current
		err := huh.NewInput().
			Title(s.label).
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
