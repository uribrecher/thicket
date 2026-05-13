package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/uribrecher/thicket/internal/config"
	"github.com/uribrecher/thicket/internal/secrets"
)

func runDoctor(cmd *cobra.Command, _ []string) error {
	report := []check{}

	// Config
	cfgPath, err := config.Path()
	if err != nil {
		report = append(report, fail("config dir", err.Error()))
		printReport(report)
		return err
	}
	cfg, err := config.Load(cfgPath)
	switch {
	case errors.Is(err, config.ErrNoConfig):
		report = append(report, fail("config file", "missing — run `thicket init`"))
		printReport(report)
		return errors.New("doctor: setup required")
	case err != nil:
		report = append(report, fail("config file", err.Error()))
		printReport(report)
		return err
	}
	report = append(report, ok("config file", cfgPath))
	report = append(report, checkConfigValues(cfg)...)
	report = append(report, checkSecrets(cmd.Context(), cfg)...)

	// External tools
	report = append(report, checkBinary("git"))
	report = append(report, checkBinary("gh"))
	report = append(report, checkBinary("claude"))

	printReport(report)
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

func printReport(report []check) {
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
		fmt.Printf("%-7s %-22s %s\n", prefix, c.name, c.detail)
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
	mgr, err := secrets.New(c.Passwords.Manager, secrets.Options{
		OnePasswordAccount: c.Passwords.OnePassword.Account,
	})
	if err != nil {
		return append(out, fail("password manager", err.Error()))
	}
	if c.Passwords.Manager == "1password" && c.Passwords.OnePassword.Account != "" {
		out = append(out, ok("1password account", c.Passwords.OnePassword.Account))
	}
	if err := mgr.Check(ctx); err != nil {
		switch {
		case errors.Is(err, secrets.ErrCLIMissing):
			return append(out, fail("password manager",
				fmt.Sprintf("%s: CLI not installed", c.Passwords.Manager)))
		case errors.Is(err, secrets.ErrNotAuthenticated):
			return append(out, fail("password manager",
				fmt.Sprintf("%s: not signed in / unlocked", c.Passwords.Manager)))
		default:
			return append(out, fail("password manager", err.Error()))
		}
	}
	out = append(out, ok("password manager", c.Passwords.Manager+" — installed, unlocked"))

	for _, sec := range []struct {
		label, ref string
	}{
		{"shortcut token", c.Passwords.ShortcutTokenRef},
		{"anthropic key", c.Passwords.AnthropicKeyRef},
	} {
		if sec.ref == "" {
			out = append(out, fail(sec.label, "no reference set — run `thicket init`"))
			continue
		}
		_, err := mgr.Get(ctx, sec.ref)
		if err != nil {
			out = append(out, fail(sec.label, fmt.Sprintf("%s: %v", sec.ref, err)))
			continue
		}
		out = append(out, ok(sec.label, "fetched OK from "+c.Passwords.Manager))
	}
	return out
}

func checkBinary(name string) check {
	path, err := exec.LookPath(name)
	if err != nil {
		level := statusFail
		hint := "not found on PATH"
		// Claude is the only optional binary — without it we just print
		// the cd hint instead of auto-launching.
		if name == "claude" {
			level = statusWarn
			hint = "not found on PATH (auto-launch disabled; everything else works)"
		}
		return check{name: name + " binary", detail: hint, status: level}
	}
	return ok(name+" binary", path)
}
