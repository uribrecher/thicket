package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/uribrecher/thicket/internal/config"
	"github.com/uribrecher/thicket/internal/tui"
	"github.com/uribrecher/thicket/internal/tui/wizard"
	cfgwiz "github.com/uribrecher/thicket/internal/tui/wizard/config"
)

func runConfig(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	cfgPath, err := config.Path()
	if err != nil {
		return err
	}

	firstRun := false
	cfg, err := config.Load(cfgPath)
	if errors.Is(err, config.ErrNoConfig) || cfg == nil {
		d := config.Default()
		cfg = &d
		firstRun = true
	} else if err != nil {
		fmt.Fprintf(errOut, "warning: existing config at %s is invalid (%v); starting fresh.\n",
			cfgPath, err)
		d := config.Default()
		cfg = &d
		firstRun = true
	}

	res, err := cfgwiz.Run(wizard.ConfigDeps{
		Ctx:      ctx,
		Cfg:      cfg,
		FirstRun: firstRun,
	})
	if err != nil {
		if errors.Is(err, tui.ErrCancelled) {
			fmt.Fprintln(out, "cancelled.")
			return nil
		}
		return err
	}
	if res.DeferredForToken {
		fmt.Fprintln(out, "Opened Shortcut's API tokens page in your browser.")
		fmt.Fprintln(out, "Re-run `thicket config` once you've saved the token to your password manager (or as $SHORTCUT_API_TOKEN).")
		return nil
	}
	if !res.Confirmed || res.Cfg == nil {
		fmt.Fprintln(out, "cancelled.")
		return nil
	}

	fillDefaults(res.Cfg)
	if err := res.Cfg.ExpandPaths(); err != nil {
		return err
	}
	if err := res.Cfg.Validate(); err != nil {
		return err
	}
	if err := res.Cfg.Save(cfgPath); err != nil {
		return err
	}
	if err := os.MkdirAll(res.Cfg.WorkspaceRoot, 0o755); err != nil {
		fmt.Fprintf(errOut, "warning: could not create workspace_root %s: %v\n",
			res.Cfg.WorkspaceRoot, err)
	}

	fmt.Fprintf(out, "\nconfig written to %s\n", cfgPath)
	warnAboutEmptyOrgs(errOut, res.Cfg.GithubOrgs)
	verifyExternalTools(out, res.Cfg)
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
	if cfg.Passwords.Manager == "" {
		// All secrets covered by env vars — record "env" as the
		// notional manager so Validate() (which requires Manager) is
		// happy.
		cfg.Passwords.Manager = "env"
	}
}

// warnAboutEmptyOrgs probes each configured org with `gh repo list`. If
// gh succeeds but returns zero repos for some, names those (and lists
// the orgs the gh user actually belongs to). If gh itself errors, we
// say so honestly instead of pretending every org is empty — a missing
// `gh auth login` is a different problem than a typo'd org name.
func warnAboutEmptyOrgs(errOut io.Writer, orgs []string) {
	var empties []string
	for _, org := range orgs {
		out, err := exec.Command("gh", "repo", "list", org, "--limit", "1", "--json", "name").Output()
		if err != nil {
			fmt.Fprintf(errOut, "\nwarning: could not query org %q via gh: %v\n", org, err)
			fmt.Fprintln(errOut, "  (run `gh auth status` to check; thicket won't be able to list repos until this is fixed)")
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
	fmt.Fprintf(errOut, "\nwarning: no visible repos in %v\n", empties)
	if memberships, err := exec.Command("gh", "api", "user/orgs", "--jq", ".[].login").Output(); err == nil {
		got := strings.TrimSpace(string(memberships))
		if got != "" {
			fmt.Fprintf(errOut, "  orgs your gh user belongs to: %s\n",
				strings.Join(strings.Split(got, "\n"), ", "))
		}
	}
	fmt.Fprintln(errOut, "  edit ~/.config/thicket/config.toml or re-run `thicket config` to fix.")
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
