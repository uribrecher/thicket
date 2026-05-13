package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uribrecher/thicket/internal/git"
	"github.com/uribrecher/thicket/internal/memory"
)

func sh(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, stderr.String())
	}
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sh(t, dir, "git", "init", "-q", "-b", "main")
	sh(t, dir, "git", "config", "user.email", "t@example.com")
	sh(t, dir, "git", "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, dir, "git", "add", ".")
	sh(t, dir, "git", "commit", "-q", "-m", "init")
}

func basePlan(root string) Plan {
	srcA := filepath.Join(root, "src", "alpha")
	srcB := filepath.Join(root, "src", "beta")
	wsDir := filepath.Join(root, "tasks", "sc-1-x")
	return Plan{
		WorkspaceDir: wsDir,
		Branch:       "u/sc-1-x",
		Repos: []PlanRepo{
			{Name: "alpha", SourcePath: srcA, WorktreePath: filepath.Join(wsDir, "alpha"), BranchExists: false},
			{Name: "beta", SourcePath: srcB, WorktreePath: filepath.Join(wsDir, "beta"), BranchExists: false},
		},
		Memory: memory.Input{
			TicketID: "sc-1", Title: "X", Body: "body",
			Branch: "u/sc-1-x", WorkspaceDir: wsDir,
			Repos: []memory.RepoEntry{
				{Name: "alpha", Branch: "u/sc-1-x", WorktreePath: filepath.Join(wsDir, "alpha"), DefaultBranch: "main"},
				{Name: "beta", Branch: "u/sc-1-x", WorktreePath: filepath.Join(wsDir, "beta"), DefaultBranch: "main"},
			},
			CreatedAt: time.Date(2026, 5, 13, 12, 30, 0, 0, time.UTC),
		},
	}
}

func TestCreate_happyPath(t *testing.T) {
	root := t.TempDir()
	p := basePlan(root)
	initRepo(t, p.Repos[0].SourcePath)
	initRepo(t, p.Repos[1].SourcePath)

	w := New(git.New())
	if err := w.Create(p); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Worktrees exist and are on the branch
	g := git.New()
	for _, r := range p.Repos {
		got, err := g.CurrentBranch(r.WorktreePath)
		if err != nil {
			t.Errorf("current branch %s: %v", r.Name, err)
		}
		if got != p.Branch {
			t.Errorf("%s branch = %q, want %q", r.Name, got, p.Branch)
		}
	}
	// CLAUDE.local.md exists
	if _, err := os.Stat(filepath.Join(p.WorkspaceDir, memory.FileName)); err != nil {
		t.Errorf("memory file missing: %v", err)
	}
	// State manifest written
	st, err := ReadState(p.WorkspaceDir)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if st.TicketID != "sc-1" || st.Branch != "u/sc-1-x" || len(st.Repos) != 2 {
		t.Errorf("state mismatch: %+v", st)
	}
}

func TestCreate_collisionReturnsErrExists(t *testing.T) {
	root := t.TempDir()
	p := basePlan(root)
	if err := os.MkdirAll(p.WorkspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := New(git.New()).Create(p)
	if !errors.Is(err, ErrExists) {
		t.Fatalf("want ErrExists, got %v", err)
	}
}

func TestCreate_rollbackOnFailure(t *testing.T) {
	root := t.TempDir()
	p := basePlan(root)
	initRepo(t, p.Repos[0].SourcePath)
	// beta's source intentionally not initialised → second AddWorktree
	// fails → rollback should clean alpha + workspace dir.
	if err := os.MkdirAll(p.Repos[1].SourcePath, 0o755); err != nil {
		t.Fatal(err)
	}

	err := New(git.New()).Create(p)
	if err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(p.WorkspaceDir); !os.IsNotExist(err) {
		t.Errorf("workspace dir should be removed after rollback, stat=%v", err)
	}
	// alpha's worktree should also be gone
	if _, err := os.Stat(p.Repos[0].WorktreePath); !os.IsNotExist(err) {
		t.Errorf("alpha worktree should be gone, stat=%v", err)
	}
}

func TestRemove_cleansWorktreesAndDir(t *testing.T) {
	root := t.TempDir()
	p := basePlan(root)
	initRepo(t, p.Repos[0].SourcePath)
	initRepo(t, p.Repos[1].SourcePath)
	w := New(git.New())
	if err := w.Create(p); err != nil {
		t.Fatal(err)
	}
	if err := w.Remove(p.WorkspaceDir, true); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(p.WorkspaceDir); !os.IsNotExist(err) {
		t.Errorf("workspace dir should be gone, stat=%v", err)
	}
	// Source repos should NOT list the worktrees anymore
	for _, r := range p.Repos {
		out, err := exec.Command("git", "-C", r.SourcePath, "worktree", "list").Output()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), r.WorktreePath) {
			t.Errorf("%s still lists worktree: %s", r.Name, out)
		}
	}
}

func TestRemove_preservesWorkspaceWhenWorktreeRemovalFails(t *testing.T) {
	root := t.TempDir()
	p := basePlan(root)
	initRepo(t, p.Repos[0].SourcePath)
	initRepo(t, p.Repos[1].SourcePath)
	w := New(git.New())
	if err := w.Create(p); err != nil {
		t.Fatal(err)
	}
	// Dirty up the first worktree so a non-force remove will refuse it.
	if err := os.WriteFile(filepath.Join(p.Repos[0].WorktreePath, "dirty.txt"),
		[]byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.Remove(p.WorkspaceDir, false); err == nil {
		t.Fatal("expected error when worktree removal fails")
	}
	// Critically: the workspace dir must still exist with the user's file.
	if _, err := os.Stat(p.WorkspaceDir); err != nil {
		t.Errorf("workspace should be preserved when removal fails: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.Repos[0].WorktreePath, "dirty.txt")); err != nil {
		t.Errorf("uncommitted file should survive: %v", err)
	}
}

func TestRemove_noManifest_refusesWithoutForce(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ws")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := New(git.New()).Remove(dir, false)
	if !errors.Is(err, ErrNoState) {
		t.Fatalf("want ErrNoState, got %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dir should be preserved when refused, stat=%v", err)
	}
}

func TestRemove_noManifest_forceDeletes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ws")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := New(git.New()).Remove(dir, true); err != nil {
		t.Fatalf("force remove: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dir should be removed under force")
	}
}

func TestListManaged_skipsNonWorkspacesAndWarnsOnCorruptManifests(t *testing.T) {
	root := t.TempDir()

	// Bare directory with no .thicket/state.json — should be skipped silently.
	if err := os.MkdirAll(filepath.Join(root, "not-a-thicket-ws"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Regular file at the top level — should be ignored entirely.
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Two valid workspaces with different CreatedAt values to verify ordering.
	older := filepath.Join(root, "ws-older")
	newer := filepath.Join(root, "ws-newer")
	writeFakeState(t, older, State{
		TicketID: "sc-1", Branch: "a", CreatedAt: time.Now().Add(-2 * time.Hour),
	})
	writeFakeState(t, newer, State{
		TicketID: "sc-2", Branch: "b", CreatedAt: time.Now(),
	})
	// Corrupt manifest — should surface as a warning, not a panic.
	corrupt := filepath.Join(root, "ws-corrupt")
	if err := os.MkdirAll(filepath.Join(corrupt, ".thicket"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt, ".thicket", "state.json"),
		[]byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, warnings := ListManaged(root)

	if len(got) != 2 {
		t.Fatalf("want 2 workspaces, got %d: %+v", len(got), got)
	}
	if got[0].Slug != "ws-newer" || got[1].Slug != "ws-older" {
		t.Errorf("ordering wrong: %s then %s (want ws-newer then ws-older)",
			got[0].Slug, got[1].Slug)
	}
	if got[0].State.TicketID != "sc-2" {
		t.Errorf("newer state not propagated: %+v", got[0].State)
	}
	if len(warnings) != 1 {
		t.Fatalf("want 1 warning for corrupt manifest, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0].Error(), "ws-corrupt") {
		t.Errorf("warning should name the corrupt slug, got %v", warnings[0])
	}
}

func TestListManaged_missingRootIsNotAnError(t *testing.T) {
	got, warnings := ListManaged(filepath.Join(t.TempDir(), "does-not-exist"))
	if got != nil || warnings != nil {
		t.Errorf("missing root should return nil/nil, got %v / %v", got, warnings)
	}
}

func writeFakeState(t *testing.T, dir string, st State) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".thicket"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".thicket", "state.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSlug_alwaysPrefixesTicketID(t *testing.T) {
	cases := []struct {
		id, title, want string
	}{
		{"sc-65825", "freshness", "sc-65825-freshness"},
		{"SC-65825", "Freshness", "sc-65825-freshness"},
		{"sc-12345", "Fix inventory grouping!!", "sc-12345-fix-inventory-grouping"},
		{"sc-1", "", "sc-1"},
		{"", "just-a-title", "just-a-title"},
		{"", "", "workspace"},
		// Title contains the id — still safe: id wins as prefix.
		{"sc-1", "sc-1 follow-up", "sc-1-sc-1-follow-up"},
		// Non-ASCII letters are stripped; surrounding ASCII fuses.
		{"sc-1", "café / résumé", "sc-1-caf-rsum"},
	}
	for _, tc := range cases {
		t.Run(tc.id+"|"+tc.title, func(t *testing.T) {
			if got := Slug(tc.id, tc.title); got != tc.want {
				t.Errorf("Slug(%q,%q)=%q, want %q", tc.id, tc.title, got, tc.want)
			}
		})
	}
}

func TestSlugify_basic(t *testing.T) {
	cases := map[string]string{
		"Fix Inventory Grouping":   "fix-inventory-grouping",
		"  spaces  everywhere ":    "spaces-everywhere",
		"under_scores/and/slashes": "under-scores-and-slashes",
		"keep_123_digits":          "keep-123-digits",
		"!!!":                      "",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q)=%q, want %q", in, got, want)
		}
	}
}
