package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/adrg/xdg"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/config"
)

func TestSplitOnLastComma(t *testing.T) {
	cases := []struct {
		in              string
		wantPrefix      string
		wantPartial     string
	}{
		{"", "", ""},
		{"foo", "", "foo"},
		{"foo,", "foo,", ""},
		{"foo,b", "foo,", "b"},
		{"foo,bar,b", "foo,bar,", "b"},
		{",", ",", ""},
	}
	for _, c := range cases {
		gotP, gotR := splitOnLastComma(c.in)
		if gotP != c.wantPrefix || gotR != c.wantPartial {
			t.Errorf("splitOnLastComma(%q) = (%q,%q), want (%q,%q)",
				c.in, gotP, gotR, c.wantPrefix, c.wantPartial)
		}
	}
}

// setupCompletionEnv points XDG_CONFIG_HOME + XDG_CACHE_HOME at fresh
// temp dirs, drops a minimal valid config.toml whose workspace_root is
// also under the temp tree, and returns the workspace_root for the
// caller to populate. xdg.Reload is invoked so config.Path and
// catalog.Path observe the new env in the same process.
func setupCompletionEnv(t *testing.T) (workspaceRoot string) {
	t.Helper()
	cfgHome := t.TempDir()
	cacheHome := t.TempDir()
	wsRoot := t.TempDir()

	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	xdg.Reload()
	t.Cleanup(func() { xdg.Reload() })

	cfgPath, err := config.Path()
	if err != nil {
		t.Fatalf("config.Path: %v", err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = wsRoot
	cfg.ReposRoot = t.TempDir()
	cfg.GithubOrgs = []string{"acme"}
	cfg.Passwords.Manager = "env"
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatalf("config.Save: %v", err)
	}
	return wsRoot
}

// writeWorkspaceFixture lands a minimally-valid .thicket/state.json
// inside wsRoot/<slug> so ListManaged picks it up.
func writeWorkspaceFixture(t *testing.T, wsRoot, slug, nickname string) {
	t.Helper()
	dir := filepath.Join(wsRoot, slug, ".thicket")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	state := map[string]any{
		"ticket_id":  "sc-1",
		"branch":     "uri/" + slug,
		"nickname":   nickname,
		"created_at": time.Now().UTC().Format(time.RFC3339),
		"repos":      []any{},
	}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), b, 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}
}

func TestCompleteWorkspaceSlugs_emitsManagedSlugs(t *testing.T) {
	wsRoot := setupCompletionEnv(t)
	writeWorkspaceFixture(t, wsRoot, "sc-1-fix-a", "fix A")
	writeWorkspaceFixture(t, wsRoot, "sc-2-fix-b", "")
	// Bare directory with no manifest — ListManaged must ignore it.
	if err := os.MkdirAll(filepath.Join(wsRoot, "stray-dir"), 0o755); err != nil {
		t.Fatalf("mkdir stray: %v", err)
	}

	out, _, err := runCmd(t, "__complete", "rm", "")
	if err != nil {
		t.Fatalf("__complete rm: %v", err)
	}
	if !strings.Contains(out, "sc-1-fix-a\tfix A") {
		t.Errorf("expected nickname-decorated slug in completion output, got:\n%s", out)
	}
	if !strings.Contains(out, "sc-2-fix-b") {
		t.Errorf("expected sc-2-fix-b in completion output, got:\n%s", out)
	}
	if strings.Contains(out, "stray-dir") {
		t.Errorf("manifest-less directory leaked into completions:\n%s", out)
	}

	// Once a positional slug is already supplied, no further completions.
	out, _, err = runCmd(t, "__complete", "rm", "sc-1-fix-a", "")
	if err != nil {
		t.Fatalf("__complete rm <slug>: %v", err)
	}
	for _, slug := range []string{"sc-1-fix-a", "sc-2-fix-b"} {
		if strings.Contains(out, slug) {
			t.Errorf("expected no slug completions after positional arg, got:\n%s", out)
		}
	}
}

func TestCompleteWorkspaceSlugs_editAlsoCompletes(t *testing.T) {
	wsRoot := setupCompletionEnv(t)
	writeWorkspaceFixture(t, wsRoot, "sc-9-edit-me", "")

	out, _, err := runCmd(t, "__complete", "edit", "")
	if err != nil {
		t.Fatalf("__complete edit: %v", err)
	}
	if !strings.Contains(out, "sc-9-edit-me") {
		t.Errorf("edit completion missing slug, got:\n%s", out)
	}
}

func TestCompleteWorkspaceSlugs_silentWhenNoConfig(t *testing.T) {
	// Fresh XDG with no config saved — completion must not error out.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	xdg.Reload()
	t.Cleanup(func() { xdg.Reload() })

	out, _, err := runCmd(t, "__complete", "rm", "")
	if err != nil {
		t.Fatalf("__complete rm with no config: %v", err)
	}
	// First line of cobra's __complete output is the suggestions; the
	// directive footer follows. We just need to confirm no panic and
	// no slug suggestions.
	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		t.Errorf("unexpected suggestion line with no config: %q", line)
	}
}

func TestCompleteCatalogRepos_filtersPartialAndExcludesTyped(t *testing.T) {
	setupCompletionEnv(t)
	cachePath, err := catalog.Path()
	if err != nil {
		t.Fatalf("catalog.Path: %v", err)
	}
	repos := []catalog.Repo{
		{Name: "service-alpha"},
		{Name: "service-beta"},
		{Name: "unrelated"},
	}
	if err := catalog.Save(cachePath, repos); err != nil {
		t.Fatalf("catalog.Save: %v", err)
	}

	// Plain prefix match.
	out, _, err := runCmd(t, "__complete", "start", "--only", "serv")
	if err != nil {
		t.Fatalf("__complete --only: %v", err)
	}
	got := suggestionLines(out)
	wantContains := []string{"service-alpha", "service-beta"}
	for _, w := range wantContains {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %q in completions: %v", w, got)
		}
	}
	for _, g := range got {
		if g == "unrelated" {
			t.Errorf("unrelated repo leaked into prefix-filtered completions: %v", got)
		}
	}

	// Comma-separated: already-typed repo dropped, prefix preserved.
	out, _, err = runCmd(t, "__complete", "start", "--only", "service-alpha,serv")
	if err != nil {
		t.Fatalf("__complete --only comma: %v", err)
	}
	got = suggestionLines(out)
	sort.Strings(got)
	want := []string{"service-alpha,service-beta"}
	if !equalStrings(got, want) {
		t.Errorf("comma completions: got %v want %v", got, want)
	}
}

// suggestionLines extracts the value column (pre-tab) from cobra's
// __complete output, skipping blank lines and the trailing directive
// footer (lines starting with `:`).
func suggestionLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if i := strings.IndexByte(line, '\t'); i >= 0 {
			line = line[:i]
		}
		lines = append(lines, line)
	}
	return lines
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
