package main

import (
	"errors"
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/uribrecher/thicket/internal/config"
)

func runDoctor(_ *cobra.Command, _ []string) error {
	report := []check{}

	// Config
	cfgPath, err := config.Path()
	if err != nil {
		report = append(report, fail("config dir", err.Error()))
	} else {
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
		default:
			report = append(report, ok("config file", cfgPath))
			report = append(report, checkConfigValues(cfg)...)
			report = append(report, checkSecrets(cfg)...)
		}
	}

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
func warn(name, detail string) check { return check{name, detail, statusWarn} }
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

func checkSecrets(c *config.Config) []check {
	store := config.DefaultStore(c)
	var out []check
	for _, sec := range []struct {
		key, label string
	}{
		{config.SecretShortcutAPIToken, "shortcut token"},
		{config.SecretAnthropicAPIKey, "anthropic key"},
	} {
		if v, err := store.Get(sec.key); err == nil && v != "" {
			out = append(out, ok(sec.label, "found"))
		} else {
			out = append(out, fail(sec.label, "missing — run `thicket init`"))
		}
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
