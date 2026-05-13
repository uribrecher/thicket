package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/uribrecher/thicket/internal/config"
)

func runInit(_ *cobra.Command, _ []string) error {
	cfgPath, err := config.Path()
	if err != nil {
		return err
	}

	// Load existing config if present so the wizard pre-fills.
	cfg, err := config.Load(cfgPath)
	if errors.Is(err, config.ErrNoConfig) || cfg == nil {
		d := config.Default()
		cfg = &d
	} else if err != nil {
		// Don't fail the wizard on parse errors — start from defaults but
		// warn the user so they don't silently lose data.
		fmt.Fprintf(os.Stderr, "warning: existing config at %s is invalid (%v); starting fresh.\n",
			cfgPath, err)
		d := config.Default()
		cfg = &d
	}

	store := config.DefaultStore(cfg)
	existingShortcut, _ := store.Get(config.SecretShortcutAPIToken)
	existingAnthropic, _ := store.Get(config.SecretAnthropicAPIKey)

	orgs := strings.Join(cfg.GithubOrgs, ",")
	shortcutToken := ""
	anthropicKey := ""

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().
				Title("Welcome to thicket").
				Description("Walk through the prerequisites once. You can re-run `thicket init` any time to change values."),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Shortcut API token").
				Description(prefilledLabel(existingShortcut, "leave blank to keep existing")).
				Password(true).
				Value(&shortcutToken),
			huh.NewInput().
				Title("Anthropic API key").
				Description(prefilledLabel(existingAnthropic, "leave blank to keep existing")).
				Password(true).
				Value(&anthropicKey),
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

	if shortcutToken != "" {
		if err := store.Set(config.SecretShortcutAPIToken, shortcutToken); err != nil {
			return fmt.Errorf("store shortcut token: %w", err)
		}
	}
	if anthropicKey != "" {
		if err := store.Set(config.SecretAnthropicAPIKey, anthropicKey); err != nil {
			return fmt.Errorf("store anthropic key: %w", err)
		}
	}
	cfg.GithubOrgs = splitCSV(orgs)
	if cfg.ClaudeModel == "" {
		cfg.ClaudeModel = config.Default().ClaudeModel
	}
	if cfg.ClaudeBinary == "" {
		cfg.ClaudeBinary = config.Default().ClaudeBinary
	}
	if cfg.DefaultBranch == "" {
		cfg.DefaultBranch = config.Default().DefaultBranch
	}
	if cfg.TicketSource == "" {
		cfg.TicketSource = config.Default().TicketSource
	}
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
	verifyExternalTools()
	return nil
}

func prefilledLabel(existing, blankMsg string) string {
	if existing != "" {
		return blankMsg
	}
	return "required"
}

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

func verifyExternalTools() {
	for _, bin := range []string{"git", "gh", "claude"} {
		path, err := exec.LookPath(bin)
		if err != nil {
			marker := "✗"
			if bin == "claude" {
				marker = "?"
			}
			fmt.Printf("  %s %s — not found on PATH\n", marker, bin)
		} else {
			fmt.Printf("  ✓ %s — %s\n", bin, path)
		}
	}
}
