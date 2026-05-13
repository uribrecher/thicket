package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/uribrecher/thicket/internal/config"
	"github.com/uribrecher/thicket/internal/secrets"
)

func runDoctor(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	report := []check{}

	// Config
	cfgPath, err := config.Path()
	if err != nil {
		report = append(report, fail("config dir", err.Error()))
		printReport(out, report)
		return err
	}
	cfg, err := config.Load(cfgPath)
	switch {
	case errors.Is(err, config.ErrNoConfig):
		report = append(report, fail("config file", "missing — run `thicket init`"))
		printReport(out, report)
		return errors.New("doctor: setup required")
	case err != nil:
		report = append(report, fail("config file", err.Error()))
		printReport(out, report)
		return err
	}
	report = append(report, ok("config file", cfgPath))
	report = append(report, checkConfigValues(cfg)...)
	report = append(report, checkSecrets(cmd.Context(), cfg)...)

	// External tools
	report = append(report, checkBinary("git", "git"))
	report = append(report, checkBinary("gh", "gh"))
	claudeBin := cfg.ClaudeBinary
	if claudeBin == "" {
		claudeBin = "claude"
	}
	report = append(report, checkBinary(claudeBin, "claude"))

	printReport(out, report)
	for _, c := range report {
		if c.status == statusFail {
			return errors.New("doctor: some checks failed")
		}
	}
	return nil
}

type checkStatus int

const (
	statusOK checkStatus = iota
	statusWarn
	statusFail
)

type check struct {
	name   string
	detail string
	status checkStatus
}

func ok(name, detail string) check   { return check{name, detail, statusOK} }
func fail(name, detail string) check { return check{name, detail, statusFail} }

func printReport(w io.Writer, report []check) {
	for _, c := range report {
		var prefix string
		switch c.status {
		case statusOK:
			prefix = "[ok]"
		case statusWarn:
			prefix = "[warn]"
		case statusFail:
			prefix = "[fail]"
		}
		fmt.Fprintf(w, "%-7s %-22s %s\n", prefix, c.name, c.detail)
	}
}

func checkConfigValues(c *config.Config) []check {
	var out []check
	out = append(out, ok("repos_root", c.ReposRoot))
	out = append(out, ok("workspace_root", c.WorkspaceRoot))
	out = append(out, ok("ticket_source", c.TicketSource))
	if len(c.GithubOrgs) == 0 {
		out = append(out, fail("github_orgs", "none configured"))
	} else {
		out = append(out, ok("github_orgs", fmt.Sprintf("%v", c.GithubOrgs)))
	}
	out = append(out, ok("claude_model", c.ClaudeModel))
	return out
}

func checkSecrets(ctx context.Context, c *config.Config) []check {
	var out []check
	if c.Passwords.Manager == "" {
		return append(out, fail("password manager", "not configured — run `thicket init`"))
	}
	out = append(out, ok("password manager", c.Passwords.Manager))

	// Each secret carries its own account (1Password only) so we
	// construct a fresh manager per fetch and report status per-secret.
	backend := c.ClaudeBackend
	if backend == "" {
		backend = "cli"
	}
	type secCheck struct {
		label, ref, account string
		skip                bool
	}
	checks := []secCheck{
		{"shortcut token", c.Passwords.ShortcutTokenRef, c.Passwords.ShortcutTokenAccount, false},
		{"anthropic key", c.Passwords.AnthropicKeyRef, c.Passwords.AnthropicKeyAccount, backend == "cli"},
	}
	for _, sec := range checks {
		if sec.skip {
			out = append(out, ok(sec.label, "skipped (claude_backend=cli)"))
			continue
		}
		if sec.ref == "" {
			out = append(out, fail(sec.label, "no reference set — run `thicket init`"))
			continue
		}
		mgr, err := secrets.New(c.Passwords.Manager, secrets.Options{
			OnePasswordAccount: sec.account,
		})
		if err != nil {
			out = append(out, fail(sec.label, err.Error()))
			continue
		}
		_, err = mgr.Get(ctx, sec.ref)
		if err != nil {
			out = append(out, fail(sec.label, fmt.Sprintf("%s: %v", sec.ref, err)))
			continue
		}
		out = append(out, ok(sec.label, "fetched OK from "+c.Passwords.Manager))
	}
	return out
}

// checkBinary looks up `name` on PATH. The `kind` argument is the logical
// role ("git", "gh", "claude") — it determines whether a missing binary is
// fatal (git, gh) or merely a warning (claude, since launch is optional)
// and is also used as the report label.
func checkBinary(name, kind string) check {
	path, err := exec.LookPath(name)
	if err != nil {
		if kind == "claude" {
			return check{
				name:   "claude binary",
				detail: fmt.Sprintf("%s not found on PATH (auto-launch disabled; everything else works)", name),
				status: statusWarn,
			}
		}
		return check{name: kind + " binary", detail: name + " not found on PATH", status: statusFail}
	}
	return ok(kind+" binary", path)
}
