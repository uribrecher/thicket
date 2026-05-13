package main

import (
	"bytes"
	"strings"
	"testing"
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
	for _, name := range []string{"start", "init", "list", "rm", "catalog", "doctor", "version"} {
		if !strings.Contains(out, name) {
			t.Errorf("help missing subcommand %q", name)
		}
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
