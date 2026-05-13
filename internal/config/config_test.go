package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidate_requiresKeyFields(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{"missing repos_root", func(c *Config) { c.ReposRoot = "" }, "repos_root"},
		{"missing workspace_root", func(c *Config) { c.WorkspaceRoot = "" }, "workspace_root"},
		{"missing ticket_source", func(c *Config) { c.TicketSource = "" }, "ticket_source"},
		{"missing github_orgs", func(c *Config) { c.GithubOrgs = nil }, "github_orgs"},
		{"missing claude_model", func(c *Config) { c.ClaudeModel = "" }, "claude_model"},
		{"missing passwords.manager", func(c *Config) { c.Passwords.Manager = "" }, "passwords.manager"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.mutate(&c)
			err := c.Validate()
			if err == nil {
				t.Fatal("want validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestValidate_acceptsHealthyConfig(t *testing.T) {
	c := validConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSave_roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	in := validConfig()
	in.RepoAliases = []RepoAlias{
		{Name: "acme-scan-state-manager", Aliases: []string{"ssm"}},
	}
	if err := in.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out.ReposRoot != in.ReposRoot {
		t.Errorf("repos_root: got %q want %q", out.ReposRoot, in.ReposRoot)
	}
	if len(out.RepoAliases) != 1 || out.RepoAliases[0].Name != "acme-scan-state-manager" {
		t.Errorf("repo aliases not preserved: %+v", out.RepoAliases)
	}
}

func TestLoad_missingFile_returnsSentinel(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if !errors.Is(err, ErrNoConfig) {
		t.Fatalf("want ErrNoConfig, got %v", err)
	}
}

func TestLoad_expandsTilde(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `
repos_root      = "~/code"
workspace_root  = "~/tasks"
default_branch  = "main"
claude_model    = "claude-haiku-4-5"
claude_binary   = "claude"
ticket_source   = "shortcut"
github_orgs     = ["acme"]
[passwords]
manager = "env"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	home, _ := os.UserHomeDir()
	if c.ReposRoot != filepath.Join(home, "code") {
		t.Errorf("repos_root not expanded: %q", c.ReposRoot)
	}
	if c.WorkspaceRoot != filepath.Join(home, "tasks") {
		t.Errorf("workspace_root not expanded: %q", c.WorkspaceRoot)
	}
}

func TestSave_writesFilePerm0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.toml")
	c := validConfig()
	if err := c.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("got perm %o, want 0600", perm)
	}
}

func validConfig() Config {
	return Config{
		ReposRoot:     "/tmp/code",
		WorkspaceRoot: "/tmp/tasks",
		DefaultBranch: "main",
		ClaudeModel:   "claude-haiku-4-5",
		ClaudeBinary:  "claude",
		TicketSource:  "shortcut",
		GithubOrgs:    []string{"acme"},
		Passwords:     PasswordsConfig{Manager: "env"},
	}
}
