package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/uribrecher/thicket/internal/config"
)

func runCmd(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	root := newRootCmd()
	root.SilenceErrors = true
	root.SilenceUsage = true
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errBuf.String(), err
}

func TestRoot_help_listsAllSubcommands(t *testing.T) {
	out, _, err := runCmd(t, "--help")
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	for _, name := range []string{"start", "config", "list", "rm", "catalog", "doctor", "version"} {
		if !strings.Contains(out, name) {
			t.Errorf("help missing subcommand %q", name)
		}
	}
}

// TestRoot_init_rejected guards the `thicket init` → `thicket config`
// rename: the old verb must not silently dispatch to anything.
func TestRoot_init_rejected(t *testing.T) {
	_, _, err := runCmd(t, "init")
	if err == nil {
		t.Fatalf("expected error from `thicket init`, got nil (rename to `config` regressed?)")
	}
}

func TestStart_help_listsFlags(t *testing.T) {
	out, _, err := runCmd(t, "start", "--help")
	if err != nil {
		t.Fatalf("start --help: %v", err)
	}
	for _, flag := range []string{"--only", "--branch", "--no-interactive", "--no-launch", "--dry-run"} {
		if !strings.Contains(out, flag) {
			t.Errorf("start help missing flag %q", flag)
		}
	}
}

func TestVersion_printsBakedValues(t *testing.T) {
	out, _, err := runCmd(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.HasPrefix(out, "thicket ") {
		t.Errorf("version output unexpected: %q", out)
	}
}

func TestUnknownSubcommand_errors(t *testing.T) {
	_, _, err := runCmd(t, "nonsense")
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
}

func TestFetchSecret_envVarOverrideShortcuts(t *testing.T) {
	t.Setenv("SHORTCUT_API_TOKEN", "from-env")
	cfg := &config.Config{} // no manager configured at all
	got, err := fetchSecret(context.Background(), cfg, secretShortcut)
	if err != nil {
		t.Fatalf("fetchSecret: %v", err)
	}
	if got != "from-env" {
		t.Errorf("got %q, want %q", got, "from-env")
	}
}

func TestFetchSecret_envVarOverride_forAnthropic(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "k-from-env")
	cfg := &config.Config{}
	got, err := fetchSecret(context.Background(), cfg, secretAnthropic)
	if err != nil {
		t.Fatal(err)
	}
	if got != "k-from-env" {
		t.Errorf("got %q", got)
	}
}

func TestFetchSecret_noEnvAndNoManager_errors(t *testing.T) {
	// Defensive: explicitly clear the override env vars in case the dev
	// shell has them set, so the fall-through path is exercised.
	t.Setenv("SHORTCUT_API_TOKEN", "")
	cfg := &config.Config{}
	_, err := fetchSecret(context.Background(), cfg, secretShortcut)
	if err == nil || !strings.Contains(err.Error(), "manager") {
		t.Errorf("want manager-not-configured error, got %v", err)
	}
}

func TestEnvVarFor_knownKinds(t *testing.T) {
	if got := envVarFor(secretShortcut); got != "SHORTCUT_API_TOKEN" {
		t.Errorf("shortcut env = %q", got)
	}
	if got := envVarFor(secretAnthropic); got != "ANTHROPIC_API_KEY" {
		t.Errorf("anthropic env = %q", got)
	}
}
